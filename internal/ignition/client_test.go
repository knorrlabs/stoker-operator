package ignition

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// scanRecorder captures requests to the fake gateway so tests can assert on
// call order, paths, and auth headers.
type scanRecorder struct {
	mu       sync.Mutex
	paths    []string
	keys     []string
	statuses map[string]int // per-path response status; 0 means 200
}

func (r *scanRecorder) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		r.paths = append(r.paths, req.URL.Path)
		r.keys = append(r.keys, req.Header.Get("X-Ignition-API-Token"))
		status := r.statuses[req.URL.Path]
		r.mu.Unlock()
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
	}
}

func newTestClient(t *testing.T, rec *scanRecorder, key func() string) *Client {
	t.Helper()
	srv := httptest.NewServer(rec.handler())
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "http://")
	return NewClient("http", host, key)
}

// TestTriggerScanOrderAndAuth pins the scan contract Ignition requires:
// projects are scanned before config, and every call carries the API key.
// Out-of-order scans make the gateway load projects referencing config that
// hasn't been re-read yet.
func TestTriggerScanOrderAndAuth(t *testing.T) {
	rec := &scanRecorder{statuses: map[string]int{}}
	c := newTestClient(t, rec, func() string { return " test-key\n" })

	result := c.TriggerScan()
	if result.Error != "" {
		t.Fatalf("TriggerScan error: %s", result.Error)
	}
	if result.ProjectsStatus != 200 || result.ConfigStatus != 200 {
		t.Errorf("statuses = %d/%d, want 200/200", result.ProjectsStatus, result.ConfigStatus)
	}

	wantPaths := []string{"/data/api/v1/scan/projects", "/data/api/v1/scan/config"}
	if len(rec.paths) != 2 || rec.paths[0] != wantPaths[0] || rec.paths[1] != wantPaths[1] {
		t.Errorf("scan paths = %v, want %v (projects before config)", rec.paths, wantPaths)
	}
	for i, k := range rec.keys {
		if k != "test-key" {
			t.Errorf("request %d API key header = %q, want trimmed %q", i, k, "test-key")
		}
	}
}

// TestTriggerScanProjectsFailureSkipsConfig ensures a failed projects scan
// short-circuits: scanning config after a failed projects scan would apply
// half the new state.
func TestTriggerScanProjectsFailureSkipsConfig(t *testing.T) {
	rec := &scanRecorder{statuses: map[string]int{
		"/data/api/v1/scan/projects": http.StatusInternalServerError,
	}}
	c := newTestClient(t, rec, nil)

	result := c.TriggerScan()
	if result.Error == "" {
		t.Fatal("expected error when projects scan fails")
	}
	for _, p := range rec.paths {
		if p == "/data/api/v1/scan/config" {
			t.Error("config scan must not run after projects scan failed")
		}
	}
}

// TestPortCheck pins the post-commission readiness semantics: any HTTP
// response except 503 means the gateway is up, even an auth rejection,
// because commissioning may have reset the API token's access.
func TestPortCheck(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		wantErr bool
	}{
		{"unauthorized still counts as up", http.StatusUnauthorized, false},
		{"ok", http.StatusOK, false},
		{"still starting", http.StatusServiceUnavailable, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &scanRecorder{statuses: map[string]int{"/system/gwinfo": tc.status}}
			c := newTestClient(t, rec, nil)
			err := c.PortCheck()
			if tc.wantErr && err == nil {
				t.Error("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

// TestSetAuthNilKeyFunc guards the nil-func contract added when the key
// became lazily resolved: a client built with no key must not panic and must
// not send an empty auth header.
func TestSetAuthNilKeyFunc(t *testing.T) {
	rec := &scanRecorder{statuses: map[string]int{}}
	c := newTestClient(t, rec, nil)

	if err := c.HealthCheck(); err != nil {
		t.Fatalf("HealthCheck: %v", err)
	}
	if len(rec.keys) != 1 || rec.keys[0] != "" {
		t.Errorf("auth header sent without a key func: %v", rec.keys)
	}
}

// TestGetDesignerSessions covers the envelope decode and the empty-items
// normalization the wait policy relies on (nil items would make len() checks
// ambiguous against a decode failure).
func TestGetDesignerSessions(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[{"id":"1","user":"alice","project":"demo"}]}`))
	}))
	t.Cleanup(srv.Close)
	c := NewClient("http", strings.TrimPrefix(srv.URL, "http://"), nil)

	sessions, err := c.GetDesignerSessions(context.Background())
	if err != nil {
		t.Fatalf("GetDesignerSessions: %v", err)
	}
	if len(sessions) != 1 || sessions[0].User != "alice" || sessions[0].Project != "demo" {
		t.Errorf("sessions = %+v, want one alice/demo session", sessions)
	}
}

func TestGetDesignerSessionsEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	c := NewClient("http", strings.TrimPrefix(srv.URL, "http://"), nil)

	sessions, err := c.GetDesignerSessions(context.Background())
	if err != nil {
		t.Fatalf("GetDesignerSessions: %v", err)
	}
	if sessions == nil {
		t.Error("sessions must be non-nil empty slice when items are absent")
	}
	if len(sessions) != 0 {
		t.Errorf("sessions = %+v, want empty", sessions)
	}
}

func TestGetDesignerSessionsHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)
	c := NewClient("http", strings.TrimPrefix(srv.URL, "http://"), nil)

	if _, err := c.GetDesignerSessions(context.Background()); err == nil {
		t.Error("expected error on HTTP 403")
	}
}
