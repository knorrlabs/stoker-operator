package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	"github.com/go-git/go-git/v5/plumbing/transport"
	gogithttp "github.com/go-git/go-git/v5/plumbing/transport/http"

	stokerv1alpha1 "github.com/ia-eknorr/stoker-operator/api/v1alpha1"
	"github.com/ia-eknorr/stoker-operator/internal/git"
	"github.com/ia-eknorr/stoker-operator/pkg/conditions"
	stokertypes "github.com/ia-eknorr/stoker-operator/pkg/types"
)

const (
	lsRemoteTimeout = 30 * time.Second

	// tokenRefreshBuffer is how long before expiry the controller refreshes a GitHub App token.
	tokenRefreshBuffer = 5 * time.Minute
)

// cachedToken holds a GitHub App installation token and its expiry time.
type cachedToken struct {
	token  string
	expiry time.Time
}

// backoffState tracks consecutive failures for exponential backoff.
type backoffState struct {
	failureCount int
	lastFailure  time.Time
}

// GatewaySyncReconciler reconciles a GatewaySync object.
type GatewaySyncReconciler struct {
	client.Client
	Scheme            *runtime.Scheme
	GitClient         git.Client
	Recorder          record.EventRecorder
	AutoBindAgentRBAC bool

	// mu guards tokenCache and backoff. Reconcile runs with
	// MaxConcurrentReconciles > 1, so reconciles for different CRs
	// mutate these maps concurrently.
	mu sync.Mutex

	// tokenCache holds GitHub App installation tokens keyed by "appID:installationID".
	tokenCache map[string]cachedToken

	// backoff tracks consecutive failures per CR for exponential backoff.
	backoff map[types.NamespacedName]*backoffState
}

// +kubebuilder:rbac:groups=stoker.io,resources=gatewaysyncs,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=stoker.io,resources=gatewaysyncs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=stoker.io,resources=gatewaysyncs/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=rolebindings,verbs=get;list;create;update;delete;watch
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterroles,verbs=bind,resourceNames=stoker-agent

