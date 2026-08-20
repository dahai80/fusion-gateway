package server

import (
    "crypto/hmac"
    "crypto/sha256"
    "encoding/hex"
    "encoding/json"
    "io"
    "log/slog"
    "net/http"
    "strings"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

// maxWebhookBody caps an inbound webhook payload. model-hub events are small
// metadata envelopes ({"event","data"}); 1 MiB mirrors the SSE linebuf cap and
// the adapter-index decode cap, bounding allocation on a malformed/malicious
// POST without rejecting legitimate events.
const maxWebhookBody = 1 * 1024 * 1024

// webhookEventEnvelope is the payload shape model-hub's dispatcher sends:
// {"event": "<type>", "data": {...}}. Only the event discriminator is consumed
// here; data is logged at debug level for traceability but not parsed further
// (the receiver's job is to trigger a refresh, not to interpret the payload).
type webhookEventEnvelope struct {
    Event string          `json:"event"`
    Data  json.RawMessage `json:"data"`
}

// adapterIndexRefresher refreshes the LoRA AdapterIndex. Injected by main.go
// (the index lives in the run() scope); nil when no fusion-mlx backend is
// configured, in which case adapter.* events are acknowledged but no refresh
// runs (the index is already disabled, so there is nothing to refresh).
type adapterIndexRefresher interface {
    Refresh() error
}

// handleModelHubWebhook receives signed lifecycle events from fusion-model-hub
// (POST /webhooks/model-hub). It verifies the HMAC-SHA256 signature over the
// raw body, then on an adapter.* event triggers an immediate AdapterIndex
// refresh so newly published LoRA adapters are picked up without waiting for
// the 60s poll. Non-adapter events are acknowledged (200) and logged but do
// not trigger a refresh. Disabled when routing.webhooks.model_hub.enabled is
// false — the route is only registered when enabled (see server.go Start).
func (s *Server) handleModelHubWebhook(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        http.Error(w, `{"error":{"message":"Method not allowed","type":"invalid_request"}}`, http.StatusMethodNotAllowed)
        return
    }

    cfg := s.cfg.Config.Routing.Webhooks.ModelHub
    if !cfg.Enabled {
        slog.Warn("model-hub webhook received but receiver disabled", "path", r.URL.Path, "remote", r.RemoteAddr)
        http.Error(w, `{"error":{"message":"webhook receiver disabled","type":"disabled"}}`, http.StatusServiceUnavailable)
        return
    }

    body, err := io.ReadAll(io.LimitReader(r.Body, maxWebhookBody))
    if err != nil {
        slog.Warn("model-hub webhook: read body failed", "error", err, "remote", r.RemoteAddr)
        http.Error(w, `{"error":{"message":"Failed to read body","type":"invalid_request"}}`, http.StatusBadRequest)
        return
    }
    defer r.Body.Close()

    if !verifyWebhookSignature(body, r.Header.Get("X-Webhook-Signature"), cfg.Secret) {
        slog.Warn("model-hub webhook: signature verification failed", "remote", r.RemoteAddr, "event_header", r.Header.Get("X-Webhook-Event"))
        http.Error(w, `{"error":{"message":"Invalid signature","type":"auth_error"}}`, http.StatusUnauthorized)
        return
    }

    var env webhookEventEnvelope
    if err := json.Unmarshal(body, &env); err != nil {
        slog.Warn("model-hub webhook: decode envelope failed", "error", err, "bytes", len(body))
        http.Error(w, `{"error":{"message":"Invalid JSON","type":"invalid_request"}}`, http.StatusBadRequest)
        return
    }

    slog.Info("model-hub webhook received", "event", env.Event, "bytes", len(body))

    // adapter.* events imply the set of available LoRA adapters changed →
    // refresh the index. Other events (model.created, version.published, ...)
    // are acknowledged but do not affect the adapter index.
    if strings.HasPrefix(env.Event, "adapter.") {
        if s.adapterIndexRefresher == nil {
            slog.Info("model-hub webhook: adapter event but no index refresher wired (fusion-mlx backend not configured), acknowledging", "event", env.Event)
        } else if err := s.adapterIndexRefresher.Refresh(); err != nil {
            slog.Warn("model-hub webhook: adapter index refresh failed", "event", env.Event, "error", err)
        } else {
            slog.Info("model-hub webhook: adapter index refreshed on event", "event", env.Event)
        }
    }

    w.WriteHeader(http.StatusOK)
    w.Write([]byte(`{"status":"accepted","event":"` + env.Event + `"}`))
}

// verifyWebhookSignature re-computes HMAC-SHA256 over the raw body with the
// shared secret and compares it to the hex-encoded X-Webhook-Signature header
// in constant time. model-hub's _sign_payload uses HMAC-SHA256 + hex
// (sha256.hexdigest). An empty or malformed signature header fails closed.
func verifyWebhookSignature(body []byte, sigHeader, secret string) bool {
    if secret == "" || sigHeader == "" {
        return false
    }
    mac := hmac.New(sha256.New, []byte(secret))
    mac.Write(body)
    expected := hex.EncodeToString(mac.Sum(nil))
    return hmac.Equal([]byte(expected), []byte(sigHeader))
}

// modelHubWebhookEnabled reports whether the receiver should be registered.
func modelHubWebhookEnabled(snap *config.ConfigSnapshot) bool {
    if snap == nil {
        return false
    }
    return snap.Config.Routing.Webhooks.ModelHub.Enabled
}
