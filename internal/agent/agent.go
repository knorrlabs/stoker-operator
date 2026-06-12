package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/ia-eknorr/stoker-operator/internal/git"
	"github.com/ia-eknorr/stoker-operator/internal/ignition"
	"github.com/ia-eknorr/stoker-operator/internal/syncengine"
	"github.com/ia-eknorr/stoker-operator/pkg/conditions"
	stokertypes "github.com/ia-eknorr/stoker-operator/pkg/types"
)

const defaultProfileName = "default"

// agentVersion is set at build time via ldflags. Falls back to "dev" for local builds.
var agentVersion = "dev"

// gatewaySyncGVK is the GVK for the GatewaySync CR, used with unstructured client.
var gatewaySyncGVK = schema.GroupVersionKind{
	Group:   "stoker.io",
	Version: "v1alpha1",
	Kind:    "GatewaySync",
}

// Agent orchestrates the sync process.
type Agent struct {
	Config       *Config
	K8sClient    client.Client
	GitClient    git.Client
	SyncEngine   *syncengine.Engine
	IgnitionAPI  *ignition.Client
	HealthServer *HealthServer
	Metrics      *AgentMetrics
	Watcher      *Watcher
	Recorder     record.EventRecorder // may be nil

	crRef              *unstructured.Unstructured // cached for event target
	lastSyncedCommit   string
	lastSyncedProfiles string // raw profiles JSON; re-sync when CR profile changes
	initialSyncDone    bool

	// Exponential backoff for consecutive sync failures.
	consecutiveErrors int
	backoffUntil      time.Time

	// syncMu serializes sync cycles. The post-commission goroutine and the
	// watcher loop share one staging directory; concurrent ExecutePlan calls
	// would delete it out from under each other mid-merge. It also guards
	// lastSyncedCommit and lastSyncedProfiles.
	syncMu sync.Mutex

	// Graceful shutdown: track in-flight syncs.
	syncInProgress atomic.Bool
	shutdownCh     chan struct{}
}

// New creates a new Agent with all dependencies wired.
func New(cfg *Config, k8sClient client.Client, recorder record.EventRecorder) *Agent {
	// Build exclude patterns.
	excludes := []string{"**/.git/**", "**/.git", "**/.gitkeep", "**/.resources/**", "**/.resources"}

	// Build Ignition API client. The key is read per request so a rotated
	// Secret takes effect without restarting the agent.
	igClient := ignition.NewClient(cfg.GatewayScheme(), cfg.GatewayHost(), cfg.APIKey)

	return &Agent{
		Config:       cfg,
		K8sClient:    k8sClient,
		GitClient:    &git.NativeGitClient{},
		SyncEngine:   &syncengine.Engine{ExcludePatterns: excludes},
		IgnitionAPI:  igClient,
		HealthServer: NewHealthServer(":8082"),
		Metrics:      NewAgentMetrics(),
		Watcher:      NewWatcher(k8sClient, cfg.CRNamespace, cfg.CRName, time.Duration(cfg.SyncPeriod)*time.Second),
		Recorder:     recorder,
		shutdownCh:   make(chan struct{}, 1),
	}
}