func (r *GatewaySyncReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	reconcileStart := time.Now()
	reconcileResult := resultSuccess

	// Observe reconcile duration and increment counter on return.
	defer func() {
		reconcileDuration.WithLabelValues(req.Name, req.Namespace).Observe(time.Since(reconcileStart).Seconds())
		reconcileTotal.WithLabelValues(req.Name, req.Namespace, reconcileResult).Inc()
	}()

	// Fetch the CR — NotFound is expected after finalizer cleanup race
	var gs stokerv1alpha1.GatewaySync
	if err := r.Get(ctx, req.NamespacedName, &gs); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		reconcileResult = resultError
		return ctrl.Result{}, err
	}

	// Capture the original for merge-patch base (avoids resourceVersion conflicts).
	base := gs.DeepCopy()

	// Reset backoff on webhook-triggered reconcile so it isn't delayed by previous failures.
	if gs.Annotations[stokertypes.AnnotationRequestedRef] != "" {
		r.resetBackoff(req.NamespacedName)
	}

	// --- Step 0: Finalizer handling ---

	if !gs.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(&gs, stokertypes.Finalizer) {
			log.Info("cleaning up resources for deleted CR")
			if err := r.cleanupOwnedResources(ctx, &gs); err != nil {
				return ctrl.Result{}, err
			}
			r.resetBackoff(req.NamespacedName)
			controllerutil.RemoveFinalizer(&gs, stokertypes.Finalizer)
			return ctrl.Result{}, r.Update(ctx, &gs)
		}
		return ctrl.Result{}, nil
	}

	if !controllerutil.ContainsFinalizer(&gs, stokertypes.Finalizer) {
		controllerutil.AddFinalizer(&gs, stokertypes.Finalizer)
		return ctrl.Result{}, r.Update(ctx, &gs)
	}

	// --- Step 0.5: Check if paused ---

	if gs.Spec.Paused {
		crPaused.WithLabelValues(gs.Name, gs.Namespace).Set(1)
		wasPaused := conditionHasReason(gs.Status.Conditions, conditions.TypeReady, conditions.ReasonPaused)
		r.setCondition(ctx, &gs, conditions.TypeReady, metav1.ConditionFalse, conditions.ReasonPaused, "Reconciliation paused")
		if !wasPaused {
			log.Info("CR is paused, skipping reconciliation")
			r.Recorder.Event(&gs, corev1.EventTypeNormal, conditions.ReasonPaused, "Reconciliation paused")
		}
		return ctrl.Result{}, r.patchStatus(ctx, &gs, base)
	}
	crPaused.WithLabelValues(gs.Name, gs.Namespace).Set(0)

	// --- Step 1: Validate profiles ---

	if err := r.validateProfiles(&gs); err != nil {
		r.setCondition(ctx, &gs, conditions.TypeProfilesValid, metav1.ConditionFalse, conditions.ReasonProfilesInvalid, err.Error())
		r.Recorder.Eventf(&gs, corev1.EventTypeWarning, conditions.ReasonProfilesInvalid, "Profile validation failed: %s", err.Error())
	} else {
		r.setCondition(ctx, &gs, conditions.TypeProfilesValid, metav1.ConditionTrue, conditions.ReasonProfilesValid, "All profiles valid")
	}

	// --- Step 2: Validate git auth secrets exist ---

	if err := r.validateGitSecrets(ctx, &gs); err != nil {
		r.recordFailure(req.NamespacedName)
		delay := r.backoffDelay(req.NamespacedName)
		r.setCondition(ctx, &gs, conditions.TypeReady, metav1.ConditionFalse, conditions.ReasonReconciling,
			fmt.Sprintf("%s (retry in %s)", err.Error(), delay.Round(time.Second)))
		_ = r.patchStatus(ctx, &gs, base)
		reconcileResult = resultRequeue
		return ctrl.Result{RequeueAfter: delay}, nil
	}

	// --- Step 2.5: SSH host key verification warning ---

	if gs.Spec.Git.Auth != nil && gs.Spec.Git.Auth.SSHKey != nil {
		if gs.Spec.Git.Auth.SSHKey.KnownHosts == nil {
			r.setCondition(ctx, &gs, conditions.TypeSSHHostKeyVerification, metav1.ConditionFalse,
				conditions.ReasonHostKeyVerificationDisabled,
				"SSH host key verification disabled — set spec.git.auth.sshKey.knownHosts to enable MITM protection")
		} else {
			r.setCondition(ctx, &gs, conditions.TypeSSHHostKeyVerification, metav1.ConditionTrue,
				conditions.ReasonHostKeyVerificationEnabled, "SSH host key verification enabled")
		}
	}

	// --- Step 3: Resolve git ref via ls-remote ---

	refStart := time.Now()
	result, err := r.resolveRef(ctx, &gs)
	refResolveDuration.WithLabelValues(gs.Name, gs.Namespace).Observe(time.Since(refStart).Seconds())

	if err != nil {
		r.recordFailure(req.NamespacedName)
		delay := r.backoffDelay(req.NamespacedName)
		reason := conditions.ReasonRefResolutionFailed
		if gs.Spec.Git.Auth != nil && gs.Spec.Git.Auth.GitHubApp != nil && strings.Contains(err.Error(), "GitHub App") {
			reason = conditions.ReasonGitHubAppExchangeFailed
		}
		wasAlreadyFailed := conditionHasStatus(gs.Status.Conditions, conditions.TypeRefResolved, metav1.ConditionFalse)
		r.setCondition(ctx, &gs, conditions.TypeRefResolved, metav1.ConditionFalse, reason,
			fmt.Sprintf("Ref resolution failed (retry in %s): %s", delay.Round(time.Second), err.Error()))
		if !wasAlreadyFailed {
			r.Recorder.Eventf(&gs, corev1.EventTypeWarning, reason, "Ref resolution failed: %s", err.Error())
		}
		gs.Status.RefResolutionStatus = "Error"
		_ = r.patchStatus(ctx, &gs, base)
		reconcileResult = resultRequeue
		return ctrl.Result{RequeueAfter: delay}, nil
	}

	// Ref resolved successfully
	r.resetBackoff(req.NamespacedName)
	r.setCondition(ctx, &gs, conditions.TypeRefResolved, metav1.ConditionTrue, conditions.ReasonRefResolved, result.Commit)
	gs.Status.RefResolutionStatus = "Resolved"
	if gs.Status.LastSyncCommit != result.Commit {
		gs.Status.LastSyncCommit = result.Commit
		gs.Status.LastSyncCommitShort = shortCommit(result.Commit)
		gs.Status.LastSyncRef = result.Ref
		now := metav1.Now()
		gs.Status.LastSyncTime = &now
	}

	// --- Step 3.5: Validate gateway API key secret ---

	if err := r.validateAPIKeySecret(ctx, &gs); err != nil {
		r.recordFailure(req.NamespacedName)
		delay := r.backoffDelay(req.NamespacedName)
		r.setCondition(ctx, &gs, conditions.TypeReady, metav1.ConditionFalse, conditions.ReasonReconciling,
			fmt.Sprintf("%s (retry in %s)", err.Error(), delay.Round(time.Second)))
		_ = r.patchStatus(ctx, &gs, base)
		reconcileResult = resultRequeue
		return ctrl.Result{RequeueAfter: delay}, nil
	}

	// --- Step 4: Create/update metadata ConfigMap ---

	if err := r.ensureMetadataConfigMap(ctx, &gs, result); err != nil {
		log.Error(err, "failed to update metadata ConfigMap")
	}

	// --- Step 5: Discover gateways ---

	prevGatewayCount := len(gs.Status.DiscoveredGateways)
	gateways, err := r.discoverGateways(ctx, &gs)
	if err != nil {
		log.Error(err, "failed to discover gateways")
	} else {
		gateways = r.collectGatewayStatus(ctx, &gs, gateways)
		gs.Status.DiscoveredGateways = gateways

		if len(gateways) != prevGatewayCount {
			r.Recorder.Eventf(&gs, corev1.EventTypeNormal, "GatewaysDiscovered",
				"Discovered %d gateway(s) (was %d)", len(gateways), prevGatewayCount)
		}
	}

	// --- Step 5.5: Auto-RBAC ---

	if r.AutoBindAgentRBAC {
		if err := r.ensureAgentRoleBinding(ctx, &gs); err != nil {
			log.Error(err, "failed to ensure agent RoleBinding")
			r.Recorder.Eventf(&gs, corev1.EventTypeWarning, "RBACError", "Failed to create agent RoleBinding: %v", err)
		}
	}

	// --- Step 6: Update conditions ---

	r.updateAllGatewaysSyncedCondition(ctx, &gs)
	r.updateReadyCondition(ctx, &gs)

	// --- Step 6.5: Update metrics ---

	observeGatewayMetrics(&gs)

	// --- Step 7: Update status ---

	gs.Status.ObservedGeneration = gs.Generation
	gs.Status.ProfileCount = int32(len(gs.Spec.Sync.Profiles))
	if err := r.patchStatus(ctx, &gs, base); err != nil {
		reconcileResult = resultError
		return ctrl.Result{}, err
	}

	// --- Step 7.5: Clear stale requested-ref annotation ---
	r.clearRequestedRefIfCaughtUp(ctx, req, &gs)

	// --- Step 8: Requeue ---

	requeueAfter := r.requeueInterval(&gs)

	reconcileLog := log.V(1)
	if base.Status.LastSyncCommit != gs.Status.LastSyncCommit || len(base.Status.DiscoveredGateways) != len(gs.Status.DiscoveredGateways) {
		reconcileLog = log
	}
	reconcileLog.Info("reconciliation complete", "commit", result.Commit, "gateways", len(gs.Status.DiscoveredGateways), "requeueAfter", requeueAfter)
	return ctrl.Result{RequeueAfter: requeueAfter}, nil
}

