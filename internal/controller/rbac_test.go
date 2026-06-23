package controller

import (
	"context"
	"fmt"
	"testing"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	stokerv1alpha1 "github.com/knorrlabs/stoker-operator/api/v1alpha1"
)

// TestEnsureAgentRoleBindingPreservesSubjectsOnListError guards against a
// regression where a transient pod List failure was swallowed, causing the
// RoleBinding subjects to be rewritten to the "default" baseline and revoking
// RBAC from live agents mid-flight.
func TestEnsureAgentRoleBindingPreservesSubjectsOnListError(t *testing.T) {
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatalf("adding core scheme: %v", err)
	}
	if err := stokerv1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("adding stoker scheme: %v", err)
	}

	gs := &stokerv1alpha1.GatewaySync{
		ObjectMeta: metav1.ObjectMeta{Name: "test-cr", Namespace: "test-ns"},
	}
	existing := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: agentRoleBindingName(gs.Name), Namespace: gs.Namespace},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     agentClusterRoleName,
		},
		Subjects: []rbacv1.Subject{
			{Kind: "ServiceAccount", Name: "gateway-sa", Namespace: gs.Namespace},
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(gs, existing).
		WithInterceptorFuncs(interceptor.Funcs{
			List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				if _, ok := list.(*corev1.PodList); ok {
					return fmt.Errorf("transient apiserver error")
				}
				return cl.List(ctx, list, opts...)
			},
		}).
		Build()

	r := &GatewaySyncReconciler{Client: c, Scheme: s}

	if err := r.ensureAgentRoleBinding(context.Background(), gs); err == nil {
		t.Fatal("expected error when pod List fails, got nil")
	}

	var rb rbacv1.RoleBinding
	key := types.NamespacedName{Name: agentRoleBindingName(gs.Name), Namespace: gs.Namespace}
	if err := c.Get(context.Background(), key, &rb); err != nil {
		t.Fatalf("getting RoleBinding: %v", err)
	}
	if len(rb.Subjects) != 1 || rb.Subjects[0].Name != "gateway-sa" {
		t.Errorf("RoleBinding subjects were modified despite List error: %+v", rb.Subjects)
	}
}
