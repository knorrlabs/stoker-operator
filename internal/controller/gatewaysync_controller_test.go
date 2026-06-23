package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/go-git/go-git/v5/plumbing/transport"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	stokerv1alpha1 "github.com/knorrlabs/stoker-operator/api/v1alpha1"
	"github.com/knorrlabs/stoker-operator/internal/git"
	"github.com/knorrlabs/stoker-operator/pkg/conditions"
	stokertypes "github.com/knorrlabs/stoker-operator/pkg/types"
)

// fakeGitClient is a test double for git.Client.
type fakeGitClient struct {
	result git.Result
	err    error
	calls  int
}

func (f *fakeGitClient) LsRemote(_ context.Context, _, _ string, _ transport.AuthMethod) (git.Result, error) {
	f.calls++
	return f.result, f.err
}

func (f *fakeGitClient) CloneOrFetch(_ context.Context, _, _, _ string, _ transport.AuthMethod) (git.Result, error) {
	f.calls++
	return f.result, f.err
}

// helper to create the reconciler with a fake git client and event recorder
func newReconciler(gitClient git.Client) *GatewaySyncReconciler {
	return &GatewaySyncReconciler{
		Client:    k8sClient,
		Scheme:    k8sClient.Scheme(),
		GitClient: gitClient,
		Recorder:  record.NewFakeRecorder(20),
	}
}

// helper to create the required gateway API key secret
func createAPIKeySecret(ctx context.Context, name string) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
		},
		Data: map[string][]byte{
			"apiKey": []byte("test-api-key"),
		},
	}
	err := k8sClient.Create(ctx, secret)
	if err != nil && !errors.IsAlreadyExists(err) {
		Expect(err).NotTo(HaveOccurred())
	}
}

// helper to create a GatewaySync CR with a default profile
func createCR(ctx context.Context, name, secretName string) {
	cr := &stokerv1alpha1.GatewaySync{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
		},
		Spec: stokerv1alpha1.GatewaySyncSpec{
			Git: stokerv1alpha1.GitSpec{
				Repo: "git@github.com:example/test.git",
				Ref:  "main",
			},
			Gateway: stokerv1alpha1.GatewaySpec{
				API: stokerv1alpha1.GatewayAPISpec{
					SecretName: secretName,
					SecretKey:  "apiKey",
				},
			},
			Sync: stokerv1alpha1.SyncSpec{
				Profiles: map[string]stokerv1alpha1.SyncProfileSpec{
					"default": {
						Mappings: []stokerv1alpha1.SyncMapping{
							{Source: "config", Destination: "config"},
						},
					},
				},
			},
		},
	}
	Expect(k8sClient.Create(ctx, cr)).To(Succeed())
}

// helper to create a pod annotated for gateway discovery and set it to Running
func createAnnotatedPod(ctx context.Context, name string, annotations map[string]string) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   "default",
			Annotations: annotations,
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "ignition", Image: "inductiveautomation/ignition-gateway:latest"},
			},
		},
	}
	Expect(k8sClient.Create(ctx, pod)).To(Succeed())

	// Set pod status to Running (envtest doesn't run kubelet)
	pod.Status.Phase = corev1.PodRunning
	Expect(k8sClient.Status().Update(ctx, pod)).To(Succeed())
}

// helper to run through the full reconcile cycle (finalizer + ref resolution) and return
func reconcileToSteadyState(ctx context.Context, nn types.NamespacedName, r *GatewaySyncReconciler) {
	// Reconcile 1: add finalizer
	_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
	Expect(err).NotTo(HaveOccurred())

	// Reconcile 2: resolve ref + discover gateways + update conditions
	_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
	Expect(err).NotTo(HaveOccurred())
}