// backoffDelay returns the requeue delay based on consecutive failures.
// Sequence: 30s, 60s, 120s, 240s, 300s (capped).
func (r *GatewaySyncReconciler) backoffDelay(key types.NamespacedName) time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()

	state, ok := r.backoff[key]
	if !ok || state.failureCount == 0 {
		return 30 * time.Second
	}
	delay := 30 * time.Second
	for i := 0; i < state.failureCount-1 && delay < 5*time.Minute; i++ {
		delay *= 2
	}
	if delay > 5*time.Minute {
		delay = 5 * time.Minute
	}
	return delay
}

func (r *GatewaySyncReconciler) recordFailure(key types.NamespacedName) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.backoff == nil {
		r.backoff = make(map[types.NamespacedName]*backoffState)
	}
	state, ok := r.backoff[key]
	if !ok {
		state = &backoffState{}
		r.backoff[key] = state
	}
	state.failureCount++
	state.lastFailure = time.Now()
}

func (r *GatewaySyncReconciler) resetBackoff(key types.NamespacedName) {
	r.mu.Lock()
	defer r.mu.Unlock()

	delete(r.backoff, key)
}

// cachedGitHubToken returns the cached GitHub App token for a cache key, if present.
func (r *GatewaySyncReconciler) cachedGitHubToken(cacheKey string) (cachedToken, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	cached, ok := r.tokenCache[cacheKey]
	return cached, ok
}

// storeGitHubToken caches a GitHub App installation token.
func (r *GatewaySyncReconciler) storeGitHubToken(cacheKey string, token cachedToken) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.tokenCache == nil {
		r.tokenCache = make(map[string]cachedToken)
	}
	r.tokenCache[cacheKey] = token
}

// validVarKey matches Go identifiers: letters, digits, underscores; must start with letter or underscore.
var validVarKey = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// validateProfiles validates all embedded profiles for path safety and var key naming.
func (r *GatewaySyncReconciler) validateProfiles(gs *stokerv1alpha1.GatewaySync) error {
	if err := validateVarKeys(gs.Spec.Sync.Defaults.Vars, "sync.defaults.vars"); err != nil {
		return err
	}
	for name, profile := range gs.Spec.Sync.Profiles {
		if err := validateVarKeys(profile.Vars, fmt.Sprintf("profiles[%s].vars", name)); err != nil {
			return err
		}
		for i, m := range profile.Mappings {
			if err := validatePath(m.Source, fmt.Sprintf("profiles[%s].mappings[%d].source", name, i)); err != nil {
				return err
			}
			if err := validatePath(m.Destination, fmt.Sprintf("profiles[%s].mappings[%d].destination", name, i)); err != nil {
				return err
			}
		}
	}
	return nil
}

