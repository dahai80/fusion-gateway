package server

import (
    "crypto/hmac"
    "crypto/sha256"
    "encoding/hex"
    "errors"
    "io"
    "log/slog"
    "net/http"
    "net/http/httptest"
    "strings"
    "testing"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

// stubRefresher records whether Refresh was called and can return an error.
type stubRefresher struct {
    called int
    err    error
}

func (s *stubRefresher) Refresh() error {
    s.called++
    return s.err
}

func init() {
    slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func signBody(body, secret string) string {
    mac := hmac.New(sha256.New, []byte(secret))
    mac.Write([]byte(body))
    return hex.EncodeToString(mac.Sum(nil))
}

// snapshotWithWebhook builds a ConfigSnapshot with the model-hub webhook
// receiver configured (enabled + secret) on top of DefaultConfig.
func snapshotWithWebhook(enabled bool, secret string) *config.ConfigSnapshot {
    cfg := config.DefaultConfig()
    cfg.Routing.Webhooks.ModelHub.Enabled = enabled
    cfg.Routing.Webhooks.ModelHub.Secret = secret
    return &config.ConfigSnapshot{Config: cfg}
}

func newWebhookServer(t *testing.T, enabled bool, secret string, refresher adapterIndexRefresher) *Server {
    t.Helper()
    s := &Server{
        cfg: snapshotWithWebhook(enabled, secret),
    }
    s.adapterIndexRefresher = refresher
    return s
}

func TestVerifyWebhookSignature_Valid(t *testing.T) {
    body := `{"event":"adapter.published","data":{}}`
    sig := signBody(body, "s3cr3t")
    if !verifyWebhookSignature([]byte(body), sig, "s3cr3t") {
        t.Fatal("valid signature rejected")
    }
}

func TestVerifyWebhookSignature_TamperedBody(t *testing.T) {
    sig := signBody(`{"event":"adapter.published"}`, "s3cr3t")
    if verifyWebhookSignature([]byte(`{"event":"adapter.deleted"}`), sig, "s3cr3t") {
        t.Fatal("tampered body accepted")
    }
}

func TestVerifyWebhookSignature_WrongSecret(t *testing.T) {
    body := `{"event":"adapter.published"}`
    sig := signBody(body, "s3cr3t")
    if verifyWebhookSignature([]byte(body), sig, "wrong") {
        t.Fatal("wrong secret accepted")
    }
}

func TestVerifyWebhookSignature_EmptyInputs(t *testing.T) {
    if verifyWebhookSignature([]byte("x"), "sig", "") {
        t.Fatal("empty secret accepted")
    }
    if verifyWebhookSignature([]byte("x"), "", "secret") {
        t.Fatal("empty signature accepted")
    }
}

func TestHandleModelHubWebhook_AdapterEventTriggersRefresh(t *testing.T) {
    ref := &stubRefresher{}
    s := newWebhookServer(t, true, "s3cr3t", ref)
    body := `{"event":"adapter.published","data":{"name":"lora-code"}}`
    req := httptest.NewRequest(http.MethodPost, "/webhooks/model-hub", strings.NewReader(body))
    req.Header.Set("X-Webhook-Signature", signBody(body, "s3cr3t"))
    req.Header.Set("X-Webhook-Event", "adapter.published")
    rec := httptest.NewRecorder()

    s.handleModelHubWebhook(rec, req)

    if rec.Code != http.StatusOK {
        t.Fatalf("want 200, got %d", rec.Code)
    }
    if ref.called != 1 {
        t.Fatalf("want 1 refresh call, got %d", ref.called)
    }
    if !strings.Contains(rec.Body.String(), "adapter.published") {
        t.Fatalf("response missing event echo: %s", rec.Body.String())
    }
}

func TestHandleModelHubWebhook_NonAdapterEventSkipsRefresh(t *testing.T) {
    ref := &stubRefresher{}
    s := newWebhookServer(t, true, "s3cr3t", ref)
    body := `{"event":"model.created","data":{}}`
    req := httptest.NewRequest(http.MethodPost, "/webhooks/model-hub", strings.NewReader(body))
    req.Header.Set("X-Webhook-Signature", signBody(body, "s3cr3t"))
    rec := httptest.NewRecorder()

    s.handleModelHubWebhook(rec, req)

    if rec.Code != http.StatusOK {
        t.Fatalf("want 200, got %d", rec.Code)
    }
    if ref.called != 0 {
        t.Fatalf("want 0 refresh calls for non-adapter event, got %d", ref.called)
    }
}

func TestHandleModelHubWebhook_BadSignature(t *testing.T) {
    ref := &stubRefresher{}
    s := newWebhookServer(t, true, "s3cr3t", ref)
    body := `{"event":"adapter.published"}`
    req := httptest.NewRequest(http.MethodPost, "/webhooks/model-hub", strings.NewReader(body))
    req.Header.Set("X-Webhook-Signature", "deadbeef")
    rec := httptest.NewRecorder()

    s.handleModelHubWebhook(rec, req)

    if rec.Code != http.StatusUnauthorized {
        t.Fatalf("want 401, got %d", rec.Code)
    }
    if ref.called != 0 {
        t.Fatalf("refresh should not run on bad signature, got %d calls", ref.called)
    }
}

func TestHandleModelHubWebhook_RefreshErrorStill200(t *testing.T) {
    ref := &stubRefresher{err: errors.New("boom")}
    s := newWebhookServer(t, true, "s3cr3t", ref)
    body := `{"event":"adapter.merged"}`
    req := httptest.NewRequest(http.MethodPost, "/webhooks/model-hub", strings.NewReader(body))
    req.Header.Set("X-Webhook-Signature", signBody(body, "s3cr3t"))
    rec := httptest.NewRecorder()

    s.handleModelHubWebhook(rec, req)

    // Refresh failed but the event was validly signed — acknowledge so the
    // sender does not retry-storm; the failure is logged server-side.
    if rec.Code != http.StatusOK {
        t.Fatalf("want 200 despite refresh error, got %d", rec.Code)
    }
    if ref.called != 1 {
        t.Fatalf("want 1 refresh attempt, got %d", ref.called)
    }
}

func TestHandleModelHubWebhook_Disabled(t *testing.T) {
    ref := &stubRefresher{}
    s := newWebhookServer(t, false, "", ref)
    body := `{"event":"adapter.published"}`
    req := httptest.NewRequest(http.MethodPost, "/webhooks/model-hub", strings.NewReader(body))
    rec := httptest.NewRecorder()

    s.handleModelHubWebhook(rec, req)

    if rec.Code != http.StatusServiceUnavailable {
        t.Fatalf("want 503 when disabled, got %d", rec.Code)
    }
}

func TestHandleModelHubWebhook_AdapterEventNilRefresher(t *testing.T) {
    s := newWebhookServer(t, true, "s3cr3t", nil)
    body := `{"event":"adapter.published"}`
    req := httptest.NewRequest(http.MethodPost, "/webhooks/model-hub", strings.NewReader(body))
    req.Header.Set("X-Webhook-Signature", signBody(body, "s3cr3t"))
    rec := httptest.NewRecorder()

    s.handleModelHubWebhook(rec, req)

    if rec.Code != http.StatusOK {
        t.Fatalf("want 200 with nil refresher, got %d", rec.Code)
    }
}

func TestHandleModelHubWebhook_WrongMethod(t *testing.T) {
    s := newWebhookServer(t, true, "s3cr3t", &stubRefresher{})
    req := httptest.NewRequest(http.MethodGet, "/webhooks/model-hub", nil)
    rec := httptest.NewRecorder()

    s.handleModelHubWebhook(rec, req)

    if rec.Code != http.StatusMethodNotAllowed {
        t.Fatalf("want 405, got %d", rec.Code)
    }
}

func TestHandleModelHubWebhook_InvalidJSON(t *testing.T) {
    s := newWebhookServer(t, true, "s3cr3t", &stubRefresher{})
    body := `not json`
    req := httptest.NewRequest(http.MethodPost, "/webhooks/model-hub", strings.NewReader(body))
    req.Header.Set("X-Webhook-Signature", signBody(body, "s3cr3t"))
    rec := httptest.NewRecorder()

    s.handleModelHubWebhook(rec, req)

    if rec.Code != http.StatusBadRequest {
        t.Fatalf("want 400, got %d", rec.Code)
    }
}