// Run starts the agent. It clones the repo, performs the initial sync, marks
// the startup probe as ready (which gates the gateway container start when
// deployed as a native sidecar), then watches for changes until ctx is cancelled.
func (a *Agent) Run(ctx context.Context) error {
	log := logf.FromContext(ctx).WithName("agent")

	log.Info("starting agent",
		"gateway", a.Config.GatewayName,
		"cr", a.Config.CRName,
		"namespace", a.Config.CRNamespace,
		"repoPath", a.Config.RepoPath,
		"dataPath", a.Config.DataPath,
		"syncPeriod", a.Config.SyncPeriod,
	)

	// Start health server immediately so startup probe has an endpoint.
	go a.HealthServer.Start(ctx)

	// Start metrics server on a dedicated port (separate from health probes).
	metricsServer := NewMetricsServer(":8083", a.Metrics.Handler())
	go metricsServer.Start(ctx)

	// Read metadata ConfigMap to get git URL and commit.
	log.Info("reading metadata ConfigMap")
	meta, err := a.waitForMetadata(ctx)
	if err != nil {
		return fmt.Errorf("waiting for metadata: %w", err)
	}

	log.Info("metadata loaded", "gitURL", meta.GitURL, "commit", meta.Commit, "ref", meta.Ref)

	// Apply profile-level syncPeriod if present (overrides env var default).
	a.applySyncPeriodFromMeta(meta, log)

	// Cache GatewaySync CR reference for event emission.
	a.fetchCRRef(ctx)

	// Use git URL from metadata ConfigMap, fall back to empty (shouldn't happen).
	gitURL := meta.GitURL
	if gitURL == "" {
		return fmt.Errorf("gitURL not found in metadata ConfigMap")
	}

	// Initial clone. Git auth comes from mounted credential files via
	// GIT_SSH_KEY_FILE / GIT_TOKEN_FILE env vars, read fresh on every git
	// operation so rotated Secrets are picked up without a restart.
	log.Info("cloning repository", "url", gitURL, "ref", meta.Ref)
	cloneStart := time.Now()
	result, err := a.GitClient.CloneOrFetch(ctx, gitURL, meta.Ref, a.Config.RepoPath, nil)
	a.Metrics.GitFetchDuration.WithLabelValues("clone").Observe(time.Since(cloneStart).Seconds())
	if err != nil {
		a.Metrics.GitFetchTotal.WithLabelValues("clone", "error").Inc()
		a.event(corev1.EventTypeWarning, conditions.ReasonCloneFailed, "Initial clone failed: %v", err)
		return fmt.Errorf("initial clone: %w", err)
	}
	a.Metrics.GitFetchTotal.WithLabelValues("clone", "success").Inc()
	log.Info("clone complete", "commit", result.Commit)

	// Initial sync (blocking). Files land on disk before startup probe passes,
	// so the gateway container won't start until config is ready.
	log.Info("performing initial sync")
	syncErr := a.syncOnce(ctx, result.Commit, result.Ref, true, meta.Profiles)
	if syncErr != nil {
		log.Error(syncErr, "initial sync had errors (continuing)")
	}

	a.initialSyncDone = true

	// Mark startup/readiness probes as passing. When deployed as a native
	// sidecar (initContainer with restartPolicy: Always), this signals K8s
	// to proceed with starting the gateway container.
	a.HealthServer.MarkReady()
	log.Info("initial sync complete, startup probe now passing")

	// After the gateway finishes commissioning (first boot), re-sync files
	// and trigger a scan. Ignition's commissioning can overwrite resources
	// (e.g. security-properties) with defaults. This post-commission sync
	// restores the git-sourced config.
	go a.postCommissionSync(ctx, result.Commit, result.Ref)

	// Start watcher in background.
	go a.Watcher.Run(ctx)

	// Main loop: watch for trigger events with graceful shutdown.
	const shutdownDeadline = 15 * time.Second

	for {
		select {
		case <-ctx.Done():
			log.Info("shutdown signal received")
			a.HealthServer.MarkNotReady()

			if !a.syncInProgress.Load() {
				log.Info("no sync in progress, shutting down immediately")
				return nil
			}

			log.Info("waiting for in-flight sync to complete", "deadline", shutdownDeadline)
			select {
			case <-a.shutdownCh:
				log.Info("in-flight sync completed during shutdown")
			case <-time.After(shutdownDeadline):
				log.Info("shutdown deadline exceeded, aborting")
			}
			return nil

		case <-a.Watcher.Events():
			log.V(1).Info("sync triggered")
			a.handleSyncTrigger(ctx, gitURL)
		}
	}
}

