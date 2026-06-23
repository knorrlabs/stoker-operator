package webhook

import (
	"context"
	"encoding/json"
	"testing"

	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	stokerv1alpha1 "github.com/knorrlabs/stoker-operator/api/v1alpha1"
	stokertypes "github.com/knorrlabs/stoker-operator/pkg/types"
)

const testNamespace = "test-ns"

func newScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = stokerv1alpha1.AddToScheme(s)
	_ = corev1.AddToScheme(s)
	return s
}

func newInjector(objects ...runtime.Object) *PodInjector {
	s := newScheme()
	return &PodInjector{
		Client: fake.NewClientBuilder().
			WithScheme(s).
			WithRuntimeObjects(objects...).
			Build(),
		Decoder: admission.NewDecoder(s),
	}
}

func testGatewaySync() *stokerv1alpha1.GatewaySync {
	return &stokerv1alpha1.GatewaySync{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-sync",
			Namespace: testNamespace,
		},
		Spec: stokerv1alpha1.GatewaySyncSpec{
			Git: stokerv1alpha1.GitSpec{
				Repo: "git@github.com:example/test.git",
				Ref:  "main",
				Auth: &stokerv1alpha1.GitAuthSpec{
					Token: &stokerv1alpha1.TokenAuth{
						SecretRef: stokerv1alpha1.SecretKeyRef{
							Name: "git-token-secret",
							Key:  "token",
						},
					},
				},
			},
			Gateway: stokerv1alpha1.GatewaySpec{
				Port: 8043,
				API: stokerv1alpha1.GatewayAPISpec{
					SecretName: "api-key-secret",
					SecretKey:  "apiKey",
				},
			},
			Sync: stokerv1alpha1.SyncSpec{
				Profiles: map[string]stokerv1alpha1.SyncProfileSpec{
					"my-profile": {
						Mappings: []stokerv1alpha1.SyncMapping{
							{Source: "config/", Destination: "config/"},
						},
					},
				},
			},
		},
	}
}

func makeAdmissionRequest(pod *corev1.Pod) admission.Request {
	raw, _ := json.Marshal(pod)
	return admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			Namespace: testNamespace,
			Object:    runtime.RawExtension{Raw: raw},
		},
	}
}

func basePod(annotations map[string]string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "gateway-0",
			Namespace:   testNamespace,
			Annotations: annotations,
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "ignition-gateway", Image: "inductiveautomation/ignition:8.3.3"},
			},
		},
	}
}

// --- Test Cases ---

func TestInject_WithAllAnnotations(t *testing.T) {
	gs := testGatewaySync()
	injector := newInjector(gs)

	pod := basePod(map[string]string{
		stokertypes.AnnotationInject:      "true",
		stokertypes.AnnotationCRName:      "my-sync",
		stokertypes.AnnotationProfile:     "my-profile",
		stokertypes.AnnotationGatewayName: "blue-gw",
	})
	resp := injector.Handle(context.Background(), makeAdmissionRequest(pod))

	if !resp.Allowed {
		t.Fatalf("expected allowed, got denied: %s", resp.Result.Message)
	}
	if resp.Patches == nil {
		t.Fatal("expected patches, got nil")
	}

	// Verify mutation content via direct injection
	patched := injectDirect(t, pod, gs)
	assertHasInitContainer(t, patched, agentContainerName)
	assertHasVolume(t, patched, volumeSyncRepo)
	assertHasVolume(t, patched, volumeGitCredentials)
	assertHasVolume(t, patched, volumeAPIKey)
	assertAnnotation(t, patched, stokertypes.AnnotationInjected, "true")
}

func TestInject_WithoutInjectAnnotation(t *testing.T) {
	injector := newInjector(testGatewaySync())

	pod := basePod(map[string]string{})
	resp := injector.Handle(context.Background(), makeAdmissionRequest(pod))

	if !resp.Allowed {
		t.Fatalf("expected allowed, got denied: %s", resp.Result.Message)
	}
	if resp.Patches != nil {
		t.Fatal("expected no patches for non-annotated pod")
	}
}

func TestInject_MissingCR(t *testing.T) {
	injector := newInjector() // no CRs

	pod := basePod(map[string]string{
		stokertypes.AnnotationInject: "true",
		stokertypes.AnnotationCRName: "nonexistent",
	})
	resp := injector.Handle(context.Background(), makeAdmissionRequest(pod))

	if resp.Allowed {
		t.Fatal("expected denied for missing CR")
	}
	assertContains(t, resp.Result.Message, "not found")
}

