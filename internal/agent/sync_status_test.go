package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/ia-eknorr/stoker-operator/internal/ignition"
	"github.com/ia-eknorr/stoker-operator/internal/syncengine"
)

// TestSyncOnceScanFailureIsRetryable pins the recovery path for a gateway
// that accepts files but fails the scan call (e.g. mid-restart): syncOnce
// must return an error and must NOT record the commit as synced, so the
// caller's backoff retries instead of leaving the gateway in Error until the
// next commit. The second pass with a healthy gateway must then succeed.
func TestSyncOnceScanFailureIsRetryable(t *testing.T) {
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatalf("scheme: %v", err)
	}

	repoDir := t.TempDir()
	dataDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, "config"), 0755); err != nil {
		t.Fatalf("mkdir repo config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "config", "a.json"), []byte(`{}`), 0644); err != nil {
		t.Fatalf("write repo file: %v", err)
	}

	profilesJSON := `{"default":{"mappings":[{"source":"config","destination":"config"}],"syncPeriod":30,"designerSessionPolicy":"proceed"}}`
	meta := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: MetadataConfigMapName("test-cr"), Namespace: "test-ns"},
		Data: map[string]string{
			"commit": "abc123def456", "ref": "main", "gitURL": "https://example.invalid/r.git",
			"profiles": profilesJSON,
		},
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "gw-0", Namespace: "test-ns"}}
	k8s := fake.NewClientBuilder().WithScheme(s).WithObjects(meta, pod).Build()

	var scanHealthy atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if strings.HasPrefix(req.URL.Path, "/data/api/v1/scan/") && !scanHealthy.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	a := &Agent{
		Config: &Config{
			CRName: "test-cr", CRNamespace: "test-ns",
			PodName: "gw-0", PodNamespace: "test-ns", GatewayName: "gw-0",
			RepoPath: repoDir, DataPath: dataDir, ProfileName: "default",
		},
		K8sClient:    k8s,
		SyncEngine:   &syncengine.Engine{},
		IgnitionAPI:  &ignition.Client{BaseURL: srv.URL, HTTPClient: &http.Client{Timeout: 5 * time.Second}},
		HealthServer: NewHealthServer(":0"),
		Metrics:      NewAgentMetrics(),
	}

	err := a.syncOnce(context.Background(), "abc123def456", "main", false, profilesJSON)
	if err == nil {
		t.Fatal("syncOnce must return an error when the scan fails")
	}
	if a.lastSyncedCommit != "" {
		t.Errorf("lastSyncedCommit = %q after failed scan, want empty so the sync retries", a.lastSyncedCommit)
	}

	statusCM := &corev1.ConfigMap{}
	key := client.ObjectKey{Name: StatusConfigMapName("test-cr"), Namespace: "test-ns"}
	if err := k8s.Get(context.Background(), key, statusCM); err != nil {
		t.Fatalf("status ConfigMap: %v", err)
	}
	if !strings.Contains(statusCM.Data["gw-0"], "Error") {
		t.Errorf("status after failed scan = %s, want Error", statusCM.Data["gw-0"])
	}

	scanHealthy.Store(true)
	if err := a.syncOnce(context.Background(), "abc123def456", "main", false, profilesJSON); err != nil {
		t.Fatalf("syncOnce with healthy scan: %v", err)
	}
	if a.lastSyncedCommit != "abc123def456" {
		t.Errorf("lastSyncedCommit = %q after recovery, want abc123def456", a.lastSyncedCommit)
	}
	if err := k8s.Get(context.Background(), key, statusCM); err != nil {
		t.Fatalf("status ConfigMap: %v", err)
	}
	if !strings.Contains(statusCM.Data["gw-0"], "Synced") {
		t.Errorf("status after recovery = %s, want Synced", statusCM.Data["gw-0"])
	}

	if _, err := os.Stat(filepath.Join(dataDir, "config", "a.json")); err != nil {
		t.Errorf("synced file missing from live dir: %v", err)
	}
}