// postCommissionSync waits for the gateway to become responsive after first boot,
// then forces a re-sync and scan. This is needed because Ignition's commissioning
// process can overwrite config resources (e.g. security-properties) with defaults.
// Since the agent syncs as a native sidecar BEFORE the gateway starts, the
// commissioning defaults would otherwise shadow the git-sourced config.
func (a *Agent) postCommissionSync(ctx context.Context, commit, ref string) {
	log := logf.FromContext(ctx).WithName("post-commission")
	startupStart := time.Now()

	// Poll until gateway port is responding. We can't use HealthCheck() here
	// because it requires API token auth, and the commissioning defaults may
	// have overwritten the security-properties that grant the token access.
	// Instead, check for any HTTP response (even 401/403 means the gateway is up).
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}

		if err := a.IgnitionAPI.PortCheck(); err != nil {
			log.V(1).Info("gateway not ready yet", "error", err)
			continue
		}

		a.Metrics.GatewayStartupDuration.Observe(time.Since(startupStart).Seconds())
		log.Info("gateway responsive, running post-commission re-sync")
		a.syncMu.Lock()
		err := a.syncOnce(ctx, commit, ref, false, a.lastSyncedProfiles)
		a.syncMu.Unlock()
		if err != nil {
			log.Error(err, "post-commission sync failed")
		} else {
			log.Info("post-commission sync complete")
		}
		return
	}
}

