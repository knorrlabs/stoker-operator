package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	stokerv1alpha1 "github.com/ia-eknorr/stoker-operator/api/v1alpha1"
	"github.com/ia-eknorr/stoker-operator/pkg/conditions"
	stokertypes "github.com/ia-eknorr/stoker-operator/pkg/types"
)

// findGatewaySyncForPod reads the stoker.io/cr-name annotation from a pod
// and returns a reconcile.Request for the matching GatewaySync CR in the same namespace.
// Returns nil if the annotation is not present.
func (r *GatewaySyncReconciler) findGatewaySyncForPod(ctx context.Context, pod client.Object) []reconcile.Request {
	crName, ok := pod.GetAnnotations()[stokertypes.AnnotationCRName]
	if !ok || crName == "" {
		return nil
	}

	return []reconcile.Request{
		{
			NamespacedName: types.NamespacedName{
				Name:      crName,
				Namespace: pod.GetNamespace(),
			},
		},
	}
}

// discoverGateways lists all pods in the CR's namespace with annotation stoker.io/cr-name
// matching gs.Name. For each matching pod in Running phase, it builds a DiscoveredGateway.
func (r *GatewaySyncReconciler) discoverGateways(ctx context.Context, gs *stokerv1alpha1.GatewaySync) ([]stokerv1alpha1.DiscoveredGateway, error) {
	log := logf.FromContext(ctx)

	// List all pods in the namespace
	var podList corev1.PodList
	if err := r.List(ctx, &podList, client.InNamespace(gs.Namespace)); err != nil {
		return nil, fmt.Errorf("listing pods: %w", err)
	}

	discovered := make([]stokerv1alpha1.DiscoveredGateway, 0, len(podList.Items))

	// Previous status by pod name, to emit MissingSidecar only on transition
	// instead of on every reconcile (polling reconciles every 60s by default).
	prevStatus := make(map[string]string, len(gs.Status.DiscoveredGateways))
	for _, gw := range gs.Status.DiscoveredGateways {
		prevStatus[gw.PodName] = gw.SyncStatus
	}

	for _, pod := range podList.Items {
		// Filter by annotation
		crName, ok := pod.Annotations[stokertypes.AnnotationCRName]
		if !ok || crName != gs.Name {
			continue
		}

		// Only include Running pods
		if pod.Status.Phase != corev1.PodRunning {
			continue
		}

		// Determine gateway name
		gatewayName := pod.Name
		if nameFromAnnotation, ok := pod.Annotations[stokertypes.AnnotationGatewayName]; ok && nameFromAnnotation != "" {
			gatewayName = nameFromAnnotation
		} else if nameFromLabel, ok := pod.Labels["app.kubernetes.io/name"]; ok && nameFromLabel != "" {
			gatewayName = nameFromLabel
		}

		// Get profile from annotation
		profile := pod.Annotations[stokertypes.AnnotationProfile]

		// Detect missing sidecar: pod has inject annotation but no stoker-agent container
		syncStatus := stokertypes.SyncStatusPending
		if pod.Annotations[stokertypes.AnnotationInject] == "true" && !hasSyncAgent(&pod) {
			syncStatus = stokertypes.SyncStatusMissingSidecar
			if prevStatus[pod.Name] != stokertypes.SyncStatusMissingSidecar {
				r.Recorder.Eventf(gs, corev1.EventTypeWarning, "MissingSidecar",
					"Pod %s has inject annotation but no stoker-agent sidecar — webhook may have been unavailable during pod creation. Delete and recreate the pod.", pod.Name)
			}
		}

		// Capture ServiceAccount for auto-RBAC binding.
		saName := pod.Spec.ServiceAccountName
		if saName == "" {
			saName = defaultServiceAccount
		}

		gateway := stokerv1alpha1.DiscoveredGateway{
			Name:               gatewayName,
			Namespace:          pod.Namespace,
			PodName:            pod.Name,
			ServiceAccountName: saName,
			Profile:            profile,
			SyncStatus:         syncStatus,
		}

		discovered = append(discovered, gateway)
	}

	log.V(1).Info("discovered gateways", "count", len(discovered))
	return discovered, nil
}

// hasSyncAgent checks if a pod has the stoker-agent sidecar container.
func hasSyncAgent(pod *corev1.Pod) bool {
	for _, c := range pod.Spec.InitContainers {
		if c.Name == "stoker-agent" {
			return true
		}
	}
	return false
}