// validateVarKeys rejects var keys that are not valid Go identifiers. Keys with
// dashes, dots, or slashes cannot be accessed via {{.Vars.key}} in templates.
func validateVarKeys(vars map[string]string, field string) error {
	for k := range vars {
		if !validVarKey.MatchString(k) {
			return fmt.Errorf("%s: key %q is not a valid identifier (use letters, digits, underscores only — dashes are not supported in template variable names)", field, k)
		}
	}
	return nil
}

// validatePath rejects absolute paths and path traversal.
func validatePath(p, field string) error {
	if filepath.IsAbs(p) {
		return fmt.Errorf("%s: absolute paths not allowed (%q)", field, p)
	}
	if containsTraversal(p) {
		return fmt.Errorf("%s: path traversal (..) not allowed (%q)", field, p)
	}
	return nil
}

// containsTraversal checks for ".." path components.
func containsTraversal(p string) bool {
	return slices.Contains(strings.Split(filepath.ToSlash(p), "/"), "..")
}

// resolveProfiles merges defaults into each profile, returning fully-resolved profiles.
func (r *GatewaySyncReconciler) resolveProfiles(gs *stokerv1alpha1.GatewaySync) map[string]stokertypes.ResolvedProfile {
	defaults := gs.Spec.Sync.Defaults
	resolved := make(map[string]stokertypes.ResolvedProfile, len(gs.Spec.Sync.Profiles))

	for name, p := range gs.Spec.Sync.Profiles {
		rp := stokertypes.ResolvedProfile{}

		// Merge vars: defaults first, then profile overrides per-key.
		merged := make(map[string]string, len(defaults.Vars)+len(p.Vars))
		maps.Copy(merged, defaults.Vars)
		maps.Copy(merged, p.Vars)
		if len(merged) > 0 {
			rp.Vars = merged
		}

		// Resolve mappings — Type is intentionally passed through as-is (empty means
		// infer from filesystem at sync time; non-empty is validated by the agent).
		rp.Mappings = make([]stokertypes.ResolvedMapping, len(p.Mappings))
		for i, m := range p.Mappings {
			patches := make([]stokertypes.ResolvedPatch, len(m.Patches))
			for j, p := range m.Patches {
				patches[j] = stokertypes.ResolvedPatch{
					File: p.File,
					Set:  p.Set,
				}
			}
			rp.Mappings[i] = stokertypes.ResolvedMapping{
				Source:      m.Source,
				Destination: m.Destination,
				Type:        m.Type,
				Required:    m.Required,
				Template:    m.Template,
				Patches:     patches,
			}
		}

		// Merge excludes: defaults + profile-specific
		rp.ExcludePatterns = append([]string{}, defaults.ExcludePatterns...)
		rp.ExcludePatterns = append(rp.ExcludePatterns, p.ExcludePatterns...)

		// Apply overrides with nil-means-inherit
		rp.SyncPeriod = defaults.SyncPeriod
		if p.SyncPeriod != nil {
			rp.SyncPeriod = *p.SyncPeriod
		}
		if rp.SyncPeriod == 0 {
			rp.SyncPeriod = 30
		}

		rp.DryRun = defaults.DryRun
		if p.DryRun != nil {
			rp.DryRun = *p.DryRun
		}

		rp.DesignerSessionPolicy = defaults.DesignerSessionPolicy
		if p.DesignerSessionPolicy != "" {
			rp.DesignerSessionPolicy = p.DesignerSessionPolicy
		}
		if rp.DesignerSessionPolicy == "" {
			rp.DesignerSessionPolicy = "proceed"
		}

		rp.Paused = defaults.Paused
		if p.Paused != nil {
			rp.Paused = *p.Paused
		}

		resolved[name] = rp
	}

	return resolved
}