// waitForMetadata polls for the metadata ConfigMap until it's available.
func (a *Agent) waitForMetadata(ctx context.Context) (*Metadata, error) {
	log := logf.FromContext(ctx)

	for {
		meta, err := ReadMetadataConfigMap(ctx, a.K8sClient, a.Config.CRNamespace, a.Config.CRName)
		if err == nil && meta.Commit != "" {
			return meta, nil
		}

		if err != nil {
			if isForbidden(err) {
				log.Error(err, "RBAC permission denied — agent cannot read metadata ConfigMap",
					"namespace", a.Config.CRNamespace,
					"configmap", MetadataConfigMapName(a.Config.CRName),
					"hint", fmt.Sprintf("ensure agent RBAC is configured: kubectl create rolebinding stoker-agent -n %s --clusterrole=stoker-agent --serviceaccount=%s:<service-account>",
						a.Config.CRNamespace, a.Config.CRNamespace),
				)
			} else {
				log.V(1).Info("metadata not available yet, retrying", "error", err)
			}
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
}

// handleSyncTrigger reads the latest metadata and performs a sync if needed.
func (a *Agent) handleSyncTrigger(ctx context.Context, gitURL string) {
	log := logf.FromContext(ctx).WithName("sync")

	// Check backoff before doing any work.
	if time.Now().Before(a.backoffUntil) {
		log.V(1).Info("in backoff period, skipping sync", "until", a.backoffUntil)
		a.Metrics.SyncSkippedTotal.WithLabelValues("backoff").Inc()
		return
	}

	meta, err := ReadMetadataConfigMap(ctx, a.K8sClient, a.Config.CRNamespace, a.Config.CRName)
	if err != nil {
		log.Error(err, "failed to read metadata ConfigMap")
		return
	}

	if meta.Paused == "true" {
		log.V(1).Info("CR is paused, skipping sync")
		a.Metrics.SyncSkippedTotal.WithLabelValues("paused").Inc()
		return
	}

	a.syncMu.Lock()
	defer a.syncMu.Unlock()

	// Check if commit or profiles changed.
	if meta.Commit == a.lastSyncedCommit && meta.Profiles == a.lastSyncedProfiles {
		log.V(1).Info("commit and profiles unchanged, skipping sync", "commit", meta.Commit)
		a.Metrics.SyncSkippedTotal.WithLabelValues("commit_unchanged").Inc()
		return
	}

	if meta.Commit != a.lastSyncedCommit {
		log.Info("new commit detected", "old", a.lastSyncedCommit, "new", meta.Commit, "ref", meta.Ref)
	} else {
		log.Info("profiles changed, re-syncing", "commit", meta.Commit)
	}

	// Mark sync in progress for graceful shutdown tracking.
	a.syncInProgress.Store(true)
	defer func() {
		a.syncInProgress.Store(false)
		select {
		case a.shutdownCh <- struct{}{}:
		default:
		}
	}()

	// Use a context that survives SIGTERM so in-flight syncs can complete.
	syncCtx := context.WithoutCancel(ctx)

	// Fetch and checkout new commit.
	fetchStart := time.Now()
	result, err := a.GitClient.CloneOrFetch(syncCtx, gitURL, meta.Ref, a.Config.RepoPath, nil)
	a.Metrics.GitFetchDuration.WithLabelValues("fetch").Observe(time.Since(fetchStart).Seconds())
	if err != nil {
		a.Metrics.GitFetchTotal.WithLabelValues("fetch", "error").Inc()
		a.consecutiveErrors++
		delay := min(30*time.Second<<(a.consecutiveErrors-1), 5*time.Minute)
		a.backoffUntil = time.Now().Add(delay)
		log.Error(err, "git fetch failed, backing off", "consecutiveErrors", a.consecutiveErrors, "retryIn", delay)
		a.reportError(ctx, meta.Commit, meta.Ref, fmt.Sprintf("git fetch: %v", err))
		return
	}
	a.Metrics.GitFetchTotal.WithLabelValues("fetch", "success").Inc()

	log.V(1).Info("git updated", "commit", result.Commit)

	// Pre-sync designer session check via resolved profile from metadata.
	profile, profileName, err := a.lookupProfile(meta)
	if err != nil {
		log.Error(err, "failed to look up profile for designer check")
		a.Metrics.SyncSkippedTotal.WithLabelValues("profile_error").Inc()
		a.reportError(ctx, result.Commit, result.Ref, fmt.Sprintf("looking up profile: %v", err))
		return
	}

	// Update watcher period if profile specifies a different syncPeriod.
	a.applySyncPeriod(profile, log)

	if !profile.Paused && !profile.DryRun {
		if blocked := a.checkDesignerSessions(ctx, profile.DesignerSessionPolicy, result.Commit, result.Ref); blocked {
			a.Metrics.DesignerBlocked.Set(1)
			a.Metrics.SyncSkippedTotal.WithLabelValues("designer_blocked").Inc()
			a.event(corev1.EventTypeWarning, conditions.ReasonSyncSkipped,
				"Sync skipped: designer sessions blocked sync (policy=%s)", profile.DesignerSessionPolicy)
			return
		}
		a.Metrics.DesignerBlocked.Set(0)
	}

	_ = profileName // used in syncWithProfile via metadata re-read

	if syncErr := a.syncOnce(syncCtx, result.Commit, result.Ref, false, meta.Profiles); syncErr != nil {
		a.consecutiveErrors++
		delay := min(30*time.Second<<(a.consecutiveErrors-1), 5*time.Minute)
		a.backoffUntil = time.Now().Add(delay)
		log.Error(syncErr, "sync had errors, backing off", "consecutiveErrors", a.consecutiveErrors, "retryIn", delay)
	} else {
		a.consecutiveErrors = 0
		a.backoffUntil = time.Time{}
	}
}

// applySyncPeriodFromMeta looks up the resolved profile and applies its
// syncPeriod to the watcher if set.
func (a *Agent) applySyncPeriodFromMeta(meta *Metadata, log logr.Logger) {
	profile, _, err := a.lookupProfile(meta)
	if err != nil {
		return
	}
	a.applySyncPeriod(profile, log)
}

// applySyncPeriod updates the watcher's fallback interval if the profile
// specifies a non-zero syncPeriod.
func (a *Agent) applySyncPeriod(profile *stokertypes.ResolvedProfile, log logr.Logger) {
	if profile.SyncPeriod > 0 {
		newPeriod := time.Duration(profile.SyncPeriod) * time.Second
		a.Watcher.UpdatePeriod(newPeriod)
		log.V(1).Info("using profile-level syncPeriod", "syncPeriod", profile.SyncPeriod)
	}
}

// lookupProfile resolves the agent's profile from metadata ConfigMap profiles.
// Falls back to "default" if no profile name is configured.
func (a *Agent) lookupProfile(meta *Metadata) (*stokertypes.ResolvedProfile, string, error) {
	profiles, err := ParseResolvedProfiles(meta.Profiles)
	if err != nil {
		return nil, "", fmt.Errorf("parsing profiles: %w", err)
	}

	profileName := a.Config.ProfileName
	if profileName == "" {
		profileName = defaultProfileName
	}

	profile, ok := profiles[profileName]
	if !ok {
		return nil, profileName, fmt.Errorf("profile %q not found in metadata ConfigMap", profileName)
	}

	return profile, profileName, nil
}

// checkDesignerSessions enforces the designer session policy before sync.
// Returns true if the sync should be skipped (blocked or failed).
func (a *Agent) checkDesignerSessions(ctx context.Context, policy, commit, ref string) bool {
	log := logf.FromContext(ctx).WithName("designer-check")

	if policy == "" {
		policy = "proceed"
	}

	sessions, err := a.IgnitionAPI.GetDesignerSessions(ctx)
	if err != nil {
		log.Info("failed to query designer sessions (continuing sync)", "error", err)
		a.Metrics.DesignerSessionsActive.Set(0)
		return false
	}

	a.Metrics.DesignerSessionsActive.Set(float64(len(sessions)))

	if len(sessions) == 0 {
		return false
	}

	// Format session info for logging.
	sessionInfo := formatDesignerSessions(sessions)

	switch policy {
	case "proceed":
		log.Info("designer sessions active, proceeding per policy", "sessions", sessionInfo)
		return false

	case "fail":
		log.Info("designer sessions active, aborting sync per policy", "sessions", sessionInfo)
		a.event(corev1.EventTypeWarning, conditions.ReasonDesignerSessionsBlocked, "Designer sessions blocked sync: %s", sessionInfo)
		a.reportError(ctx, commit, ref, fmt.Sprintf("designer sessions active (policy=fail): %s", sessionInfo))
		return true

	case "wait":
		log.Info("designer sessions active, waiting for close", "sessions", sessionInfo)
		a.setDesignerBlocked(ctx, true)
		defer a.setDesignerBlocked(ctx, false)

		// Retry every 10s for up to 5 minutes.
		timeout := time.After(5 * time.Minute)
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return true
			case <-timeout:
				log.Info("timed out waiting for designer sessions to close", "sessions", sessionInfo)
				a.event(corev1.EventTypeWarning, conditions.ReasonDesignerSessionsBlocked, "Designer sessions blocked sync (5m timeout): %s", sessionInfo)
				a.reportError(ctx, commit, ref, fmt.Sprintf("designer sessions still active after 5m timeout: %s", sessionInfo))
				return true
			case <-ticker.C:
				sessions, err = a.IgnitionAPI.GetDesignerSessions(ctx)
				if err != nil {
					log.Info("designer check failed during wait (continuing sync)", "error", err)
					return false
				}
				if len(sessions) == 0 {
					log.Info("designer sessions closed, proceeding with sync")
					return false
				}
				log.V(1).Info("still waiting for designer sessions", "sessions", formatDesignerSessions(sessions))
			}
		}

	default:
		log.Info("unknown designer session policy, proceeding", "policy", policy)
		return false
	}
}