func TestInject_PausedCR(t *testing.T) {
	gs := testGatewaySync()
	gs.Spec.Paused = true
	injector := newInjector(gs)

	pod := basePod(map[string]string{
		stokertypes.AnnotationInject: "true",
		stokertypes.AnnotationCRName: "my-sync",
	})
	resp := injector.Handle(context.Background(), makeAdmissionRequest(pod))

	if resp.Allowed {
		t.Fatal("expected denied for paused CR")
	}
	assertContains(t, resp.Result.Message, "paused")
}

func TestInject_InvalidProfile(t *testing.T) {
	injector := newInjector(testGatewaySync()) // has "my-profile" but not "nonexistent-profile"

	pod := basePod(map[string]string{
		stokertypes.AnnotationInject:  "true",
		stokertypes.AnnotationCRName:  "my-sync",
		stokertypes.AnnotationProfile: "nonexistent-profile",
	})
	resp := injector.Handle(context.Background(), makeAdmissionRequest(pod))

	if resp.Allowed {
		t.Fatal("expected denied for missing profile")
	}
	assertContains(t, resp.Result.Message, "not found")
}

func TestInject_AlreadyInjected(t *testing.T) {
	injector := newInjector(testGatewaySync())

	restartAlways := corev1.ContainerRestartPolicyAlways
	pod := basePod(map[string]string{
		stokertypes.AnnotationInject: "true",
		stokertypes.AnnotationCRName: "my-sync",
	})
	pod.Spec.InitContainers = []corev1.Container{
		{Name: agentContainerName, Image: "test:latest", RestartPolicy: &restartAlways},
	}
	resp := injector.Handle(context.Background(), makeAdmissionRequest(pod))

	if !resp.Allowed {
		t.Fatalf("expected allowed for already injected, got: %s", resp.Result.Message)
	}
	if resp.Patches != nil {
		t.Fatal("expected no patches for already injected pod")
	}
}