// resolveRef resolves the git ref to a commit SHA via ls-remote (single HTTP call, no clone).
func (r *GatewaySyncReconciler) resolveRef(ctx context.Context, gs *stokerv1alpha1.GatewaySync) (git.Result, error) {
	ref := gs.Spec.Git.Ref

	// Check for webhook-requested ref override
	if requested, ok := gs.Annotations[stokertypes.AnnotationRequestedRef]; ok && requested != "" {
		ref = requested
	}

	// If the ref is already resolved at the desired ref and was resolved recently,
	// return cached result to avoid redundant ls-remote calls on status-triggered reconciles.
	if gs.Status.RefResolutionStatus == "Resolved" && gs.Status.LastSyncRef == ref &&
		gs.Status.LastSyncCommit != "" && gs.Status.LastSyncTime != nil {
		sinceLastSync := time.Since(gs.Status.LastSyncTime.Time)
		if sinceLastSync < r.pollingInterval(gs) {
			return git.Result{Commit: gs.Status.LastSyncCommit, Ref: gs.Status.LastSyncRef}, nil
		}
	}

	// Resolve auth — GitHub App uses cached tokens; other methods go through ResolveAuth.
	var auth transport.AuthMethod
	if gs.Spec.Git.Auth != nil && gs.Spec.Git.Auth.GitHubApp != nil {
		token, err := r.resolveGitHubAppToken(ctx, gs)
		if err != nil {
			return git.Result{}, fmt.Errorf("resolving GitHub App auth: %w", err)
		}
		auth = &gogithttp.BasicAuth{
			Username: "x-access-token",
			Password: token,
		}
	} else {
		var err error
		auth, err = git.ResolveAuth(ctx, r.Client, gs.Namespace, gs.Spec.Git.Auth)
		if err != nil {
			return git.Result{}, fmt.Errorf("resolving git auth: %w", err)
		}
	}

	lsCtx, cancel := context.WithTimeout(ctx, lsRemoteTimeout)
	defer cancel()

	return r.GitClient.LsRemote(lsCtx, gs.Spec.Git.Repo, ref, auth)
}

// resolveGitHubAppToken returns a cached GitHub App installation token, exchanging
// a new one if the cache is empty or the token is within 5 minutes of expiry.
func (r *GatewaySyncReconciler) resolveGitHubAppToken(ctx context.Context, gs *stokerv1alpha1.GatewaySync) (string, error) {
	appAuth := gs.Spec.Git.Auth.GitHubApp
	cacheKey := fmt.Sprintf("%d:%d", appAuth.AppID, appAuth.InstallationID)

	// Return cached token if still valid.
	if cached, ok := r.cachedGitHubToken(cacheKey); ok && time.Now().Before(cached.expiry.Add(-tokenRefreshBuffer)) {
		return cached.token, nil
	}

	// Read PEM secret.
	secret := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{Name: appAuth.PrivateKeySecretRef.Name, Namespace: gs.Namespace}, secret); err != nil {
		return "", fmt.Errorf("reading PEM secret %s/%s: %w", gs.Namespace, appAuth.PrivateKeySecretRef.Name, err)
	}
	pemBytes, ok := secret.Data[appAuth.PrivateKeySecretRef.Key]
	if !ok {
		return "", fmt.Errorf("key %q not found in secret %s/%s", appAuth.PrivateKeySecretRef.Key, gs.Namespace, appAuth.PrivateKeySecretRef.Name)
	}

	// Exchange PEM for installation token.
	result, err := git.ExchangeGitHubAppToken(ctx, pemBytes, appAuth.AppID, appAuth.InstallationID, appAuth.APIBaseURL)
	if err != nil {
		return "", err
	}

	// Cache the token.
	r.storeGitHubToken(cacheKey, cachedToken{token: result.Token, expiry: result.ExpiresAt})

	githubAppTokenExpiry.WithLabelValues(
		fmt.Sprintf("%d", appAuth.AppID),
		fmt.Sprintf("%d", appAuth.InstallationID),
	).Set(float64(result.ExpiresAt.Unix()))

	logf.FromContext(ctx).Info("exchanged GitHub App token", "appID", appAuth.AppID, "installationID", appAuth.InstallationID, "expiry", result.ExpiresAt)
	return result.Token, nil
}

// cleanupOwnedResources removes ConfigMaps and Secrets owned by this CR during deletion.
func (r *GatewaySyncReconciler) cleanupOwnedResources(ctx context.Context, gs *stokerv1alpha1.GatewaySync) error {
	log := logf.FromContext(ctx)

	// Remove all Prometheus metric series for this CR.
	cleanupCRMetrics(gs.Name, gs.Namespace)

	// Clean up metadata, status, and changes ConfigMaps
	cmNames := []string{
		fmt.Sprintf("stoker-metadata-%s", gs.Name),
		fmt.Sprintf("stoker-status-%s", gs.Name),
		fmt.Sprintf("stoker-changes-%s", gs.Name),
	}

	for _, name := range cmNames {
		cm := &corev1.ConfigMap{}
		err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: gs.Namespace}, cm)
		if errors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("getting ConfigMap %s: %w", name, err)
		}
		if err := r.Delete(ctx, cm); err != nil && !errors.IsNotFound(err) {
			return fmt.Errorf("deleting ConfigMap %s: %w", name, err)
		}
		log.Info("deleted ConfigMap", "name", name)
	}

	// Clean up GitHub App token Secret (if one was created for this CR).
	tokenSecretName := fmt.Sprintf("stoker-github-token-%s", gs.Name)
	tokenSecret := &corev1.Secret{}
	err := r.Get(ctx, types.NamespacedName{Name: tokenSecretName, Namespace: gs.Namespace}, tokenSecret)
	if err == nil {
		if err := r.Delete(ctx, tokenSecret); err != nil && !errors.IsNotFound(err) {
			return fmt.Errorf("deleting token Secret %s: %w", tokenSecretName, err)
		}
		log.Info("deleted token Secret", "name", tokenSecretName)
	} else if !errors.IsNotFound(err) {
		return fmt.Errorf("getting token Secret %s: %w", tokenSecretName, err)
	}

	return nil
}