// setDesignerBlocked updates the DesignerSessionsBlocked field in the status ConfigMap.
func (a *Agent) setDesignerBlocked(ctx context.Context, blocked bool) {
	status := &stokertypes.GatewayStatus{
		SyncStatus:              stokertypes.SyncStatusPending,
		AgentVersion:            agentVersion,
		LastSyncTime:            time.Now().UTC().Format(time.RFC3339),
		DesignerSessionsBlocked: blocked,
	}
	if a.lastSyncedCommit != "" {
		status.SyncedCommit = a.lastSyncedCommit
	}
	_ = WriteStatusConfigMap(ctx, a.K8sClient, a.Config.CRNamespace, a.Config.CRName, a.Config.GatewayName, status)
}

// formatDesignerSessions builds a human-readable summary of active sessions.
func formatDesignerSessions(sessions []ignition.DesignerSession) string {
	parts := make([]string, len(sessions))
	for i, s := range sessions {
		parts[i] = fmt.Sprintf("%s on %s", s.User, s.Project)
	}
	return strings.Join(parts, ", ")
}

// syncOnce performs a single sync cycle: copy files, trigger scan, report status.
func (a *Agent) syncOnce(ctx context.Context, commit, ref string, isInitial bool, profiles string) error {
	log := logf.FromContext(ctx).WithName("sync")

	syncStart := time.Now()
	syncResult, profileName, isDryRun, err := a.syncWithProfile(ctx)
	a.Metrics.SyncDuration.WithLabelValues(profileName).Observe(time.Since(syncStart).Seconds())

	if err != nil {
		a.Metrics.SyncTotal.WithLabelValues(profileName, "error").Inc()
		a.Metrics.LastSyncSuccess.Set(0)
		a.reportError(ctx, commit, ref, fmt.Sprintf("sync engine: %v", err))
		a.event(corev1.EventTypeWarning, conditions.ReasonSyncFailed, "Sync failed: %v", err)
		return fmt.Errorf("sync engine: %w", err)
	}

	filesChanged := int32(syncResult.FilesAdded + syncResult.FilesModified + syncResult.FilesDeleted)
	a.Metrics.FilesChanged.WithLabelValues(profileName).Set(float64(filesChanged))
	a.Metrics.FilesAdded.WithLabelValues(profileName).Set(float64(syncResult.FilesAdded))
	a.Metrics.FilesModified.WithLabelValues(profileName).Set(float64(syncResult.FilesModified))
	a.Metrics.FilesDeleted.WithLabelValues(profileName).Set(float64(syncResult.FilesDeleted))

	syncLog := log.V(1)
	if filesChanged > 0 {
		syncLog = log
	}
	syncLog.Info("files synced",
		"added", syncResult.FilesAdded,
		"modified", syncResult.FilesModified,
		"deleted", syncResult.FilesDeleted,
		"projects", syncResult.ProjectsSynced,
		"duration", syncResult.Duration,
		"profile", profileName,
		"dryRun", isDryRun,
	)

	// During shutdown, skip scan and status write — file sync (critical) is done.
	if a.HealthServer.IsShuttingDown() {
		log.Info("shutdown in progress, skipping scan and status write")
		a.lastSyncedCommit = commit
		a.lastSyncedProfiles = profiles
		return nil
	}

	// Trigger Ignition scan API on every non-initial sync (regardless of filesChanged).
	// Only report "Synced" if both scan endpoints return 200.
	var scanResultStr string
	if isDryRun {
		log.Info("dry-run mode, skipping scan API")
	} else if !isInitial {
		log.V(1).Info("triggering Ignition scan API")
		scanStart := time.Now()
		scanResult := a.IgnitionAPI.TriggerScan()
		a.Metrics.ScanDuration.Observe(time.Since(scanStart).Seconds())
		scanResultStr = scanResult.String()
		if scanResult.Error != "" {
			a.Metrics.ScanTotal.WithLabelValues("error").Inc()
			log.Info("scan API failed (non-fatal)", "error", scanResult.Error)
		} else {
			a.Metrics.ScanTotal.WithLabelValues("success").Inc()
			log.V(1).Info("scan complete", "result", scanResultStr)
		}
	} else {
		// On initial sync, attempt a health check but don't require it.
		if err := a.IgnitionAPI.HealthCheck(); err != nil {
			log.Info("gateway health check failed (expected on initial sync)", "error", err)
			scanResultStr = fmt.Sprintf("health check failed: %v", err)
		}
	}

	// Determine status: only "Synced" if scan succeeded (both 200).
	// Initial sync reports "Pending" since gateway isn't running yet to validate.
	// Dry-run is always "Synced" on success — staging files IS the success state.
	syncStatus := stokertypes.SyncStatusSynced
	var errorMsg string
	if isDryRun {
		// dry-run: no scan needed, staging files successfully is the success state
	} else if isInitial {
		syncStatus = stokertypes.SyncStatusPending
	} else if scanResultStr == "" || strings.Contains(scanResultStr, "error") {
		syncStatus = stokertypes.SyncStatusError
		errorMsg = scanResultStr
		if errorMsg == "" {
			errorMsg = "scan not performed"
		}
	}

	// Report status to ConfigMap.
	status := &stokertypes.GatewayStatus{
		SyncStatus:       syncStatus,
		SyncedCommit:     commit,
		SyncedRef:        ref,
		LastSyncTime:     time.Now().UTC().Format(time.RFC3339),
		LastSyncDuration: syncResult.Duration.Round(time.Millisecond).String(),
		AgentVersion:     agentVersion,
		LastScanResult:   scanResultStr,
		FilesChanged:     filesChanged,
		ProjectsSynced:   syncResult.ProjectsSynced,
		ErrorMessage:     errorMsg,
		ProfileName:      profileName,
		DryRun:           isDryRun,
	}

	if isDryRun && syncResult.DryRunDiff != nil {
		status.DryRunDiffAdded = int32(len(syncResult.DryRunDiff.Added))
		status.DryRunDiffModified = int32(len(syncResult.DryRunDiff.Modified))
		status.DryRunDiffDeleted = int32(len(syncResult.DryRunDiff.Deleted))
	}

	if err := WriteStatusConfigMap(ctx, a.K8sClient, a.Config.CRNamespace, a.Config.CRName, a.Config.GatewayName, status); err != nil {
		log.Error(err, "failed to write status ConfigMap")
	} else {
		log.V(1).Info("status written to ConfigMap", "gateway", a.Config.GatewayName, "status", syncStatus)
	}

	// A failed scan means files are on disk but the gateway never loaded them.
	// Return an error WITHOUT recording the commit as synced, so the caller's
	// backoff retries until the gateway applies the config — otherwise the
	// gateway sits in Error until the next commit happens to arrive.
	if syncStatus == stokertypes.SyncStatusError {
		a.Metrics.SyncTotal.WithLabelValues(profileName, "error").Inc()
		a.Metrics.LastSyncSuccess.Set(0)
		a.event(corev1.EventTypeWarning, conditions.ReasonSyncFailed,
			"Sync on %s copied files but scan failed: %s", a.Config.GatewayName, errorMsg)
		return fmt.Errorf("scan after sync: %s", errorMsg)
	}

	a.Metrics.SyncTotal.WithLabelValues(profileName, "success").Inc()
	a.Metrics.LastSyncTimestamp.Set(float64(time.Now().Unix()))
	a.Metrics.LastSyncSuccess.Set(1)

	a.event(corev1.EventTypeNormal, conditions.ReasonSyncCompleted,
		"Sync completed on %s: commit %s, %d file(s) changed", a.Config.GatewayName, commit[:min(12, len(commit))], filesChanged)

	a.lastSyncedCommit = commit
	a.lastSyncedProfiles = profiles
	return nil
}