func TestInject_AutoDeriveCRName_SingleCR(t *testing.T) {
	gs := testGatewaySync()
	injector := newInjector(gs)

	pod := basePod(map[string]string{
		stokertypes.AnnotationInject: "true",
		// No cr-name annotation — should auto-derive
	})
	resp := injector.Handle(context.Background(), makeAdmissionRequest(pod))

	if !resp.Allowed {
		t.Fatalf("expected allowed with auto-derived CR, got: %s", resp.Result.Message)
	}
	if resp.Patches == nil {
		t.Fatal("expected patches for auto-derived injection")
	}

	// Verify cr-name annotation is written back to the pod via patch
	found := false
	for _, patch := range resp.Patches {
		if patch.Path == "/metadata/annotations/stoker.io~1cr-name" {
			if patch.Value != gs.Name {
				t.Fatalf("expected cr-name patch value %q, got %v", gs.Name, patch.Value)
			}
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected cr-name annotation to be written back to pod via patch")
	}
}

func TestInject_AutoDeriveCRName_NoCRs(t *testing.T) {
	injector := newInjector() // no CRs

	pod := basePod(map[string]string{
		stokertypes.AnnotationInject: "true",
	})
	resp := injector.Handle(context.Background(), makeAdmissionRequest(pod))

	if resp.Allowed {
		t.Fatal("expected denied when no CRs in namespace")
	}
	assertContains(t, resp.Result.Message, "no GatewaySync CR found")
}

func TestInject_AutoDeriveCRName_MultipleCRs(t *testing.T) {
	cr1 := testGatewaySync()
	cr2 := testGatewaySync()
	cr2.Name = "other-sync"
	injector := newInjector(cr1, cr2)

	pod := basePod(map[string]string{
		stokertypes.AnnotationInject: "true",
	})
	resp := injector.Handle(context.Background(), makeAdmissionRequest(pod))

	if resp.Allowed {
		t.Fatal("expected denied with multiple CRs")
	}
	assertContains(t, resp.Result.Message, "multiple GatewaySync CRs")
}

func TestInject_SSHAuth(t *testing.T) {
	gs := testGatewaySync()
	gs.Spec.Git.Auth = &stokerv1alpha1.GitAuthSpec{
		SSHKey: &stokerv1alpha1.SSHKeyAuth{
			SecretRef: stokerv1alpha1.SecretKeyRef{
				Name: "ssh-key-secret",
				Key:  "ssh-privatekey",
			},
		},
	}
	injector := newInjector(gs)

	pod := basePod(map[string]string{
		stokertypes.AnnotationInject: "true",
		stokertypes.AnnotationCRName: "my-sync",
	})
	resp := injector.Handle(context.Background(), makeAdmissionRequest(pod))

	if !resp.Allowed {
		t.Fatalf("expected allowed, got: %s", resp.Result.Message)
	}

	patched := injectDirect(t, pod, gs)
	agent := findInitContainer(patched)
	if agent == nil {
		t.Fatal("stoker-agent not found")
	}
	assertEnvVar(t, agent, "GIT_SSH_KEY_FILE", mountGitCredentials+"/ssh-privatekey")
	assertVolumeSecret(t, patched, volumeGitCredentials, "ssh-key-secret")
}

func TestInject_TokenAuth(t *testing.T) {
	gs := testGatewaySync()
	injector := newInjector(gs)

	pod := basePod(map[string]string{
		stokertypes.AnnotationInject: "true",
		stokertypes.AnnotationCRName: "my-sync",
	})
	resp := injector.Handle(context.Background(), makeAdmissionRequest(pod))

	if !resp.Allowed {
		t.Fatalf("expected allowed, got: %s", resp.Result.Message)
	}

	patched := injectDirect(t, pod, gs)
	agent := findInitContainer(patched)
	if agent == nil {
		t.Fatal("stoker-agent not found")
	}
	assertEnvVar(t, agent, "GIT_TOKEN_FILE", mountGitCredentials+"/token")
	assertVolumeSecret(t, patched, volumeGitCredentials, "git-token-secret")
}

func TestInject_GitHubAppAuth_TokenSecretVolume(t *testing.T) {
	gs := testGatewaySync()
	gs.Spec.Git.Auth = &stokerv1alpha1.GitAuthSpec{
		GitHubApp: &stokerv1alpha1.GitHubAppAuth{
			AppID:          12345,
			InstallationID: 67890,
			PrivateKeySecretRef: stokerv1alpha1.SecretKeyRef{
				Name: "github-app-pem",
				Key:  "private-key.pem",
			},
		},
	}
	injector := newInjector(gs)

	pod := basePod(map[string]string{
		stokertypes.AnnotationInject:  "true",
		stokertypes.AnnotationCRName:  "my-sync",
		stokertypes.AnnotationProfile: "my-profile",
	})
	resp := injector.Handle(context.Background(), makeAdmissionRequest(pod))

	if !resp.Allowed {
		t.Fatalf("expected allowed, got: %s", resp.Result.Message)
	}

	patched := injectDirect(t, pod, gs)
	agent := findInitContainer(patched)
	if agent == nil {
		t.Fatal("stoker-agent not found")
	}

	// GitHubApp auth: PEM stays in controller — no git-credentials volume or mount.
	for _, v := range patched.Spec.Volumes {
		if v.Name == volumeGitCredentials {
			t.Error("git-credentials volume should not be present for GitHubApp auth")
		}
	}
	for _, vm := range agent.VolumeMounts {
		if vm.Name == volumeGitCredentials {
			t.Error("git-credentials volume mount should not be present for GitHubApp auth")
		}
	}

	// No GIT_SSH_KEY_FILE (SSH is not used for GitHub App auth).
	for _, env := range agent.Env {
		if env.Name == "GIT_SSH_KEY_FILE" {
			t.Errorf("unexpected env var %s for GitHubApp auth", env.Name)
		}
	}

	// GitHub App token Secret IS mounted: GIT_TOKEN_FILE points to controller-managed Secret.
	assertEnvVar(t, agent, "GIT_TOKEN_FILE", mountGitHubToken+"/token")
	assertVolumeSecret(t, patched, volumeGitHubToken, gitHubTokenSecretName("my-sync"))

	// Confirm the volume mount is present in the agent container.
	found := false
	for _, vm := range agent.VolumeMounts {
		if vm.Name == volumeGitHubToken {
			found = true
			if vm.MountPath != mountGitHubToken {
				t.Errorf("git-token mount path: got %q, want %q", vm.MountPath, mountGitHubToken)
			}
		}
	}
	if !found {
		t.Error("git-token volume mount not found in stoker-agent")
	}
}

func TestInject_AgentLabelAndMetricsPort(t *testing.T) {
	gs := testGatewaySync()
	pod := basePod(map[string]string{
		stokertypes.AnnotationInject: "true",
		stokertypes.AnnotationCRName: "my-sync",
	})

	patched := injectDirect(t, pod, gs)

	// Verify stoker.io/agent label is set.
	if patched.Labels[stokertypes.LabelAgent] != "true" {
		t.Errorf("expected label %s=true, got %q", stokertypes.LabelAgent, patched.Labels[stokertypes.LabelAgent])
	}

	// Verify metrics containerPort is present on the agent container.
	agent := findInitContainer(patched)
	if agent == nil {
		t.Fatal("stoker-agent not found")
	}
	found := false
	for _, port := range agent.Ports {
		if port.Name == "metrics" && port.ContainerPort == 8083 {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected metrics port (8083) on stoker-agent container")
	}
}

func TestInject_AgentResourcesFromCR(t *testing.T) {
	gs := testGatewaySync()
	gs.Spec.Agent.Resources = &corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("100m"),
			corev1.ResourceMemory: resource.MustParse("128Mi"),
		},
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("500m"),
			corev1.ResourceMemory: resource.MustParse("512Mi"),
		},
	}
	injector := newInjector(gs)

	pod := basePod(map[string]string{
		stokertypes.AnnotationInject: "true",
		stokertypes.AnnotationCRName: "my-sync",
	})
	resp := injector.Handle(context.Background(), makeAdmissionRequest(pod))

	if !resp.Allowed {
		t.Fatalf("expected allowed, got: %s", resp.Result.Message)
	}

	patched := injectDirect(t, pod, gs)
	agent := findInitContainer(patched)
	if agent == nil {
		t.Fatal("stoker-agent not found")
	}

	cpuReq := agent.Resources.Requests[corev1.ResourceCPU]
	if cpuReq.String() != "100m" {
		t.Fatalf("expected CPU request 100m, got %s", cpuReq.String())
	}
	memLimit := agent.Resources.Limits[corev1.ResourceMemory]
	if memLimit.String() != "512Mi" {
		t.Fatalf("expected memory limit 512Mi, got %s", memLimit.String())
	}
}

