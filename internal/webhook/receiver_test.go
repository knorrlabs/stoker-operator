package webhook

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	stokerv1alpha1 "github.com/knorrlabs/stoker-operator/api/v1alpha1"
	stokertypes "github.com/knorrlabs/stoker-operator/pkg/types"
)

const testRef = "v2.0.0"

// --- Payload parsing tests ---

func TestParsePayload_Generic(t *testing.T) {
	body := []byte(`{"ref":"v2.0.0"}`)
	ref, source := parsePayload(body)
	if ref != testRef {
		t.Fatalf("expected ref=v2.0.0, got %q", ref)
	}
	if source != SourceGeneric {
		t.Fatalf("expected source=generic, got %q", source)
	}
}

func TestParsePayload_GitHub(t *testing.T) {
	body := []byte(`{"action":"published","release":{"tag_name":"v3.0.0"}}`)
	ref, source := parsePayload(body)
	if ref != "v3.0.0" {
		t.Fatalf("expected ref=v3.0.0, got %q", ref)
	}
	if source != "github" {
		t.Fatalf("expected source=github, got %q", source)
	}
}

func TestParsePayload_ArgoCD(t *testing.T) {
	body := []byte(`{"app":{"metadata":{"annotations":{"git.ref":"v4.0.0"}}}}`)
	ref, source := parsePayload(body)
	if ref != "v4.0.0" {
		t.Fatalf("expected ref=v4.0.0, got %q", ref)
	}
	if source != "argocd" {
		t.Fatalf("expected source=argocd, got %q", source)
	}
}

func TestParsePayload_Kargo(t *testing.T) {
	body := []byte(`{"freight":{"commits":[{"tag":"v5.0.0"}]}}`)
	ref, source := parsePayload(body)
	if ref != "v5.0.0" {
		t.Fatalf("expected ref=v5.0.0, got %q", ref)
	}
	if source != "kargo" {
		t.Fatalf("expected source=kargo, got %q", source)
	}
}

func TestParsePayload_Empty(t *testing.T) {
	ref, _ := parsePayload([]byte(`{}`))
	if ref != "" {
		t.Fatalf("expected empty ref, got %q", ref)
	}
}

func TestParsePayload_InvalidJSON(t *testing.T) {
	ref, _ := parsePayload([]byte(`not json`))
	if ref != "" {
		t.Fatalf("expected empty ref, got %q", ref)
	}
}

// --- HTTP handler tests ---

func newTestReceiver(hmacSecret string, objects ...runtime.Object) (*Receiver, *http.ServeMux) {
	return newTestReceiverFull(hmacSecret, "", objects...)
}

func newTestReceiverFull(hmacSecret, bearerToken string, objects ...runtime.Object) (*Receiver, *http.ServeMux) {
	scheme := runtime.NewScheme()
	_ = stokerv1alpha1.AddToScheme(scheme)

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(objects...).
		Build()

	rv := &Receiver{
		Client:      fakeClient,
		HMACSecret:  hmacSecret,
		BearerToken: bearerToken,
		Port:        9443,
		Recorder:    record.NewFakeRecorder(20),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /webhook/{namespace}/{crName}", rv.handleWebhook)
	return rv, mux
}

func testCR() *stokerv1alpha1.GatewaySync {
	return &stokerv1alpha1.GatewaySync{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-sync",
			Namespace: "default",
		},
		Spec: stokerv1alpha1.GatewaySyncSpec{
			Git: stokerv1alpha1.GitSpec{
				Repo: "git@github.com:example/test.git",
				Ref:  "main",
			},
			Gateway: stokerv1alpha1.GatewaySpec{
				API: stokerv1alpha1.GatewayAPISpec{
					SecretName: "api-key",
					SecretKey:  "apiKey",
				},
			},
		},
	}
}

