package types

const (
	// AnnotationPrefix is the base prefix for all stoker annotations.
	AnnotationPrefix = "stoker.io"

	// Pod annotations — set by users on gateway pods to trigger sidecar injection.

	// AnnotationInject enables sidecar injection when set to "true".
	AnnotationInject = AnnotationPrefix + "/inject"

	// AnnotationCRName identifies which GatewaySync CR in this namespace to use.
	// Auto-derived if exactly one CR exists in the namespace.
	AnnotationCRName = AnnotationPrefix + "/cr-name"

	// AnnotationGatewayName overrides gateway identity (defaults to pod label app.kubernetes.io/name).
	AnnotationGatewayName = AnnotationPrefix + "/gateway-name"

	// AnnotationProfile names the sync profile for this gateway pod.
	// If unset, the "default" profile is used.
	AnnotationProfile = AnnotationPrefix + "/profile"

	// AnnotationRefOverride overrides the git ref for this pod only.
	// Read by the agent sidecar, NOT the controller. The agent resolves
	// the ref independently via ls-remote and syncs to that commit instead
	// of the metadata ConfigMap's ref. The controller detects the skew
	// (syncedRef != lastSyncRef) and sets a RefSkew warning condition.
	// Intended for dev/test gateways in production namespaces.
	AnnotationRefOverride = AnnotationPrefix + "/ref-override"

	// CR annotations — set by the webhook receiver on the GatewaySync CR (not by users).

	// AnnotationRequestedRef is set by the webhook receiver to request a ref update.
	// The controller reads this and initiates a sync to the requested ref.
	AnnotationRequestedRef = AnnotationPrefix + "/requested-ref"

	// AnnotationRequestedAt records when the webhook request was received.
	AnnotationRequestedAt = AnnotationPrefix + "/requested-at"

	// AnnotationRequestedBy records the source of the webhook request (e.g., "argocd", "kargo", "github").
	AnnotationRequestedBy = AnnotationPrefix + "/requested-by"

	// Webhook injection annotations — set by the webhook on injected pods.

	// AnnotationInjected is set by the webhook after successful injection for tracking.
	AnnotationInjected = AnnotationPrefix + "/injected"

	// Labels

	// LabelCRName is used on owned resources (PVCs, ConfigMaps, Secrets) to identify the parent CR.
	LabelCRName = AnnotationPrefix + "/cr-name"

	// AnnotationSecretType annotates controller-managed Secrets with their purpose.
	AnnotationSecretType = AnnotationPrefix + "/secret-type"

	// LabelAgent is set on pods with an injected stoker-agent sidecar.
	// Used by PodMonitor for metrics scrape discovery (labels are indexed, annotations are not).
	LabelAgent = AnnotationPrefix + "/agent"

	// LabelNamespaceInjection enables webhook injection for a namespace via namespaceSelector.
	// Applied to namespaces: kubectl label namespace site1 stoker.io/injection=enabled
	LabelNamespaceInjection = AnnotationPrefix + "/injection"

	// Finalizer

	// Finalizer is added to GatewaySync CRs to ensure cleanup on deletion.
	Finalizer = AnnotationPrefix + "/finalizer"

	// Sync status values for missing sidecar detection.

	// SyncStatusMissingSidecar indicates the pod has the inject annotation but no agent container.
	SyncStatusMissingSidecar = "MissingSidecar"
)