func TestInject_SSHAuth_WithKnownHosts(t *testing.T) {
	gs := testGatewaySync()
	gs.Spec.Git.Auth = &stokerv1alpha1.GitAuthSpec{
		SSHKey: &stokerv1alpha1.SSHKeyAuth{
			SecretRef: stokerv1alpha1.SecretKeyRef{
				Name: "ssh-key-secret",
				Key:  "ssh-privatekey",
			},
			KnownHosts: &stokerv1alpha1.KnownHosts{
				SecretRef: stokerv1alpha1.SecretKeyRef{
					Name: "known-hosts-secret",
					Key:  "known_hosts",
				},
			},
		},
	}

	pod := basePod(map[string]string{
		stokertypes.AnnotationInject: "true",
		stokertypes.AnnotationCRName: "my-sync",
	})

	patched := injectDirect(t, pod, gs)
	agent := findInitContainer(patched)
	if agent == nil {
		t.Fatal("stoker-agent not found")
	}

	// Should have GIT_KNOWN_HOSTS_FILE env var.
	assertEnvVar(t, agent, "GIT_KNOWN_HOSTS_FILE", mountKnownHosts+"/known_hosts")

	// Should have the known-hosts volume and mount.
	assertHasVolume(t, patched, volumeKnownHosts)
	assertVolumeSecret(t, patched, volumeKnownHosts, "known-hosts-secret")

	found := false
	for _, vm := range agent.VolumeMounts {
		if vm.Name == volumeKnownHosts {
			found = true
			if vm.MountPath != mountKnownHosts {
				t.Errorf("known-hosts mount path: got %q, want %q", vm.MountPath, mountKnownHosts)
			}
			if !vm.ReadOnly {
				t.Error("known-hosts volume mount should be read-only")
			}
		}
	}
	if !found {
		t.Error("known-hosts volume mount not found in stoker-agent")
	}
}

func TestInject_SSHAuth_WithoutKnownHosts_NoExtraVolume(t *testing.T) {
	gs := testGatewaySync()
	gs.Spec.Git.Auth = &stokerv1alpha1.GitAuthSpec{
		SSHKey: &stokerv1alpha1.SSHKeyAuth{
			SecretRef: stokerv1alpha1.SecretKeyRef{
				Name: "ssh-key-secret",
				Key:  "ssh-privatekey",
			},
			// No KnownHosts
		},
	}

	pod := basePod(map[string]string{
		stokertypes.AnnotationInject: "true",
		stokertypes.AnnotationCRName: "my-sync",
	})

	patched := injectDirect(t, pod, gs)

	// Should NOT have known-hosts volume.
	for _, v := range patched.Spec.Volumes {
		if v.Name == volumeKnownHosts {
			t.Error("known-hosts volume should not be present without KnownHosts config")
		}
	}

	// Should NOT have GIT_KNOWN_HOSTS_FILE env var.
	agent := findInitContainer(patched)
	for _, env := range agent.Env {
		if env.Name == "GIT_KNOWN_HOSTS_FILE" {
			t.Error("GIT_KNOWN_HOSTS_FILE should not be set without KnownHosts config")
		}
	}
}