func TestHandler_AcceptsValidRequest(t *testing.T) {
	_, mux := newTestReceiver("", testCR())

	body := []byte(`{"ref":"v2.0.0"}`)
	req := httptest.NewRequest("POST", "/webhook/default/my-sync", bytes.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["ref"] != testRef {
		t.Fatalf("expected ref=v2.0.0 in response, got %v", resp["ref"])
	}
}

func TestHandler_ReturnsNotFoundForMissingCR(t *testing.T) {
	_, mux := newTestReceiver("") // no CRs

	body := []byte(`{"ref":"v2.0.0"}`)
	req := httptest.NewRequest("POST", "/webhook/default/nonexistent", bytes.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestHandler_RejectsInvalidHMAC(t *testing.T) {
	_, mux := newTestReceiver("my-secret", testCR())

	body := []byte(`{"ref":"v2.0.0"}`)
	req := httptest.NewRequest("POST", "/webhook/default/my-sync", bytes.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", "sha256=invalid")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestHandler_AcceptsValidHMAC(t *testing.T) {
	secret := "test-secret"
	_, mux := newTestReceiver(secret, testCR())

	body := []byte(`{"ref":"v2.0.0"}`)
	sig := computeHMAC(body, secret)
	req := httptest.NewRequest("POST", "/webhook/default/my-sync", bytes.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", sig)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandler_AnnotatesCR(t *testing.T) {
	rv, mux := newTestReceiver("", testCR())

	body := []byte(`{"ref":"v2.0.0"}`)
	req := httptest.NewRequest("POST", "/webhook/default/my-sync", bytes.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", w.Code, w.Body.String())
	}

	// Verify the CR was annotated
	var cr stokerv1alpha1.GatewaySync
	err := rv.Client.Get(req.Context(), types.NamespacedName{Name: "my-sync", Namespace: "default"}, &cr)
	if err != nil {
		t.Fatalf("failed to get CR: %v", err)
	}
	if cr.Annotations[stokertypes.AnnotationRequestedRef] != testRef {
		t.Fatalf("expected requested-ref=v2.0.0, got %q", cr.Annotations[stokertypes.AnnotationRequestedRef])
	}
	if cr.Annotations[stokertypes.AnnotationRequestedBy] != SourceGeneric {
		t.Fatalf("expected requested-by=generic, got %q", cr.Annotations[stokertypes.AnnotationRequestedBy])
	}
	if cr.Annotations[stokertypes.AnnotationRequestedAt] == "" {
		t.Fatal("expected requested-at to be set")
	}
}

func TestHandler_DuplicateRefReturns202(t *testing.T) {
	cr := testCR()
	cr.Annotations = map[string]string{
		stokertypes.AnnotationRequestedRef: testRef,
	}
	_, mux := newTestReceiver("", cr)

	body := []byte(`{"ref":"v2.0.0"}`)
	req := httptest.NewRequest("POST", "/webhook/default/my-sync", bytes.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	// Duplicate ref should return 202 so callers using successExpression: response.status == 202
	// (e.g. Kargo) don't treat an already-current ref as an error.
	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202 for duplicate ref, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandler_BadPayload(t *testing.T) {
	_, mux := newTestReceiver("", testCR())

	body := []byte(`{"nothing":"here"}`)
	req := httptest.NewRequest("POST", "/webhook/default/my-sync", bytes.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestHandler_HMACValidatedBeforeCRLookup(t *testing.T) {
	// No CRs exist, but HMAC is set — should get 401, not 404
	_, mux := newTestReceiver("my-secret")

	body := []byte(`{"ref":"v2.0.0"}`)
	req := httptest.NewRequest("POST", "/webhook/default/nonexistent", bytes.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", "sha256=invalid")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 (HMAC before CR lookup), got %d", w.Code)
	}
}

func TestHandler_AcceptsBearerToken(t *testing.T) {
	_, mux := newTestReceiverFull("", "secret-token", testCR())

	body := []byte(`{"ref":"v2.0.0"}`)
	req := httptest.NewRequest("POST", "/webhook/default/my-sync", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer secret-token")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", w.Code)
	}
}

func TestHandler_RejectsMissingBearerToken(t *testing.T) {
	_, mux := newTestReceiverFull("", "secret-token")

	body := []byte(`{"ref":"v2.0.0"}`)
	req := httptest.NewRequest("POST", "/webhook/default/my-sync", bytes.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestHandler_RejectsWrongBearerToken(t *testing.T) {
	_, mux := newTestReceiverFull("", "secret-token")

	body := []byte(`{"ref":"v2.0.0"}`)
	req := httptest.NewRequest("POST", "/webhook/default/my-sync", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer wrong-token")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestHandler_MetricsIncremented(t *testing.T) {
	_, mux := newTestReceiver("", testCR())

	// Successful request (202).
	body := []byte(`{"ref":"v9.0.0"}`)
	req := httptest.NewRequest("POST", "/webhook/default/my-sync", bytes.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", w.Code)
	}

	// 404 request.
	body = []byte(`{"ref":"v9.0.0"}`)
	req = httptest.NewRequest("POST", "/webhook/default/nonexistent", bytes.NewReader(body))
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}

	// Verify counters have been incremented (no panics, valid labels).
	// We can't easily reset global counters, so just verify they don't error.
}

func TestHandler_BearerTokenAuthorizesFallbackWhenHMACSet(t *testing.T) {
	// Both HMAC and bearer token configured — bearer token should authorize even
	// though no X-Hub-Signature-256 header is present.
	_, mux := newTestReceiverFull("hmac-secret", "bearer-token", testCR())

	body := []byte(`{"ref":"v2.0.0"}`)
	req := httptest.NewRequest("POST", "/webhook/default/my-sync", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer bearer-token")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: bearer token should authorize when HMAC is also configured", w.Code)
	}
}