// collectGatewayStatus reads the ConfigMap stoker-status-{gs.Name} in gs.Namespace
// and enriches each gateway with its sync status data. If the ConfigMap doesn't exist or a
// gateway's status key is missing, the gateway remains with SyncStatus="Pending".
func (r *GatewaySyncReconciler) collectGatewayStatus(ctx context.Context, gs *stokerv1alpha1.GatewaySync, gateways []stokerv1alpha1.DiscoveredGateway) []stokerv1alpha1.DiscoveredGateway {
	log := logf.FromContext(ctx)

	cmName := fmt.Sprintf("stoker-status-%s", gs.Name)
	cm := &corev1.ConfigMap{}
	key := types.NamespacedName{Name: cmName, Namespace: gs.Namespace}

	if err := r.Get(ctx, key, cm); err != nil {
		if errors.IsNotFound(err) {
			log.V(1).Info("status ConfigMap not found, gateways remain Pending", "configmap", cmName)
		} else {
			log.Error(err, "failed to get status ConfigMap", "configmap", cmName)
		}
		return gateways
	}

	// Enrich each gateway with its status.
	// The agent writes status keyed by its GATEWAY_NAME env, which defaults to the
	// pod name when unset. Look up by PodName first, then fall back to Name.
	for i := range gateways {
		statusJSON, ok := cm.Data[gateways[i].PodName]
		if !ok {
			statusJSON, ok = cm.Data[gateways[i].Name]
		}
		if !ok || statusJSON == "" {
			continue
		}

		var status stokertypes.GatewayStatus
		if err := json.Unmarshal([]byte(statusJSON), &status); err != nil {
			log.Error(err, "failed to unmarshal gateway status", "gateway", gateways[i].Name)
			continue
		}

		// Map status fields onto DiscoveredGateway
		gateways[i].SyncStatus = status.SyncStatus
		gateways[i].SyncedCommit = status.SyncedCommit
		gateways[i].SyncedRef = status.SyncedRef
		gateways[i].LastSyncDuration = status.LastSyncDuration
		gateways[i].AgentVersion = status.AgentVersion
		gateways[i].LastScanResult = status.LastScanResult
		gateways[i].FilesChanged = status.FilesChanged
		gateways[i].ProjectsSynced = status.ProjectsSynced

		// Parse lastSyncTime as RFC3339
		if status.LastSyncTime != "" {
			t, err := time.Parse(time.RFC3339, status.LastSyncTime)
			if err != nil {
				log.Error(err, "failed to parse lastSyncTime", "gateway", gateways[i].Name, "time", status.LastSyncTime)
			} else {
				mt := metav1.NewTime(t)
				gateways[i].LastSyncTime = &mt
			}
		}
	}

	return gateways
}

// updateAllGatewaysSyncedCondition counts how many gateways are synced and sets
// the AllGatewaysSynced condition accordingly.
func (r *GatewaySyncReconciler) updateAllGatewaysSyncedCondition(ctx context.Context, gs *stokerv1alpha1.GatewaySync) {
	totalGateways := len(gs.Status.DiscoveredGateways)

	if totalGateways == 0 {
		r.setCondition(ctx, gs, conditions.TypeAllGatewaysSynced, metav1.ConditionFalse,
			conditions.ReasonNoGateways, "0/0 synced")
		return
	}

	syncedCount := 0
	missingSidecarCount := 0
	for _, gw := range gs.Status.DiscoveredGateways {
		if gw.SyncStatus == stokertypes.SyncStatusSynced {
			syncedCount++
		}
		if gw.SyncStatus == stokertypes.SyncStatusMissingSidecar {
			missingSidecarCount++
		}
	}

	if syncedCount == totalGateways {
		message := fmt.Sprintf("%d/%d synced", syncedCount, totalGateways)
		r.setCondition(ctx, gs, conditions.TypeAllGatewaysSynced, metav1.ConditionTrue,
			conditions.ReasonSyncSucceeded, message)
	} else {
		message := fmt.Sprintf("%d/%d synced", syncedCount, totalGateways)
		if missingSidecarCount > 0 {
			message = fmt.Sprintf("%d/%d synced (%d missing sidecar)", syncedCount, totalGateways, missingSidecarCount)
		}
		r.setCondition(ctx, gs, conditions.TypeAllGatewaysSynced, metav1.ConditionFalse,
			conditions.ReasonSyncInProgress, message)
	}

	// Update SidecarInjected condition
	if missingSidecarCount > 0 {
		r.setCondition(ctx, gs, conditions.TypeSidecarInjected, metav1.ConditionFalse,
			conditions.ReasonSidecarMissing, fmt.Sprintf("%d gateway(s) missing stoker-agent sidecar", missingSidecarCount))
	} else {
		r.setCondition(ctx, gs, conditions.TypeSidecarInjected, metav1.ConditionTrue,
			conditions.ReasonSidecarPresent, "All gateways have stoker-agent sidecar")
	}
}

// updateReadyCondition sets the Ready condition based on RefResolved, ProfilesValid, and AllGatewaysSynced.
// Ready=True only when all three are True.
func (r *GatewaySyncReconciler) updateReadyCondition(ctx context.Context, gs *stokerv1alpha1.GatewaySync) {
	refResolved := false
	allGatewaysSynced := false
	profilesValid := false

	for _, cond := range gs.Status.Conditions {
		if cond.Type == conditions.TypeRefResolved && cond.Status == metav1.ConditionTrue {
			refResolved = true
		}
		if cond.Type == conditions.TypeAllGatewaysSynced && cond.Status == metav1.ConditionTrue {
			allGatewaysSynced = true
		}
		if cond.Type == conditions.TypeProfilesValid && cond.Status == metav1.ConditionTrue {
			profilesValid = true
		}
	}

	if refResolved && allGatewaysSynced && profilesValid {
		r.setCondition(ctx, gs, conditions.TypeReady, metav1.ConditionTrue,
			conditions.ReasonSyncSucceeded, "All gateways synced")
	} else if !profilesValid {
		r.setCondition(ctx, gs, conditions.TypeReady, metav1.ConditionFalse,
			conditions.ReasonReconciling, "Profiles invalid")
	} else if !refResolved {
		r.setCondition(ctx, gs, conditions.TypeReady, metav1.ConditionFalse,
			conditions.ReasonReconciling, "Ref not resolved")
	} else {
		r.setCondition(ctx, gs, conditions.TypeReady, metav1.ConditionFalse,
			conditions.ReasonReconciling, "Waiting for gateways to sync")
	}
}