func TestNeedsKnownHostsVolume(t *testing.T) {
	tests := []struct {
		name string
		gs   *stokerv1alpha1.GatewaySync
		want bool
	}{
		{
			name: "no auth",
			gs:   &stokerv1alpha1.GatewaySync{},
			want: false,
		},
		{
			name: "ssh without known_hosts",
			gs: &stokerv1alpha1.GatewaySync{
				Spec: stokerv1alpha1.GatewaySyncSpec{
					Git: stokerv1alpha1.GitSpec{
						Auth: &stokerv1alpha1.GitAuthSpec{
							SSHKey: &stokerv1alpha1.SSHKeyAuth{
								SecretRef: stokerv1alpha1.SecretKeyRef{Name: "key", Key: "k"},
							},
						},
					},
				},
			},
			want: false,
		},
		{
			name: "ssh with known_hosts",
			gs: &stokerv1alpha1.GatewaySync{
				Spec: stokerv1alpha1.GatewaySyncSpec{
					Git: stokerv1alpha1.GitSpec{
						Auth: &stokerv1alpha1.GitAuthSpec{
							SSHKey: &stokerv1alpha1.SSHKeyAuth{
								SecretRef: stokerv1alpha1.SecretKeyRef{Name: "key", Key: "k"},
								KnownHosts: &stokerv1alpha1.KnownHosts{
									SecretRef: stokerv1alpha1.SecretKeyRef{Name: "kh", Key: "known_hosts"},
								},
							},
						},
					},
				},
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := needsKnownHostsVolume(tt.gs)
			if got != tt.want {
				t.Errorf("needsKnownHostsVolume() = %v, want %v", got, tt.want)
			}
		})
	}
}

// --- Helpers ---

// injectDirect calls injectSidecar on a pod copy with the given CR for testing.
func injectDirect(t *testing.T, pod *corev1.Pod, gs *stokerv1alpha1.GatewaySync) *corev1.Pod {
	t.Helper()
	p := pod.DeepCopy()
	injectSidecar(p, gs)
	return p
}

func assertHasInitContainer(t *testing.T, pod *corev1.Pod, name string) {
	t.Helper()
	for _, c := range pod.Spec.InitContainers {
		if c.Name == name {
			return
		}
	}
	t.Errorf("initContainer %q not found", name)
}

func assertHasVolume(t *testing.T, pod *corev1.Pod, name string) {
	t.Helper()
	for _, v := range pod.Spec.Volumes {
		if v.Name == name {
			return
		}
	}
	t.Errorf("volume %q not found", name)
}

func assertAnnotation(t *testing.T, pod *corev1.Pod, key, value string) {
	t.Helper()
	if pod.Annotations[key] != value {
		t.Errorf("annotation %s: expected %q, got %q", key, value, pod.Annotations[key])
	}
}

func findInitContainer(pod *corev1.Pod) *corev1.Container {
	for i, c := range pod.Spec.InitContainers {
		if c.Name == agentContainerName {
			return &pod.Spec.InitContainers[i]
		}
	}
	return nil
}

func assertEnvVar(t *testing.T, container *corev1.Container, name, expectedValue string) {
	t.Helper()
	for _, env := range container.Env {
		if env.Name == name {
			if env.Value != expectedValue {
				t.Errorf("env %s: expected %q, got %q", name, expectedValue, env.Value)
			}
			return
		}
	}
	t.Errorf("env %s not found", name)
}

func assertVolumeSecret(t *testing.T, pod *corev1.Pod, volumeName, secretName string) {
	t.Helper()
	for _, v := range pod.Spec.Volumes {
		if v.Name == volumeName && v.Secret != nil {
			if v.Secret.SecretName != secretName {
				t.Errorf("volume %s: expected secret %q, got %q", volumeName, secretName, v.Secret.SecretName)
			}
			return
		}
	}
	t.Errorf("volume %s not found or not a secret volume", volumeName)
}

func assertContains(t *testing.T, s, substr string) {
	t.Helper()
	if len(s) == 0 {
		t.Errorf("expected string containing %q, got empty string", substr)
		return
	}
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return
		}
	}
	t.Errorf("expected %q to contain %q", s, substr)
}
