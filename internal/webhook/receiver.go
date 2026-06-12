package webhook

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	stokerv1alpha1 "github.com/ia-eknorr/stoker-operator/api/v1alpha1"
	stokertypes "github.com/ia-eknorr/stoker-operator/pkg/types"
)

const (
	maxPayloadBytes = 1 << 20 // 1 MiB

	// SourceGeneric is the source identifier for generic webhook payloads.
	SourceGeneric = "generic"
)

// Receiver is an HTTP server that receives webhook payloads and annotates
// GatewaySync CRs with the requested ref. It implements manager.Runnable.
type Receiver struct {
	Client      client.Client
	HMACSecret  string
	BearerToken string
	Port        int32
	Recorder    record.EventRecorder
}

// NeedLeaderElection makes the receiver run on every replica, not just the
// leader. The receiver Service load-balances across all pods; if only the
// leader listened, webhooks routed to followers would be refused.
func (rv *Receiver) NeedLeaderElection() bool {
	return false
}

// Start starts the webhook HTTP server. Blocks until ctx is cancelled.
func (rv *Receiver) Start(ctx context.Context) error {
	log := logf.FromContext(ctx).WithName("webhook-receiver")

	mux := http.NewServeMux()
	mux.HandleFunc("POST /webhook/{namespace}/{crName}", rv.handleWebhook)

	addr := fmt.Sprintf(":%d", rv.Port)
	server := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(_ net.Listener) context.Context { return ctx },
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	log.Info("starting webhook receiver", "addr", addr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("webhook server error: %w", err)
	}
	return nil
}

func (rv *Receiver) handleWebhook(w http.ResponseWriter, r *http.Request) {
	log := logf.FromContext(r.Context()).WithName("webhook-receiver")

	namespace := r.PathValue("namespace")
	crName := r.PathValue("crName")

	// Read body (capped at 1 MiB)
	body, err := io.ReadAll(io.LimitReader(r.Body, maxPayloadBytes))
	if err != nil {
		log.Error(err, "failed to read request body")
		webhookReceiverRequestsTotal.WithLabelValues("unknown", "400").Inc()
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}

	// Validate auth BEFORE any CR lookup — prevents enumeration attacks.
	// If any auth method is configured, at least one must succeed.
	if !rv.authorize(r, body) {
		webhookReceiverRequestsTotal.WithLabelValues("unknown", "401").Inc()
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	// Parse ref from payload (auto-detect format)
	ref, source := parsePayload(body)
	if ref == "" {
		webhookReceiverRequestsTotal.WithLabelValues("unknown", "400").Inc()
		http.Error(w, `{"error":"no ref found in payload"}`, http.StatusBadRequest)
		return
	}

	// Look up the GatewaySync CR
	var gs stokerv1alpha1.GatewaySync
	key := types.NamespacedName{Name: crName, Namespace: namespace}
	if err := rv.Client.Get(r.Context(), key, &gs); err != nil {
		log.Error(err, "CR not found", "namespace", namespace, "name", crName)
		webhookReceiverRequestsTotal.WithLabelValues(source, "404").Inc()
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	// Check if the ref is already set (idempotent) — return 202 so callers
	// with successExpression: response.status == 202 don't treat this as an error.
	if gs.Annotations != nil && gs.Annotations[stokertypes.AnnotationRequestedRef] == ref {
		webhookReceiverRequestsTotal.WithLabelValues(source, "202").Inc()
		writeJSON(w, http.StatusAccepted, map[string]any{
			"accepted": true,
			"ref":      ref,
			"message":  "ref already set",
		})
		return
	}

	// Annotate the CR
	if gs.Annotations == nil {
		gs.Annotations = make(map[string]string)
	}
	gs.Annotations[stokertypes.AnnotationRequestedRef] = ref
	gs.Annotations[stokertypes.AnnotationRequestedAt] = time.Now().UTC().Format(time.RFC3339)
	gs.Annotations[stokertypes.AnnotationRequestedBy] = source

	if err := rv.Client.Update(r.Context(), &gs); err != nil {
		log.Error(err, "failed to annotate CR", "namespace", namespace, "name", crName)
		webhookReceiverRequestsTotal.WithLabelValues(source, "500").Inc()
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	rv.Recorder.Eventf(&gs, corev1.EventTypeNormal, "WebhookReceived", "Webhook from %s, ref %q", source, ref)
	log.Info("webhook accepted", "namespace", namespace, "cr", crName, "ref", ref, "source", source)
	webhookReceiverRequestsTotal.WithLabelValues(source, "202").Inc()
	writeJSON(w, http.StatusAccepted, map[string]any{
		"accepted": true,
		"ref":      ref,
	})
}

// authorize returns true if the request passes at least one configured auth
// method. If no auth is configured, all requests are allowed. Supports two
// methods — either can satisfy the check:
//   - HMAC: X-Hub-Signature-256 header validated against HMACSecret
//   - Bearer token: Authorization: Bearer <token> validated against BearerToken
func (rv *Receiver) authorize(r *http.Request, body []byte) bool {
	if rv.HMACSecret == "" && rv.BearerToken == "" {
		return true
	}
	if rv.HMACSecret != "" {
		if ValidateHMAC(body, r.Header.Get("X-Hub-Signature-256"), rv.HMACSecret) == nil {
			return true
		}
	}
	if rv.BearerToken != "" {
		// Compare SHA-256 digests in constant time: hashing first equalizes
		// length so neither content nor token length leaks via timing.
		got := sha256.Sum256([]byte(r.Header.Get("Authorization")))
		want := sha256.Sum256([]byte("Bearer " + rv.BearerToken))
		if subtle.ConstantTimeCompare(got[:], want[:]) == 1 {
			return true
		}
	}
	return false
}

// parsePayload auto-detects the payload format and extracts the ref.
// Returns (ref, source) where source identifies the detected format.
func parsePayload(body []byte) (string, string) {
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return "", ""
	}

	// 1. GitHub release: { "action": "published", "release": { "tag_name": "2.0.0" } }
	if release, ok := raw["release"].(map[string]any); ok {
		if tag, ok := release["tag_name"].(string); ok && tag != "" {
			return tag, "github"
		}
	}

	// 2. ArgoCD notification: { "app": { "metadata": { "annotations": { "git.ref": "2.0.0" } } } }
	if app, ok := raw["app"].(map[string]any); ok {
		if meta, ok := app["metadata"].(map[string]any); ok {
			if anns, ok := meta["annotations"].(map[string]any); ok {
				if ref, ok := anns["git.ref"].(string); ok && ref != "" {
					return ref, "argocd"
				}
			}
		}
	}

	// 3. Kargo promotion: { "freight": { "commits": [{ "tag": "2.0.0" }] } }
	if freight, ok := raw["freight"].(map[string]any); ok {
		if commits, ok := freight["commits"].([]any); ok && len(commits) > 0 {
			if commit, ok := commits[0].(map[string]any); ok {
				if tag, ok := commit["tag"].(string); ok && tag != "" {
					return tag, "kargo"
				}
			}
		}
	}

	// 4. Generic: { "ref": "2.0.0" }
	if ref, ok := raw["ref"].(string); ok && ref != "" {
		return ref, SourceGeneric
	}

	return "", ""
}

func writeJSON(w http.ResponseWriter, status int, data map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}