// validateGitSecrets checks that git auth secrets exist (if configured).
func (r *GatewaySyncReconciler) validateGitSecrets(ctx context.Context, gs *stokerv1alpha1.GatewaySync) error {
	if gs.Spec.Git.Auth == nil {
		return nil
	}

	secret := &corev1.Secret{}
	key := types.NamespacedName{Namespace: gs.Namespace}

	if gs.Spec.Git.Auth.SSHKey != nil {
		key.Name = gs.Spec.Git.Auth.SSHKey.SecretRef.Name
		if err := r.Get(ctx, key, secret); err != nil {
			return fmt.Errorf("SSH key secret %q not found: %w", key.Name, err)
		}
	}
	if gs.Spec.Git.Auth.Token != nil {
		key.Name = gs.Spec.Git.Auth.Token.SecretRef.Name
		if err := r.Get(ctx, key, secret); err != nil {
			return fmt.Errorf("token secret %q not found: %w", key.Name, err)
		}
	}
	if gs.Spec.Git.Auth.GitHubApp != nil {
		key.Name = gs.Spec.Git.Auth.GitHubApp.PrivateKeySecretRef.Name
		if err := r.Get(ctx, key, secret); err != nil {
			return fmt.Errorf("GitHub App private key secret %q not found: %w", key.Name, err)
		}
	}

	return nil
}

// validateAPIKeySecret checks that the gateway API key secret exists.
func (r *GatewaySyncReconciler) validateAPIKeySecret(ctx context.Context, gs *stokerv1alpha1.GatewaySync) error {
	secret := &corev1.Secret{}
	key := types.NamespacedName{
		Name:      gs.Spec.Gateway.API.SecretName,
		Namespace: gs.Namespace,
	}
	if err := r.Get(ctx, key, secret); err != nil {
		return fmt.Errorf("gateway API key secret %q not found: %w", key.Name, err)
	}
	return nil
}

// shortCommit returns the first 7 characters of a commit SHA, or the full string if shorter.
func shortCommit(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// ensureMetadataConfigMap creates or updates the metadata ConfigMap that signals agents.
func (r *GatewaySyncReconciler) ensureMetadataConfigMap(ctx context.Context, gs *stokerv1alpha1.GatewaySync, result git.Result) error {
	cmName := fmt.Sprintf("stoker-metadata-%s", gs.Name)
	cm := &corev1.ConfigMap{}
	key := types.NamespacedName{Name: cmName, Namespace: gs.Namespace}

	data := map[string]string{
		"commit": result.Commit,
		"ref":    result.Ref,
		"gitURL": gs.Spec.Git.Repo,
		"paused": fmt.Sprintf("%t", gs.Spec.Paused),
	}

	// Include auth type so agent knows which credential source to use.
	data["authType"] = resolveAuthType(gs.Spec.Git.Auth)

	// For GitHub App auth, write the installation token to a Secret so the agent
	// can authenticate git operations via a mounted file (not a ConfigMap).
	if gs.Spec.Git.Auth != nil && gs.Spec.Git.Auth.GitHubApp != nil {
		appAuth := gs.Spec.Git.Auth.GitHubApp
		cacheKey := fmt.Sprintf("%d:%d", appAuth.AppID, appAuth.InstallationID)
		if cached, ok := r.cachedGitHubToken(cacheKey); ok {
			if err := r.ensureGitHubTokenSecret(ctx, gs, cached.token); err != nil {
				return fmt.Errorf("ensuring GitHub App token Secret: %w", err)
			}
		}
	}

	// Serialize resolved profiles as JSON.
	profiles := r.resolveProfiles(gs)
	profilesJSON, err := json.Marshal(profiles)
	if err != nil {
		return fmt.Errorf("serializing profiles: %w", err)
	}
	data["profiles"] = string(profilesJSON)

	// Gateway connection info for agent's Ignition API calls.
	data["gatewayPort"] = fmt.Sprintf("%d", gs.Spec.Gateway.Port)
	if gs.Spec.Gateway.TLS != nil {
		data["gatewayTLS"] = fmt.Sprintf("%t", *gs.Spec.Gateway.TLS)
	}

	err = r.Get(ctx, key, cm)
	if errors.IsNotFound(err) {
		cm = &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      cmName,
				Namespace: gs.Namespace,
				Labels: map[string]string{
					labelManagedBy:          "stoker-controller",
					stokertypes.LabelCRName: gs.Name,
				},
			},
			Data: data,
		}
		if err := controllerutil.SetControllerReference(gs, cm, r.Scheme); err != nil {
			return fmt.Errorf("setting owner ref on ConfigMap: %w", err)
		}
		return r.Create(ctx, cm)
	}
	if err != nil {
		return fmt.Errorf("getting ConfigMap %s: %w", cmName, err)
	}

	if reflect.DeepEqual(cm.Data, data) {
		return nil
	}
	cm.Data = data
	return r.Update(ctx, cm)
}