// syncWithProfile looks up the resolved profile from the metadata ConfigMap,
// builds a plan, and executes it.
func (a *Agent) syncWithProfile(ctx context.Context) (*syncengine.SyncResult, string, bool, error) {
	log := logf.FromContext(ctx).WithName("profile-sync")

	// Read metadata ConfigMap (contains profiles JSON + git info).
	meta, err := ReadMetadataConfigMap(ctx, a.K8sClient, a.Config.CRNamespace, a.Config.CRName)
	if err != nil {
		return nil, "", false, fmt.Errorf("reading metadata: %w", err)
	}

	// Look up resolved profile.
	profile, profileName, err := a.lookupProfile(meta)
	if err != nil {
		return nil, "", false, err
	}

	log.V(1).Info("using profile", "name", profileName)

	// Check if profile is paused.
	if profile.Paused {
		log.Info("profile is paused, returning zero-change result")
		return &syncengine.SyncResult{}, profileName, profile.DryRun, nil
	}

	// Read pod labels for template context.
	var pod corev1.Pod
	if err := a.K8sClient.Get(ctx, client.ObjectKey{Name: a.Config.PodName, Namespace: a.Config.PodNamespace}, &pod); err != nil {
		if isForbidden(err) {
			log.Error(err, "RBAC permission denied — agent cannot read pod labels",
				"pod", a.Config.PodName, "namespace", a.Config.PodNamespace)
		}
		return nil, profileName, profile.DryRun, fmt.Errorf("reading pod labels: %w", err)
	}

	// Build template context.
	tmplCtx := buildTemplateContext(a.Config, meta, profile.Vars, pod.Labels)

	// Build sync plan (no crExcludes — controller already merged excludes into profile).
	plan, err := buildSyncPlan(profile, tmplCtx, a.Config.RepoPath, a.Config.DataPath)
	if err != nil {
		return nil, profileName, profile.DryRun, fmt.Errorf("building sync plan: %w", err)
	}

	// Add engine-level excludes to the plan.
	plan.ExcludePatterns = append(plan.ExcludePatterns, a.SyncEngine.ExcludePatterns...)

	log.V(1).Info("executing sync plan",
		"mappings", len(plan.Mappings),
		"dryRun", plan.DryRun,
		"excludes", len(plan.ExcludePatterns),
	)

	// Execute the plan.
	result, err := a.SyncEngine.ExecutePlan(plan)
	if err != nil {
		return nil, profileName, profile.DryRun, fmt.Errorf("executing plan: %w", err)
	}

	return result, profileName, profile.DryRun, nil
}

