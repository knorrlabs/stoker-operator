package controller

import (
	"context"
	"fmt"
	"slices"
	"sort"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	stokerv1alpha1 "github.com/ia-eknorr/stoker-operator/api/v1alpha1"
	stokertypes "github.com/ia-eknorr/stoker-operator/pkg/types"
)

const (
	// agentClusterRoleName is the fixed name of the agent ClusterRole.
	// This must match the name in config/rbac/agent_role.yaml and the Helm chart template.
	agentClusterRoleName  = "stoker-agent"
	defaultServiceAccount = "default"
	labelManagedBy        = "app.kubernetes.io/managed-by"
)

// agentRoleBindingName returns the name for the auto-created RoleBinding.
func agentRoleBindingName(crName string) string {
	return fmt.Sprintf("stoker-agent-%s", crName)
}

// ensureAgentRoleBinding creates or updates a RoleBinding in the CR's namespace
// that binds the stoker-agent ClusterRole to the ServiceAccounts of discovered gateway pods.
// The RoleBinding is owned by the GatewaySync CR via SetControllerReference for automatic GC.
func (r *GatewaySyncReconciler) ensureAgentRoleBinding(ctx context.Context, gs *stokerv1alpha1.GatewaySync) error {
	log := logf.FromContext(ctx).WithName("auto-rbac")

	// Collect unique ServiceAccount names from ALL pods that reference this CR,
	// regardless of pod phase. This avoids the chicken-and-egg problem where pods
	// are stuck in Init waiting for RBAC but we only grant RBAC to Running pods.
	// A List failure must abort: falling through would rewrite the RoleBinding
	// subjects to the "default" baseline, revoking RBAC from live agents.
	saNames, err := r.collectServiceAccountsFromPods(ctx, gs)
	if err != nil {
		return err
	}
	if len(saNames) == 0 {
		// No matching pods exist yet — use "default" as a baseline subject.
		saNames = []string{defaultServiceAccount}
	}

	rbName := agentRoleBindingName(gs.Name)
	key := types.NamespacedName{Name: rbName, Namespace: gs.Namespace}

	// Build desired subjects.
	desired := buildSubjects(saNames, gs.Namespace)

	// Try to get existing RoleBinding.
	existing := &rbacv1.RoleBinding{}
	err = r.Get(ctx, key, existing)

	if errors.IsNotFound(err) {
		// Create new RoleBinding.
		rb := &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:      rbName,
				Namespace: gs.Namespace,
				Labels: map[string]string{
					labelManagedBy:          "stoker-operator",
					"stoker.io/gatewaysync": gs.Name,
				},
			},
			RoleRef: rbacv1.RoleRef{
				APIGroup: "rbac.authorization.k8s.io",
				Kind:     "ClusterRole",
				Name:     agentClusterRoleName,
			},
			Subjects: desired,
		}

		if err := controllerutil.SetControllerReference(gs, rb, r.Scheme); err != nil {
			return fmt.Errorf("setting owner reference on RoleBinding: %w", err)
		}

		if err := r.Create(ctx, rb); err != nil {
			return fmt.Errorf("creating agent RoleBinding: %w", err)
		}

		log.Info("created agent RoleBinding", "name", rbName, "subjects", formatSANames(saNames))
		return nil
	}
	if err != nil {
		return fmt.Errorf("getting agent RoleBinding: %w", err)
	}

	// RoleBinding exists. Check if roleRef matches (immutable field).
	if existing.RoleRef.Name != agentClusterRoleName {
		// roleRef is immutable — must delete and recreate.
		log.Info("agent RoleBinding has wrong roleRef, recreating", "current", existing.RoleRef.Name, "expected", agentClusterRoleName)
		if err := r.Delete(ctx, existing); err != nil && !errors.IsNotFound(err) {
			return fmt.Errorf("deleting mismatched RoleBinding: %w", err)
		}
		// Will be recreated on next reconcile.
		return nil
	}

	// Update subjects if they changed.
	if !subjectsEqual(existing.Subjects, desired) {
		existing.Subjects = desired
		if err := r.Update(ctx, existing); err != nil {
			return fmt.Errorf("updating agent RoleBinding subjects: %w", err)
		}
		log.Info("updated agent RoleBinding subjects", "name", rbName, "subjects", formatSANames(saNames))
	}

	return nil
}

// collectServiceAccountsFromPods lists ALL pods in the namespace that reference this CR
// (via stoker.io/cr-name annotation) regardless of phase, and returns their unique SA names.
// This is critical: pods may be in Init/Pending state waiting for RBAC before they can reach
// Running phase, so we must grant RBAC based on ALL matching pods, not just Running ones.
func (r *GatewaySyncReconciler) collectServiceAccountsFromPods(ctx context.Context, gs *stokerv1alpha1.GatewaySync) ([]string, error) {
	var podList corev1.PodList
	if err := r.List(ctx, &podList, client.InNamespace(gs.Namespace)); err != nil {
		return nil, fmt.Errorf("listing pods for RBAC subjects: %w", err)
	}

	seen := make(map[string]bool)
	for _, pod := range podList.Items {
		crName, ok := pod.Annotations[stokertypes.AnnotationCRName]
		if !ok || crName != gs.Name {
			continue
		}
		sa := pod.Spec.ServiceAccountName
		if sa == "" {
			sa = defaultServiceAccount
		}
		seen[sa] = true
	}

	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

// buildSubjects creates RoleBinding subjects for the given ServiceAccount names.
func buildSubjects(saNames []string, namespace string) []rbacv1.Subject {
	subjects := make([]rbacv1.Subject, len(saNames))
	for i, name := range saNames {
		subjects[i] = rbacv1.Subject{
			Kind:      "ServiceAccount",
			Name:      name,
			Namespace: namespace,
		}
	}
	return subjects
}

// subjectsEqual compares two subject lists (order-independent).
func subjectsEqual(a, b []rbacv1.Subject) bool {
	if len(a) != len(b) {
		return false
	}
	aNames := make([]string, len(a))
	bNames := make([]string, len(b))
	for i := range a {
		aNames[i] = a[i].Name
		bNames[i] = b[i].Name
	}
	sort.Strings(aNames)
	sort.Strings(bNames)
	return slices.Equal(aNames, bNames)
}

// formatSANames formats SA names for log output.
func formatSANames(names []string) string {
	if len(names) == 0 {
		return "(none)"
	}
	return fmt.Sprintf("%v", names)
}
