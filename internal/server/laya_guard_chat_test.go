package server

import (
    "context"
    "net/http"
    "net/http/httptest"
    "strings"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/router"
)

// mockLayaGuardServer returns a httptest server that responds to
// /v1/laya/decide with router-preset + guard-preset JSON. The guard preset
// returns jailbreak=1.0 (above threshold) so the guard blocks.
func mockLayaGuardServer() *httptest.Server {
    routerResp := `{"answers":{"domain":{"type":"choice","choice":"code","confidence":0.95},"difficulty":{"type":"score","score":1.5,"confidence":0.9},"needs_tools":{"type":"noul","noul":0.1,"confidence":0.9},"is_sensitive":{"type":"noul","noul":0.0,"confidence":0.9}},"usage":{"input_tokens":128,"output_tokens":0},"latency_ms":7.0}`
    guardResp := `{"answers":{"jailbreak":{"type":"noul","noul":1.0,"confidence":0.9},"prompt_injection":{"type":"noul","noul":0.1,"confidence":0.9},"sensitive_data":{"type":"noul","noul":0.9,"confidence":0.9},"harm_severity":{"type":"score","score":2.5,"confidence":0.9}},"usage":{"input_tokens":128,"output_tokens":0},"latency_ms":7.0}`
    return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("Content-Type", "application/json")
        w.WriteHeader(http.StatusOK)
        if strings.Contains(r.URL.Path, "/v1/laya/decide") {
            buf := make([]byte, 512)
            n, _ := r.Body.Read(buf)
            reqBody := string(buf[:n])
            if strings.Contains(reqBody, `"guard"`) {
                _, _ = w.Write([]byte(guardResp))
                return
            }
            _, _ = w.Write([]byte(routerResp))
        }
    }))
}

// newLayaGuardTestServer builds a test Server with the intent classifier
// enabled and a laya classifier wired to the mock server. The config snapshot
// is attached to the returned request context (middleware normally does this).
func newLayaGuardTestServer(t *testing.T, layaURL string) (*Server, context.Context) {
    t.Helper()
    s := newTestServer()
    s.cfg.Config.Routing.IntentClassifier = config.IntentClassifierConfig{
        Enabled:                 true,
        Type:                    "laya",
        Endpoint:                layaURL,
        Timeout:                 500 * time.Millisecond,
        MinConfidence:           0.7,
        GuardSensitiveThreshold: 0.5,
        GuardJailbreakThreshold: 0.5,
        GuardInjectionThreshold: 0.5,
        GuardSensitiveDataThreshold: 0.5,
        GuardHarmSeverityThreshold:  2.0,
    }
    classifier := router.NewLayaClassifier(s.cfg.Config.Routing.IntentClassifier)
    s.router.SetIntentClassifier(classifier)
    ctx := config.WithSnapshot(context.Background(), s.cfg)
    return s, ctx
}

// TestChatCompletions_LayaGuard403 verifies /v1/chat/completions returns HTTP
// 403 when the laya guard blocks (issue #199/#20). Previously this path skipped
// decision.Rejected — only /v1/messages checked it — leaving the primary chat
// API unprotected.
func TestChatCompletions_LayaGuard403(t *testing.T) {
    layaSrv := mockLayaGuardServer()
    defer layaSrv.Close()

    s, ctx := newLayaGuardTestServer(t, layaSrv.URL)

    chatBody := `{"model":"gpt-4","messages":[{"role":"user","content":"my aws secret key is AKIAIOSFODNN7EXAMPLE"}],"stream":false}`
    req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(chatBody))
    req = req.WithContext(ctx)
    rec := httptest.NewRecorder()
    s.handleChatCompletions(rec, req)

    if rec.Code != http.StatusForbidden {
        t.Fatalf("expected 403 (laya guard blocked), got %d body=%s", rec.Code, rec.Body.String())
    }
    if !strings.Contains(rec.Body.String(), "jailbreak") {
        t.Fatalf("expected 403 body to contain dimension=jailbreak, got %s", rec.Body.String())
    }
    t.Logf("TestChatCompletions_LayaGuard403 passed: %d %s", rec.Code, rec.Body.String())
}

// TestCompletions_LayaGuard403 verifies /v1/completions (legacy) also returns
// HTTP 403 when the laya guard blocks — same gap fix.
func TestCompletions_LayaGuard403(t *testing.T) {
    layaSrv := mockLayaGuardServer()
    defer layaSrv.Close()

    s, ctx := newLayaGuardTestServer(t, layaSrv.URL)

    compBody := `{"model":"gpt-4","prompt":"my aws secret key is AKIAIOSFODNN7EXAMPLE","stream":false}`
    req := httptest.NewRequest(http.MethodPost, "/v1/completions", strings.NewReader(compBody))
    req = req.WithContext(ctx)
    rec := httptest.NewRecorder()
    s.handleCompletions(rec, req)

    if rec.Code != http.StatusForbidden {
        t.Fatalf("expected 403 (laya guard blocked), got %d body=%s", rec.Code, rec.Body.String())
    }
    if !strings.Contains(rec.Body.String(), "jailbreak") {
        t.Fatalf("expected 403 body to contain dimension=jailbreak, got %s", rec.Body.String())
    }
    t.Logf("TestCompletions_LayaGuard403 passed: %d %s", rec.Code, rec.Body.String())
}