// reportError writes an error status to the status ConfigMap.
func (a *Agent) reportError(ctx context.Context, commit, ref, errMsg string) {
	a.event(corev1.EventTypeWarning, conditions.ReasonSyncFailed, "%s", errMsg)
	status := &stokertypes.GatewayStatus{
		SyncStatus:   stokertypes.SyncStatusError,
		SyncedCommit: commit,
		SyncedRef:    ref,
		LastSyncTime: time.Now().UTC().Format(time.RFC3339),
		AgentVersion: agentVersion,
		ErrorMessage: errMsg,
	}
	_ = WriteStatusConfigMap(ctx, a.K8sClient, a.Config.CRNamespace, a.Config.CRName, a.Config.GatewayName, status)
}

// event emits a K8s event on the cached GatewaySync CR. No-op if recorder or
// crRef is nil (e.g. during tests or when recorder setup failed).
func (a *Agent) event(eventType, reason, msgFmt string, args ...any) {
	if a.Recorder == nil || a.crRef == nil {
		return
	}
	a.Recorder.Eventf(a.crRef, eventType, reason, msgFmt, args...)
}

// fetchCRRef fetches the GatewaySync CR once using unstructured client and caches
// it as the event target. This avoids importing the CRD types package.
func (a *Agent) fetchCRRef(ctx context.Context) {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(gatewaySyncGVK)
	key := client.ObjectKey{Namespace: a.Config.CRNamespace, Name: a.Config.CRName}
	if err := a.K8sClient.Get(ctx, key, u); err != nil {
		if isForbidden(err) {
			logf.FromContext(ctx).Error(err, "RBAC permission denied — agent cannot read GatewaySync CR for event emission (non-fatal)")
		} else {
			logf.FromContext(ctx).Info("could not fetch GatewaySync for events (non-fatal)", "error", err)
		}
		return
	}
	a.crRef = u
}

// isForbidden checks if an error (possibly wrapped) is a Kubernetes 403 Forbidden.
func isForbidden(err error) bool {
	return apierrors.IsForbidden(err) || apierrors.ReasonForError(err) == metav1.StatusReasonForbidden
}