// ensureGitHubTokenSecret creates or updates a Secret holding the GitHub App installation
// token. The agent mounts this Secret at /etc/stoker/git-token/token and uses it for
// git clone authentication. Storing the token in a Secret (not a ConfigMap) restricts
// access via RBAC and prevents it from appearing in ConfigMap audit logs.
func (r *GatewaySyncReconciler) ensureGitHubTokenSecret(ctx context.Context, gs *stokerv1alpha1.GatewaySync, token string) error {
	secretName := fmt.Sprintf("stoker-github-token-%s", gs.Name)
	secret := &corev1.Secret{}
	key := types.NamespacedName{Name: secretName, Namespace: gs.Namespace}

	err := r.Get(ctx, key, secret)
	if errors.IsNotFound(err) {
		secret = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      secretName,
				Namespace: gs.Namespace,
				Labels: map[string]string{
					labelManagedBy:          "stoker-controller",
					stokertypes.LabelCRName: gs.Name,
				},
				Annotations: map[string]string{
					stokertypes.AnnotationSecretType: "github-app-token",
				},
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{
				"token": []byte(token),
			},
		}
		if err := controllerutil.SetControllerReference(gs, secret, r.Scheme); err != nil {
			return fmt.Errorf("setting owner ref on token Secret: %w", err)
		}
		return r.Create(ctx, secret)
	}
	if err != nil {
		return fmt.Errorf("getting token Secret %s: %w", secretName, err)
	}

	// Update token if it changed (refreshed installation token).
	if string(secret.Data["token"]) == token {
		return nil
	}
	secret.Data["token"] = []byte(token)
	return r.Update(ctx, secret)
}

// resolveAuthType determines the auth type string from the git auth spec.
func resolveAuthType(auth *stokerv1alpha1.GitAuthSpec) string {
	if auth == nil {
		return "none"
	}
	if auth.SSHKey != nil {
		return "ssh"
	}
	if auth.Token != nil {
		return "token"
	}
	if auth.GitHubApp != nil {
		return "githubApp"
	}
	return "none"
}

// setCondition sets a condition on the CR's status.
func (r *GatewaySyncReconciler) setCondition(_ context.Context, gs *stokerv1alpha1.GatewaySync, condType string, status metav1.ConditionStatus, reason, message string) {
	condition := metav1.Condition{
		Type:               condType,
		Status:             status,
		ObservedGeneration: gs.Generation,
		LastTransitionTime: metav1.Now(),
		Reason:             reason,
		Message:            message,
	}

	// Replace existing condition of same type, or append
	for i, c := range gs.Status.Conditions {
		if c.Type == condType {
			if c.Status != status {
				gs.Status.Conditions[i] = condition
			} else {
				// Update reason/message but keep transition time
				gs.Status.Conditions[i].Reason = reason
				gs.Status.Conditions[i].Message = message
				gs.Status.Conditions[i].ObservedGeneration = gs.Generation
			}
			return
		}
	}
	gs.Status.Conditions = append(gs.Status.Conditions, condition)
}

// patchStatus applies a status update via server-side merge patch.
// This avoids resourceVersion conflicts when overlapping reconciles both update status.
func (r *GatewaySyncReconciler) patchStatus(ctx context.Context, gs *stokerv1alpha1.GatewaySync, base client.Object) error {
	return r.Status().Patch(ctx, gs, client.MergeFrom(base))
}

// pollingInterval returns the requeue interval from the CR spec.
func (r *GatewaySyncReconciler) pollingInterval(gs *stokerv1alpha1.GatewaySync) time.Duration {
	if gs.Spec.Polling.Enabled != nil && !*gs.Spec.Polling.Enabled {
		return 0 // no requeue if polling disabled
	}
	interval := gs.Spec.Polling.Interval
	if interval == "" {
		interval = "60s"
	}
	d, err := time.ParseDuration(interval)
	if err != nil {
		return 60 * time.Second
	}
	return d
}

