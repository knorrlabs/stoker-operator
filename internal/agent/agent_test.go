package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/ia-eknorr/stoker-operator/internal/ignition"
	stokertypes "github.com/ia-eknorr/stoker-operator/pkg/types"
)

// newTestAgent wires an Agent against a fake K8s client and a stub gateway
// that reports the given designer sessions JSON.
func newTestAgent(t *testing.T, designersJSON string) (*Agent, client.Client) {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	k8s := fake.NewClientBuilder().WithScheme(s).Build()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/data/api/v1/designers" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(designersJSON))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	cfg := &Config{
		CRName:      "test-cr",
		CRNamespace: "test-ns",
		GatewayName: "gw-0",
		PodName:     "gw-0",
	}
	a := &Agent{
		Config:      cfg,
		K8sClient:   k8s,
		IgnitionAPI: ignition.NewClient("http", strings.TrimPrefix(srv.URL, "http://"), nil),
		Metrics:     NewAgentMetrics(),
	}
	return a, k8s
}

// TestCheckDesignerSessionsPolicies pins the policy matrix that decides
// whether a sync proceeds while users have Designer sessions open. A wrong
// answer either clobbers in-flight design work (should-block treated as
// proceed) or wedges deployments (should-proceed treated as block).
func TestCheckDesignerSessionsPolicies(t *testing.T) {
	oneSession := `{"items":[{"id":"1","user":"alice","project":"demo"}]}`
	cases := []struct {
		name      string
		policy    string
		designers string
		wantBlock bool
	}{
		{"proceed with active sessions", "proceed", oneSession, false},
		{"fail with active sessions", "fail", oneSession, true},
		{"empty policy defaults to proceed", "", oneSession, false},
		{"unknown policy proceeds", "yolo", oneSession, false},
		{"fail with no sessions", "fail", `{"items":[]}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, _ := newTestAgent(t, tc.designers)
			blocked := a.checkDesignerSessions(context.Background(), tc.policy, "abc123", "main")
			if blocked != tc.wantBlock {
				t.Errorf("blocked = %v, want %v", blocked, tc.wantBlock)
			}
		})
	}
}

// TestCheckDesignerSessionsFailWritesErrorStatus ensures a policy=fail block
// is surfaced to the controller via the status ConfigMap, not just logged —
// that's the only signal an operator sees in kubectl.
func TestCheckDesignerSessionsFailWritesErrorStatus(t *testing.T) {
	a, k8s := newTestAgent(t, `{"items":[{"id":"1","user":"alice","project":"demo"}]}`)

	if blocked := a.checkDesignerSessions(context.Background(), "fail", "abc123", "main"); !blocked {
		t.Fatal("policy=fail with active sessions must block")
	}

	cm := &corev1.ConfigMap{}
	key := client.ObjectKey{Name: StatusConfigMapName("test-cr"), Namespace: "test-ns"}
	if err := k8s.Get(context.Background(), key, cm); err != nil {
		t.Fatalf("status ConfigMap not written: %v", err)
	}
	status := cm.Data["gw-0"]
	if !strings.Contains(status, stokertypes.SyncStatusError) || !strings.Contains(status, "alice") {
		t.Errorf("status entry missing error/session info: %s", status)
	}
}

// TestCheckDesignerSessionsAPIErrorProceeds pins the fail-open choice: if the
// designer API is unreachable the sync continues, because blocking config
// delivery on a flaky introspection endpoint is worse than a rare sync during
// a session.
func TestCheckDesignerSessionsAPIErrorProceeds(t *testing.T) {
	a, _ := newTestAgent(t, "")
	// Point the client at a closed port to force a connection error.
	a.IgnitionAPI = ignition.NewClient("http", "127.0.0.1:1", nil)

	if blocked := a.checkDesignerSessions(context.Background(), "fail", "abc123", "main"); blocked {
		t.Error("designer API error must not block the sync")
	}
}