var _ = Describe("GatewaySync Controller", func() {

	Context("Finalizer handling", func() {
		const resourceName = "test-finalizer"
		const secretName = "test-secret-finalizer"
		ctx := context.Background()
		nn := types.NamespacedName{Name: resourceName, Namespace: "default"}

		BeforeEach(func() {
			createAPIKeySecret(ctx, secretName)
			createCR(ctx, resourceName, secretName)
		})

		AfterEach(func() {
			cr := &stokerv1alpha1.GatewaySync{}
			if err := k8sClient.Get(ctx, nn, cr); err == nil {
				controllerutil.RemoveFinalizer(cr, stokertypes.Finalizer)
				_ = k8sClient.Update(ctx, cr)
				_ = k8sClient.Delete(ctx, cr)
			}
		})

		It("should add finalizer on first reconcile", func() {
			r := newReconciler(&fakeGitClient{result: git.Result{Commit: "abc123", Ref: "main"}})
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())

			cr := &stokerv1alpha1.GatewaySync{}
			Expect(k8sClient.Get(ctx, nn, cr)).To(Succeed())
			Expect(controllerutil.ContainsFinalizer(cr, stokertypes.Finalizer)).To(BeTrue())
		})

		It("should remove finalizer and clean up on deletion", func() {
			r := newReconciler(&fakeGitClient{result: git.Result{Commit: "abc123", Ref: "main"}})
			reconcileToSteadyState(ctx, nn, r)

			// Create a ConfigMap that should be cleaned up
			cm := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("stoker-metadata-%s", resourceName),
					Namespace: "default",
				},
				Data: map[string]string{"commit": "abc123"},
			}
			// The reconciler should have created this, but ensure it exists
			existing := &corev1.ConfigMap{}
			err := k8sClient.Get(ctx, types.NamespacedName{Name: cm.Name, Namespace: cm.Namespace}, existing)
			if errors.IsNotFound(err) {
				Expect(k8sClient.Create(ctx, cm)).To(Succeed())
			}

			// Delete the CR (DeletionTimestamp set, but finalizer blocks)
			cr := &stokerv1alpha1.GatewaySync{}
			Expect(k8sClient.Get(ctx, nn, cr)).To(Succeed())
			Expect(k8sClient.Delete(ctx, cr)).To(Succeed())

			// Reconcile: cleanup + remove finalizer
			_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())

			// ConfigMap should be gone
			err = k8sClient.Get(ctx, types.NamespacedName{Name: cm.Name, Namespace: "default"}, &corev1.ConfigMap{})
			Expect(errors.IsNotFound(err)).To(BeTrue())

			// CR should be gone (finalizer removed -> GC)
			Eventually(func() bool {
				err := k8sClient.Get(ctx, nn, &stokerv1alpha1.GatewaySync{})
				return errors.IsNotFound(err)
			}, 5*time.Second, 100*time.Millisecond).Should(BeTrue())
		})
	})

	Context("Ref resolution lifecycle", func() {
		const resourceName = "test-ref"
		const secretName = "test-secret-ref"
		ctx := context.Background()
		nn := types.NamespacedName{Name: resourceName, Namespace: "default"}

		BeforeEach(func() {
			createAPIKeySecret(ctx, secretName)
			createCR(ctx, resourceName, secretName)
		})

		AfterEach(func() {
			cr := &stokerv1alpha1.GatewaySync{}
			if err := k8sClient.Get(ctx, nn, cr); err == nil {
				controllerutil.RemoveFinalizer(cr, stokertypes.Finalizer)
				_ = k8sClient.Update(ctx, cr)
				_ = k8sClient.Delete(ctx, cr)
			}
			cm := &corev1.ConfigMap{}
			cmNN := types.NamespacedName{Name: fmt.Sprintf("stoker-metadata-%s", resourceName), Namespace: "default"}
			if err := k8sClient.Get(ctx, cmNN, cm); err == nil {
				_ = k8sClient.Delete(ctx, cm)
			}
		})

		It("should set RefResolved condition and create metadata ConfigMap after ref resolution", func() {
			gitClient := &fakeGitClient{result: git.Result{Commit: "abc123def", Ref: "main"}}
			r := newReconciler(gitClient)

			// Reconcile 1: add finalizer
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())

			// Reconcile 2: resolve ref via ls-remote
			result, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(60 * time.Second)) // default polling interval

			// Verify status
			cr := &stokerv1alpha1.GatewaySync{}
			Expect(k8sClient.Get(ctx, nn, cr)).To(Succeed())
			Expect(cr.Status.LastSyncCommit).To(Equal("abc123def"))
			Expect(cr.Status.LastSyncRef).To(Equal("main"))
			Expect(cr.Status.RefResolutionStatus).To(Equal("Resolved"))
			Expect(cr.Status.LastSyncTime).NotTo(BeNil())
			Expect(cr.Status.ProfileCount).To(Equal(int32(1)))

			// Verify RefResolved condition
			var refResolvedCond *metav1.Condition
			for i := range cr.Status.Conditions {
				if cr.Status.Conditions[i].Type == "RefResolved" {
					refResolvedCond = &cr.Status.Conditions[i]
					break
				}
			}
			Expect(refResolvedCond).NotTo(BeNil())
			Expect(refResolvedCond.Status).To(Equal(metav1.ConditionTrue))
			Expect(refResolvedCond.Message).To(Equal("abc123def"))

			// Verify metadata ConfigMap
			cm := &corev1.ConfigMap{}
			cmNN := types.NamespacedName{
				Name:      fmt.Sprintf("stoker-metadata-%s", resourceName),
				Namespace: "default",
			}
			Expect(k8sClient.Get(ctx, cmNN, cm)).To(Succeed())
			Expect(cm.Data["commit"]).To(Equal("abc123def"))
			Expect(cm.Data["ref"]).To(Equal("main"))
			Expect(cm.Data["profiles"]).NotTo(BeEmpty())

			// Git client should have been called exactly once
			Expect(gitClient.calls).To(Equal(1))
		})

		It("should clear requested-ref annotation once spec.git.ref catches up", func() {
			// Simulate the webhook fast-path: annotation overrides spec.git.ref while
			// ArgoCD is still syncing. Once spec.git.ref matches the annotation, the
			// controller must remove it so a future failed webhook doesn't pin the ref.

			cr := &stokerv1alpha1.GatewaySync{}
			Expect(k8sClient.Get(ctx, nn, cr)).To(Succeed())
			if cr.Annotations == nil {
				cr.Annotations = make(map[string]string)
			}
			// Annotation uses the git tag as-is ("v1.2.3"); spec.git.ref uses "1.2.3".
			// The controller must treat these as equivalent (v-prefix normalization).
			cr.Annotations[stokertypes.AnnotationRequestedRef] = "v1.2.3"
			Expect(k8sClient.Update(ctx, cr)).To(Succeed())

			gitClient := &fakeGitClient{result: git.Result{Commit: "deadbeef", Ref: "v1.2.3"}}
			r := newReconciler(gitClient)

			// Reconcile 1: add finalizer (spec.git.ref="main", annotation="v1.2.3")
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())

			// Simulate ArgoCD catching up: spec.git.ref updated to "1.2.3" (no "v" prefix,
			// matching the values.yaml convention). This must still trigger annotation clearing.
			Expect(k8sClient.Get(ctx, nn, cr)).To(Succeed())
			cr.Spec.Git.Ref = "1.2.3"
			Expect(k8sClient.Update(ctx, cr)).To(Succeed())

			// Reconcile 2: annotation "v1.2.3" normalizes to "1.2.3" == spec.git.ref →
			// annotation should be cleared.
			_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())

			updated := &stokerv1alpha1.GatewaySync{}
			Expect(k8sClient.Get(ctx, nn, updated)).To(Succeed())
			Expect(updated.Annotations).NotTo(HaveKey(stokertypes.AnnotationRequestedRef),
				"requested-ref annotation should be cleared once spec.git.ref matches (v-prefix normalized)")
		})

		It("should set error condition when ref resolution fails", func() {
			gitClient := &fakeGitClient{err: fmt.Errorf("authentication failed")}
			r := newReconciler(gitClient)

			// Reconcile 1: add finalizer
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())

			// Reconcile 2: ref resolution fails
			result, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred()) // controller handles errors gracefully
			Expect(result.RequeueAfter).To(Equal(30 * time.Second))

			// Verify error condition
			cr := &stokerv1alpha1.GatewaySync{}
			Expect(k8sClient.Get(ctx, nn, cr)).To(Succeed())
			Expect(cr.Status.RefResolutionStatus).To(Equal("Error"))

			var refResolvedCond *metav1.Condition
			for i := range cr.Status.Conditions {
				if cr.Status.Conditions[i].Type == "RefResolved" {
					refResolvedCond = &cr.Status.Conditions[i]
					break
				}
			}
			Expect(refResolvedCond).NotTo(BeNil())
			Expect(refResolvedCond.Status).To(Equal(metav1.ConditionFalse))
			Expect(refResolvedCond.Reason).To(Equal("RefResolutionFailed"))
		})
	})

	Context("Secret validation", func() {
		const resourceName = "test-validation"
		ctx := context.Background()
		nn := types.NamespacedName{Name: resourceName, Namespace: "default"}

		AfterEach(func() {
			cr := &stokerv1alpha1.GatewaySync{}
			if err := k8sClient.Get(ctx, nn, cr); err == nil {
				controllerutil.RemoveFinalizer(cr, stokertypes.Finalizer)
				_ = k8sClient.Update(ctx, cr)
				_ = k8sClient.Delete(ctx, cr)
			}
		})

		It("should resolve ref but requeue when gateway API key secret is missing", func() {
			cr := &stokerv1alpha1.GatewaySync{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName,
					Namespace: "default",
				},
				Spec: stokerv1alpha1.GatewaySyncSpec{
					Git: stokerv1alpha1.GitSpec{
						Repo: "git@github.com:example/test.git",
						Ref:  "main",
					},
					Gateway: stokerv1alpha1.GatewaySpec{
						API: stokerv1alpha1.GatewayAPISpec{
							SecretName: "nonexistent-secret",
							SecretKey:  "apiKey",
						},
					},
					Sync: stokerv1alpha1.SyncSpec{
						Profiles: map[string]stokerv1alpha1.SyncProfileSpec{
							"default": {
								Mappings: []stokerv1alpha1.SyncMapping{
									{Source: "config", Destination: "config"},
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, cr)).To(Succeed())

			gitClient := &fakeGitClient{result: git.Result{Commit: "abc123", Ref: "main"}}
			r := newReconciler(gitClient)

			// Reconcile 1: add finalizer
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())

			// Reconcile 2: ref resolves successfully, but API key secret missing triggers requeue
			result, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(30 * time.Second))

			// Git client should have been called (ref resolution not blocked)
			Expect(gitClient.calls).To(Equal(1))

			// Verify ref was resolved despite missing API key secret
			updated := &stokerv1alpha1.GatewaySync{}
			Expect(k8sClient.Get(ctx, nn, updated)).To(Succeed())
			Expect(updated.Status.RefResolutionStatus).To(Equal("Resolved"))
			Expect(updated.Status.LastSyncCommit).To(Equal("abc123"))

			// Verify Ready condition reflects the missing secret
			var readyCond *metav1.Condition
			for i := range updated.Status.Conditions {
				if updated.Status.Conditions[i].Type == conditions.TypeReady {
					readyCond = &updated.Status.Conditions[i]
					break
				}
			}
			Expect(readyCond).NotTo(BeNil())
			Expect(readyCond.Status).To(Equal(metav1.ConditionFalse))
			Expect(readyCond.Message).To(ContainSubstring("not found"))
		})
	})

	Context("Paused CR", func() {
		const resourceName = "test-paused"
		const secretName = "test-secret-paused"
		ctx := context.Background()
		nn := types.NamespacedName{Name: resourceName, Namespace: "default"}

		BeforeEach(func() {
			createAPIKeySecret(ctx, secretName)
		})

		AfterEach(func() {
			cr := &stokerv1alpha1.GatewaySync{}
			if err := k8sClient.Get(ctx, nn, cr); err == nil {
				controllerutil.RemoveFinalizer(cr, stokertypes.Finalizer)
				_ = k8sClient.Update(ctx, cr)
				_ = k8sClient.Delete(ctx, cr)
			}
		})

		It("should skip reconciliation when paused", func() {
			cr := &stokerv1alpha1.GatewaySync{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName,
					Namespace: "default",
				},
				Spec: stokerv1alpha1.GatewaySyncSpec{
					Git: stokerv1alpha1.GitSpec{
						Repo: "git@github.com:example/test.git",
						Ref:  "main",
					},
					Gateway: stokerv1alpha1.GatewaySpec{
						API: stokerv1alpha1.GatewayAPISpec{
							SecretName: secretName,
							SecretKey:  "apiKey",
						},
					},
					Sync: stokerv1alpha1.SyncSpec{
						Profiles: map[string]stokerv1alpha1.SyncProfileSpec{
							"default": {
								Mappings: []stokerv1alpha1.SyncMapping{
									{Source: "config", Destination: "config"},
								},
							},
						},
					},
					Paused: true,
				},
			}
			Expect(k8sClient.Create(ctx, cr)).To(Succeed())

			gitClient := &fakeGitClient{result: git.Result{Commit: "abc123", Ref: "main"}}
			r := newReconciler(gitClient)

			// Reconcile 1: add finalizer
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())

			// Reconcile 2: paused — should not call git
			result, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(BeZero())
			Expect(gitClient.calls).To(Equal(0))
		})
	})

	Context("Gateway discovery", func() {
		const resourceName = "test-discovery"
		const secretName = "test-secret-discovery"
		ctx := context.Background()
		nn := types.NamespacedName{Name: resourceName, Namespace: "default"}

		BeforeEach(func() {
			createAPIKeySecret(ctx, secretName)
			createCR(ctx, resourceName, secretName)
		})

		AfterEach(func() {
			cr := &stokerv1alpha1.GatewaySync{}
			if err := k8sClient.Get(ctx, nn, cr); err == nil {
				controllerutil.RemoveFinalizer(cr, stokertypes.Finalizer)
				_ = k8sClient.Update(ctx, cr)
				_ = k8sClient.Delete(ctx, cr)
			}
			// Clean up pods
			for _, name := range []string{"gw-pod-1", "gw-pod-2", "gw-pod-other"} {
				pod := &corev1.Pod{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: "default"}, pod); err == nil {
					_ = k8sClient.Delete(ctx, pod)
				}
			}
			// Clean up ConfigMaps
			for _, prefix := range []string{"stoker-metadata-", "stoker-status-"} {
				cm := &corev1.ConfigMap{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: prefix + resourceName, Namespace: "default"}, cm); err == nil {
					_ = k8sClient.Delete(ctx, cm)
				}
			}
		})

		It("should discover annotated Running pods as gateways", func() {
			// Create two annotated pods for this CR
			createAnnotatedPod(ctx, "gw-pod-1", map[string]string{
				stokertypes.AnnotationCRName:      resourceName,
				stokertypes.AnnotationGatewayName: "gateway-alpha",
			})
			createAnnotatedPod(ctx, "gw-pod-2", map[string]string{
				stokertypes.AnnotationCRName:      resourceName,
				stokertypes.AnnotationGatewayName: "gateway-beta",
			})

			r := newReconciler(&fakeGitClient{result: git.Result{Commit: "abc123", Ref: "main"}})
			reconcileToSteadyState(ctx, nn, r)

			// Verify discovered gateways
			cr := &stokerv1alpha1.GatewaySync{}
			Expect(k8sClient.Get(ctx, nn, cr)).To(Succeed())
			Expect(cr.Status.DiscoveredGateways).To(HaveLen(2))

			names := []string{cr.Status.DiscoveredGateways[0].Name, cr.Status.DiscoveredGateways[1].Name}
			Expect(names).To(ContainElements("gateway-alpha", "gateway-beta"))
		})

		It("should not discover pods for a different CR", func() {
			// Pod for this CR
			createAnnotatedPod(ctx, "gw-pod-1", map[string]string{
				stokertypes.AnnotationCRName: resourceName,
			})
			// Pod for a different CR
			createAnnotatedPod(ctx, "gw-pod-other", map[string]string{
				stokertypes.AnnotationCRName: "some-other-cr",
			})

			r := newReconciler(&fakeGitClient{result: git.Result{Commit: "abc123", Ref: "main"}})
			reconcileToSteadyState(ctx, nn, r)

			cr := &stokerv1alpha1.GatewaySync{}
			Expect(k8sClient.Get(ctx, nn, cr)).To(Succeed())
			Expect(cr.Status.DiscoveredGateways).To(HaveLen(1))
			Expect(cr.Status.DiscoveredGateways[0].PodName).To(Equal("gw-pod-1"))
		})

		It("should fall back to pod name when gateway name annotation is missing", func() {
			createAnnotatedPod(ctx, "gw-pod-1", map[string]string{
				stokertypes.AnnotationCRName: resourceName,
				// No AnnotationGatewayName — should fall back
			})

			r := newReconciler(&fakeGitClient{result: git.Result{Commit: "abc123", Ref: "main"}})
			reconcileToSteadyState(ctx, nn, r)

			cr := &stokerv1alpha1.GatewaySync{}
			Expect(k8sClient.Get(ctx, nn, cr)).To(Succeed())
			Expect(cr.Status.DiscoveredGateways).To(HaveLen(1))
			Expect(cr.Status.DiscoveredGateways[0].Name).To(Equal("gw-pod-1"))
		})
	})

	Context("Gateway status collection", func() {
		const resourceName = "test-status"
		const secretName = "test-secret-status"
		ctx := context.Background()
		nn := types.NamespacedName{Name: resourceName, Namespace: "default"}

		BeforeEach(func() {
			createAPIKeySecret(ctx, secretName)
			createCR(ctx, resourceName, secretName)
		})

		AfterEach(func() {
			cr := &stokerv1alpha1.GatewaySync{}
			if err := k8sClient.Get(ctx, nn, cr); err == nil {
				controllerutil.RemoveFinalizer(cr, stokertypes.Finalizer)
				_ = k8sClient.Update(ctx, cr)
				_ = k8sClient.Delete(ctx, cr)
			}
			pod := &corev1.Pod{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: "gw-status-pod", Namespace: "default"}, pod); err == nil {
				_ = k8sClient.Delete(ctx, pod)
			}
			for _, prefix := range []string{"stoker-metadata-", "stoker-status-"} {
				cm := &corev1.ConfigMap{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: prefix + resourceName, Namespace: "default"}, cm); err == nil {
					_ = k8sClient.Delete(ctx, cm)
				}
			}
		})

		It("should enrich gateways with status from ConfigMap", func() {
			createAnnotatedPod(ctx, "gw-status-pod", map[string]string{
				stokertypes.AnnotationCRName:      resourceName,
				stokertypes.AnnotationGatewayName: "my-gateway",
			})

			// Create the status ConfigMap (as an agent would)
			statusData := stokertypes.GatewayStatus{
				SyncStatus:       stokertypes.SyncStatusSynced,
				SyncedCommit:     "def456",
				SyncedRef:        "main",
				LastSyncTime:     "2026-01-15T10:30:00Z",
				LastSyncDuration: "2.5s",
				AgentVersion:     "1.2.3",
				FilesChanged:     3,
				ProjectsSynced:   []string{"MyProject"},
			}
			statusJSON, err := json.Marshal(statusData)
			Expect(err).NotTo(HaveOccurred())

			statusCM := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("stoker-status-%s", resourceName),
					Namespace: "default",
				},
				Data: map[string]string{
					"my-gateway": string(statusJSON),
				},
			}
			Expect(k8sClient.Create(ctx, statusCM)).To(Succeed())

			r := newReconciler(&fakeGitClient{result: git.Result{Commit: "abc123", Ref: "main"}})
			reconcileToSteadyState(ctx, nn, r)

			cr := &stokerv1alpha1.GatewaySync{}
			Expect(k8sClient.Get(ctx, nn, cr)).To(Succeed())
			Expect(cr.Status.DiscoveredGateways).To(HaveLen(1))

			gw := cr.Status.DiscoveredGateways[0]
			Expect(gw.SyncStatus).To(Equal(stokertypes.SyncStatusSynced))
			Expect(gw.SyncedCommit).To(Equal("def456"))
			Expect(gw.AgentVersion).To(Equal("1.2.3"))
			Expect(gw.FilesChanged).To(Equal(int32(3)))
			Expect(gw.ProjectsSynced).To(Equal([]string{"MyProject"}))
			Expect(gw.LastSyncTime).NotTo(BeNil())
		})
	})

	Context("Condition aggregation", func() {
		const resourceName = "test-conditions"
		const secretName = "test-secret-conditions"
		ctx := context.Background()
		nn := types.NamespacedName{Name: resourceName, Namespace: "default"}

		BeforeEach(func() {
			createAPIKeySecret(ctx, secretName)
			createCR(ctx, resourceName, secretName)
		})

		AfterEach(func() {
			cr := &stokerv1alpha1.GatewaySync{}
			if err := k8sClient.Get(ctx, nn, cr); err == nil {
				controllerutil.RemoveFinalizer(cr, stokertypes.Finalizer)
				_ = k8sClient.Update(ctx, cr)
				_ = k8sClient.Delete(ctx, cr)
			}
			for _, name := range []string{"gw-cond-1", "gw-cond-2"} {
				pod := &corev1.Pod{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: "default"}, pod); err == nil {
					_ = k8sClient.Delete(ctx, pod)
				}
			}
			for _, prefix := range []string{"stoker-metadata-", "stoker-status-"} {
				cm := &corev1.ConfigMap{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: prefix + resourceName, Namespace: "default"}, cm); err == nil {
					_ = k8sClient.Delete(ctx, cm)
				}
			}
		})

		It("should set Ready=True when ref is resolved and all gateways synced", func() {
			createAnnotatedPod(ctx, "gw-cond-1", map[string]string{
				stokertypes.AnnotationCRName:      resourceName,
				stokertypes.AnnotationGatewayName: "gw1",
			})

			// Create status ConfigMap with Synced status
			statusJSON, _ := json.Marshal(stokertypes.GatewayStatus{
				SyncStatus:   stokertypes.SyncStatusSynced,
				SyncedCommit: "abc123",
			})
			statusCM := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("stoker-status-%s", resourceName),
					Namespace: "default",
				},
				Data: map[string]string{"gw1": string(statusJSON)},
			}
			Expect(k8sClient.Create(ctx, statusCM)).To(Succeed())

			r := newReconciler(&fakeGitClient{result: git.Result{Commit: "abc123", Ref: "main"}})
			reconcileToSteadyState(ctx, nn, r)

			cr := &stokerv1alpha1.GatewaySync{}
			Expect(k8sClient.Get(ctx, nn, cr)).To(Succeed())

			// Check Ready condition
			var readyCond *metav1.Condition
			for i := range cr.Status.Conditions {
				if cr.Status.Conditions[i].Type == "Ready" {
					readyCond = &cr.Status.Conditions[i]
					break
				}
			}
			Expect(readyCond).NotTo(BeNil())
			Expect(readyCond.Status).To(Equal(metav1.ConditionTrue))

			// Check AllGatewaysSynced
			var allSyncedCond *metav1.Condition
			for i := range cr.Status.Conditions {
				if cr.Status.Conditions[i].Type == conditions.TypeAllGatewaysSynced {
					allSyncedCond = &cr.Status.Conditions[i]
					break
				}
			}
			Expect(allSyncedCond).NotTo(BeNil())
			Expect(allSyncedCond.Status).To(Equal(metav1.ConditionTrue))
			Expect(allSyncedCond.Message).To(Equal("1/1 synced"))
		})

		It("should set Ready=False when gateways are not all synced", func() {
			createAnnotatedPod(ctx, "gw-cond-1", map[string]string{
				stokertypes.AnnotationCRName:      resourceName,
				stokertypes.AnnotationGatewayName: "gw1",
			})
			createAnnotatedPod(ctx, "gw-cond-2", map[string]string{
				stokertypes.AnnotationCRName:      resourceName,
				stokertypes.AnnotationGatewayName: "gw2",
			})

			// Only one gateway synced
			statusJSON, _ := json.Marshal(stokertypes.GatewayStatus{
				SyncStatus:   stokertypes.SyncStatusSynced,
				SyncedCommit: "abc123",
			})
			statusCM := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("stoker-status-%s", resourceName),
					Namespace: "default",
				},
				Data: map[string]string{"gw1": string(statusJSON)},
				// gw2 not in map -> stays Pending
			}
			Expect(k8sClient.Create(ctx, statusCM)).To(Succeed())

			r := newReconciler(&fakeGitClient{result: git.Result{Commit: "abc123", Ref: "main"}})
			reconcileToSteadyState(ctx, nn, r)

			cr := &stokerv1alpha1.GatewaySync{}
			Expect(k8sClient.Get(ctx, nn, cr)).To(Succeed())

			// Ready should be False
			var readyCond *metav1.Condition
			for i := range cr.Status.Conditions {
				if cr.Status.Conditions[i].Type == "Ready" {
					readyCond = &cr.Status.Conditions[i]
					break
				}
			}
			Expect(readyCond).NotTo(BeNil())
			Expect(readyCond.Status).To(Equal(metav1.ConditionFalse))

			// AllGatewaysSynced should show 1/2
			var allSyncedCond *metav1.Condition
			for i := range cr.Status.Conditions {
				if cr.Status.Conditions[i].Type == conditions.TypeAllGatewaysSynced {
					allSyncedCond = &cr.Status.Conditions[i]
					break
				}
			}
			Expect(allSyncedCond).NotTo(BeNil())
			Expect(allSyncedCond.Status).To(Equal(metav1.ConditionFalse))
			Expect(allSyncedCond.Message).To(Equal("1/2 synced"))
		})

		It("should set AllGatewaysSynced=False with NoGateways when no pods found", func() {
			// No annotated pods created
			r := newReconciler(&fakeGitClient{result: git.Result{Commit: "abc123", Ref: "main"}})
			reconcileToSteadyState(ctx, nn, r)

			cr := &stokerv1alpha1.GatewaySync{}
			Expect(k8sClient.Get(ctx, nn, cr)).To(Succeed())

			var allSyncedCond *metav1.Condition
			for i := range cr.Status.Conditions {
				if cr.Status.Conditions[i].Type == conditions.TypeAllGatewaysSynced {
					allSyncedCond = &cr.Status.Conditions[i]
					break
				}
			}
			Expect(allSyncedCond).NotTo(BeNil())
			Expect(allSyncedCond.Status).To(Equal(metav1.ConditionFalse))
			Expect(allSyncedCond.Reason).To(Equal("NoGatewaysDiscovered"))
		})
	})

	Context("Exponential backoff", func() {
		const resourceName = "test-backoff"
		const secretName = "test-secret-backoff"
		ctx := context.Background()
		nn := types.NamespacedName{Name: resourceName, Namespace: "default"}

		BeforeEach(func() {
			createAPIKeySecret(ctx, secretName)
			createCR(ctx, resourceName, secretName)
		})

		AfterEach(func() {
			cr := &stokerv1alpha1.GatewaySync{}
			if err := k8sClient.Get(ctx, nn, cr); err == nil {
				controllerutil.RemoveFinalizer(cr, stokertypes.Finalizer)
				_ = k8sClient.Update(ctx, cr)
				_ = k8sClient.Delete(ctx, cr)
			}
			for _, prefix := range []string{"stoker-metadata-"} {
				cm := &corev1.ConfigMap{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: prefix + resourceName, Namespace: "default"}, cm); err == nil {
					_ = k8sClient.Delete(ctx, cm)
				}
			}
		})

		It("should increase requeue delay on consecutive failures", func() {
			gitClient := &fakeGitClient{err: fmt.Errorf("connection refused")}
			r := newReconciler(gitClient)

			// Reconcile 1: add finalizer
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())

			// Reconcile 2: first failure — 30s
			result, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(30 * time.Second))

			// Reconcile 3: second failure — 60s
			result, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(60 * time.Second))

			// Reconcile 4: third failure — 120s
			result, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(120 * time.Second))
		})

		It("should cap backoff at 5 minutes", func() {
			gitClient := &fakeGitClient{err: fmt.Errorf("connection refused")}
			r := newReconciler(gitClient)

			// Add finalizer
			_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: nn})

			// Drive up failures past the cap
			for range 10 {
				_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			}

			result, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(5 * time.Minute))
		})

		It("should reset backoff on success", func() {
			gitClient := &fakeGitClient{err: fmt.Errorf("connection refused")}
			r := newReconciler(gitClient)

			// Add finalizer
			_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: nn})

			// Fail a few times
			_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			_, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			result, _ := r.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(result.RequeueAfter).To(Equal(120 * time.Second))

			// Now succeed
			gitClient.err = nil
			gitClient.result = git.Result{Commit: "abc123", Ref: "main"}
			result, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(60 * time.Second)) // normal polling

			// Change the spec.git.ref to force a cache miss on next reconcile
			cr := &stokerv1alpha1.GatewaySync{}
			Expect(k8sClient.Get(ctx, nn, cr)).To(Succeed())
			cr.Spec.Git.Ref = "v2.0.0"
			Expect(k8sClient.Update(ctx, cr)).To(Succeed())

			// Fail again — should be back to 30s (backoff was reset)
			gitClient.err = fmt.Errorf("temporary error")
			gitClient.result = git.Result{}
			result, _ = r.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(result.RequeueAfter).To(Equal(30 * time.Second))
		})
	})

	Context("SSH host key verification condition", func() {
		const resourceName = "test-ssh-warning"
		const secretName = "test-secret-ssh-warning"
		ctx := context.Background()
		nn := types.NamespacedName{Name: resourceName, Namespace: "default"}

		BeforeEach(func() {
			createAPIKeySecret(ctx, secretName)
		})

		AfterEach(func() {
			cr := &stokerv1alpha1.GatewaySync{}
			if err := k8sClient.Get(ctx, nn, cr); err == nil {
				controllerutil.RemoveFinalizer(cr, stokertypes.Finalizer)
				_ = k8sClient.Update(ctx, cr)
				_ = k8sClient.Delete(ctx, cr)
			}
			for _, prefix := range []string{"stoker-metadata-"} {
				cm := &corev1.ConfigMap{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: prefix + resourceName, Namespace: "default"}, cm); err == nil {
					_ = k8sClient.Delete(ctx, cm)
				}
			}
			// Clean up SSH key secret
			secret := &corev1.Secret{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: "test-ssh-key", Namespace: "default"}, secret); err == nil {
				_ = k8sClient.Delete(ctx, secret)
			}
		})

		It("should set SSHHostKeyVerification=False when SSH without knownHosts", func() {
			// Create the SSH key secret
			sshSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "test-ssh-key", Namespace: "default"},
				Data:       map[string][]byte{"key": []byte("fake-ssh-key")},
			}
			Expect(k8sClient.Create(ctx, sshSecret)).To(Succeed())

			cr := &stokerv1alpha1.GatewaySync{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: "default"},
				Spec: stokerv1alpha1.GatewaySyncSpec{
					Git: stokerv1alpha1.GitSpec{
						Repo: "git@github.com:example/test.git",
						Ref:  "main",
						Auth: &stokerv1alpha1.GitAuthSpec{
							SSHKey: &stokerv1alpha1.SSHKeyAuth{
								SecretRef: stokerv1alpha1.SecretKeyRef{Name: "test-ssh-key", Key: "key"},
							},
						},
					},
					Gateway: stokerv1alpha1.GatewaySpec{API: stokerv1alpha1.GatewayAPISpec{SecretName: secretName, SecretKey: "apiKey"}},
					Sync: stokerv1alpha1.SyncSpec{
						Profiles: map[string]stokerv1alpha1.SyncProfileSpec{
							"default": {Mappings: []stokerv1alpha1.SyncMapping{{Source: "config", Destination: "config"}}},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, cr)).To(Succeed())

			r := newReconciler(&fakeGitClient{result: git.Result{Commit: "abc123", Ref: "main"}})
			reconcileToSteadyState(ctx, nn, r)

			updated := &stokerv1alpha1.GatewaySync{}
			Expect(k8sClient.Get(ctx, nn, updated)).To(Succeed())

			var cond *metav1.Condition
			for i := range updated.Status.Conditions {
				if updated.Status.Conditions[i].Type == conditions.TypeSSHHostKeyVerification {
					cond = &updated.Status.Conditions[i]
					break
				}
			}
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(cond.Reason).To(Equal(conditions.ReasonHostKeyVerificationDisabled))
		})
	})

	Context("Pod to CR mapping", func() {
		It("should map annotated pod to GatewaySync reconcile request", func() {
			r := newReconciler(&fakeGitClient{})
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "default",
					Annotations: map[string]string{
						stokertypes.AnnotationCRName: "my-gs",
					},
				},
			}

			requests := r.findGatewaySyncForPod(context.Background(), pod)
			Expect(requests).To(HaveLen(1))
			Expect(requests[0].NamespacedName.Name).To(Equal("my-gs"))
			Expect(requests[0].NamespacedName.Namespace).To(Equal("default"))
		})

		It("should return nil for pods without annotation", func() {
			r := newReconciler(&fakeGitClient{})
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "unrelated-pod",
					Namespace: "default",
				},
			}

			requests := r.findGatewaySyncForPod(context.Background(), pod)
			Expect(requests).To(BeNil())
		})
	})

	Context("Profile validation", func() {
		const resourceName = "test-profile-validation"
		const secretName = "test-secret-profile-val"
		ctx := context.Background()
		nn := types.NamespacedName{Name: resourceName, Namespace: "default"}

		BeforeEach(func() {
			createAPIKeySecret(ctx, secretName)
		})

		AfterEach(func() {
			cr := &stokerv1alpha1.GatewaySync{}
			if err := k8sClient.Get(ctx, nn, cr); err == nil {
				controllerutil.RemoveFinalizer(cr, stokertypes.Finalizer)
				_ = k8sClient.Update(ctx, cr)
				_ = k8sClient.Delete(ctx, cr)
			}
			for _, prefix := range []string{"stoker-metadata-"} {
				cm := &corev1.ConfigMap{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: prefix + resourceName, Namespace: "default"}, cm); err == nil {
					_ = k8sClient.Delete(ctx, cm)
				}
			}
		})

		It("should set ProfilesValid=True for valid profiles", func() {
			cr := &stokerv1alpha1.GatewaySync{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: "default"},
				Spec: stokerv1alpha1.GatewaySyncSpec{
					Git:     stokerv1alpha1.GitSpec{Repo: "git@github.com:example/test.git", Ref: "main"},
					Gateway: stokerv1alpha1.GatewaySpec{API: stokerv1alpha1.GatewayAPISpec{SecretName: secretName, SecretKey: "apiKey"}},
					Sync: stokerv1alpha1.SyncSpec{
						Profiles: map[string]stokerv1alpha1.SyncProfileSpec{
							"blue": {Mappings: []stokerv1alpha1.SyncMapping{{Source: "services/blue/projects", Destination: "projects"}}},
							"red":  {Mappings: []stokerv1alpha1.SyncMapping{{Source: "services/red/projects", Destination: "projects"}}},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, cr)).To(Succeed())

			r := newReconciler(&fakeGitClient{result: git.Result{Commit: "abc123", Ref: "main"}})
			reconcileToSteadyState(ctx, nn, r)

			updated := &stokerv1alpha1.GatewaySync{}
			Expect(k8sClient.Get(ctx, nn, updated)).To(Succeed())

			var cond *metav1.Condition
			for i := range updated.Status.Conditions {
				if updated.Status.Conditions[i].Type == conditions.TypeProfilesValid {
					cond = &updated.Status.Conditions[i]
					break
				}
			}
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionTrue))
		})

		It("should set ProfilesValid=False for path traversal", func() {
			cr := &stokerv1alpha1.GatewaySync{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: "default"},
				Spec: stokerv1alpha1.GatewaySyncSpec{
					Git:     stokerv1alpha1.GitSpec{Repo: "git@github.com:example/test.git", Ref: "main"},
					Gateway: stokerv1alpha1.GatewaySpec{API: stokerv1alpha1.GatewayAPISpec{SecretName: secretName, SecretKey: "apiKey"}},
					Sync: stokerv1alpha1.SyncSpec{
						Profiles: map[string]stokerv1alpha1.SyncProfileSpec{
							"evil": {Mappings: []stokerv1alpha1.SyncMapping{{Source: "../../../etc/passwd", Destination: "config"}}},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, cr)).To(Succeed())

			r := newReconciler(&fakeGitClient{result: git.Result{Commit: "abc123", Ref: "main"}})
			reconcileToSteadyState(ctx, nn, r)

			updated := &stokerv1alpha1.GatewaySync{}
			Expect(k8sClient.Get(ctx, nn, updated)).To(Succeed())

			var cond *metav1.Condition
			for i := range updated.Status.Conditions {
				if updated.Status.Conditions[i].Type == conditions.TypeProfilesValid {
					cond = &updated.Status.Conditions[i]
					break
				}
			}
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(cond.Message).To(ContainSubstring("traversal"))
		})

		It("should set ProfilesValid=False for absolute path", func() {
			cr := &stokerv1alpha1.GatewaySync{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: "default"},
				Spec: stokerv1alpha1.GatewaySyncSpec{
					Git:     stokerv1alpha1.GitSpec{Repo: "git@github.com:example/test.git", Ref: "main"},
					Gateway: stokerv1alpha1.GatewaySpec{API: stokerv1alpha1.GatewayAPISpec{SecretName: secretName, SecretKey: "apiKey"}},
					Sync: stokerv1alpha1.SyncSpec{
						Profiles: map[string]stokerv1alpha1.SyncProfileSpec{
							"evil": {Mappings: []stokerv1alpha1.SyncMapping{{Source: "/etc/passwd", Destination: "config"}}},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, cr)).To(Succeed())

			r := newReconciler(&fakeGitClient{result: git.Result{Commit: "abc123", Ref: "main"}})
			reconcileToSteadyState(ctx, nn, r)

			updated := &stokerv1alpha1.GatewaySync{}
			Expect(k8sClient.Get(ctx, nn, updated)).To(Succeed())

			var cond *metav1.Condition
			for i := range updated.Status.Conditions {
				if updated.Status.Conditions[i].Type == conditions.TypeProfilesValid {
					cond = &updated.Status.Conditions[i]
					break
				}
			}
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(cond.Message).To(ContainSubstring("absolute"))
		})
	})
})