// requeueInterval returns the requeue interval, accounting for both the polling
// interval and GitHub App token expiry (whichever is sooner).
func (r *GatewaySyncReconciler) requeueInterval(gs *stokerv1alpha1.GatewaySync) time.Duration {
	interval := r.pollingInterval(gs)

	if gs.Spec.Git.Auth != nil && gs.Spec.Git.Auth.GitHubApp != nil {
		appAuth := gs.Spec.Git.Auth.GitHubApp
		cacheKey := fmt.Sprintf("%d:%d", appAuth.AppID, appAuth.InstallationID)
		if cached, ok := r.cachedGitHubToken(cacheKey); ok {
			tokenRefresh := time.Until(cached.expiry) - tokenRefreshBuffer
			if tokenRefresh > 0 && (interval == 0 || tokenRefresh < interval) {
				interval = tokenRefresh
			}
		}
	}

	return interval
}

// clearRequestedRefIfCaughtUp removes the requested-ref annotation once spec.git.ref
// has caught up to the value the webhook set (i.e., ArgoCD has synced the values change).
//
// The annotation is a fast-path override that lets the controller act immediately
// after a Kargo promotion without waiting for ArgoCD's polling cycle. Once ArgoCD
// syncs, the annotation is stale — leaving it would permanently override spec.git.ref,
// pinning the controller to an old ref if a future webhook fails to fire.
//
// Comparison uses "v"-prefix normalization so "v2.2.3" (git tag) matches "2.2.3"
// (values.yaml convention).
func (r *GatewaySyncReconciler) clearRequestedRefIfCaughtUp(ctx context.Context, req ctrl.Request, gs *stokerv1alpha1.GatewaySync) {
	annRef, ok := gs.Annotations[stokertypes.AnnotationRequestedRef]
	if !ok || annRef == "" {
		return
	}
	if strings.TrimPrefix(annRef, "v") != strings.TrimPrefix(gs.Spec.Git.Ref, "v") {
		return
	}
	log := logf.FromContext(ctx)
	// Re-fetch to get latest resourceVersion and avoid a conflict with the status patch.
	var fresh stokerv1alpha1.GatewaySync
	if err := r.Get(ctx, req.NamespacedName, &fresh); err != nil {
		return
	}
	if fresh.Annotations[stokertypes.AnnotationRequestedRef] != annRef {
		return // already cleared by a concurrent reconcile
	}
	freshBase := fresh.DeepCopy()
	delete(fresh.Annotations, stokertypes.AnnotationRequestedRef)
	if patchErr := r.Patch(ctx, &fresh, client.MergeFrom(freshBase)); patchErr != nil {
		log.Error(patchErr, "failed to clear requested-ref annotation")
	} else {
		log.Info("cleared requested-ref annotation (spec.git.ref matched)", "ref", annRef)
	}
}

// conditionHasStatus returns true if the conditions slice already contains
// a condition of the given type with the given status.
func conditionHasStatus(conds []metav1.Condition, condType string, status metav1.ConditionStatus) bool {
	for _, c := range conds {
		if c.Type == condType && c.Status == status {
			return true
		}
	}
	return false
}

// conditionHasReason returns true if the conditions slice already contains
// a condition of the given type with the given reason.
func conditionHasReason(conds []metav1.Condition, condType, reason string) bool {
	for _, c := range conds {
		if c.Type == condType && c.Reason == reason {
			return true
		}
	}
	return false
}

// annotationOrGenerationChanged passes update events where either the
// generation changed (spec edits) or annotations changed (webhook receiver).
// This filters out status-only patches that would cause reconcile noise.
type annotationOrGenerationChanged struct {
	predicate.GenerationChangedPredicate
}

func (p annotationOrGenerationChanged) Update(e event.UpdateEvent) bool {
	if p.GenerationChangedPredicate.Update(e) {
		return true
	}
	return !reflect.DeepEqual(e.ObjectOld.GetAnnotations(), e.ObjectNew.GetAnnotations())
}

// SetupWithManager sets up the controller with the Manager.
func (r *GatewaySyncReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&stokerv1alpha1.GatewaySync{}, builder.WithPredicates(annotationOrGenerationChanged{})).
		Owns(&corev1.ConfigMap{}).
		Owns(&corev1.Secret{}).
		Owns(&rbacv1.RoleBinding{}).
		Watches(&corev1.Pod{}, handler.EnqueueRequestsFromMapFunc(r.findGatewaySyncForPod)).
		WithOptions(controller.Options{MaxConcurrentReconciles: 5}).
		Named("gatewaysync").
		Complete(r)
}
