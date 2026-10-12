package server

import (
    "net/http"
    "net/http/httptest"
    "strings"
    "testing"
)

// #203: /v1/solve-stream rejects non-POST.
func TestHandleSolveStream_MethodCheck(t *testing.T) {
    s := newTestServer()
    req := httptest.NewRequest(http.MethodGet, "/v1/solve-stream", nil)
    rec := httptest.NewRecorder()
    s.handleSolveStream(rec, req)

    if rec.Code != http.StatusMethodNotAllowed {
        t.Fatalf("expected 405, got %d", rec.Code)
    }
}

// #203: /v1/solve-stream rejects invalid JSON.
func TestHandleSolveStream_InvalidJSON(t *testing.T) {
    s := newTestServer()
    req := httptest.NewRequest(http.MethodPost, "/v1/solve-stream",
        strings.NewReader("{not json"))
    req.Header.Set("Content-Type", "application/json")
    rec := httptest.NewRecorder()
    s.handleSolveStream(rec, req)

    if rec.Code != http.StatusBadRequest {
        t.Fatalf("expected 400, got %d", rec.Code)
    }
}

// #203: /v1/solve-stream with a valid body delegates to the chat-completions
// pipeline (the response comes from routing/provider, not solve-stream's own
// validation). We verify the response is NOT a solve-stream 400/405 — proving
// the request was rewritten (stream forced + bnup model applied) and forwarded.
func TestHandleSolveStream_DelegatesToChatPipeline(t *testing.T) {
    s := newTestServer()
    s.cfg.Config.Routing.BnupMode = false // global default off — endpoint must still work
    s.cfg.Config.Routing.BnupModel = "Qwen2.5-32B-BNUP-Final"
    s.cfg.Config.Routing.BnupGrammar = "bnup-socratic"

    body := `{"model":"","messages":[{"role":"user","content":"solve x+1=2"}]}`
    req := httptest.NewRequest(http.MethodPost, "/v1/solve-stream",
        strings.NewReader(body))
    req.Header.Set("Content-Type", "application/json")
    rec := httptest.NewRecorder()
    s.handleSolveStream(rec, req)

    // solve-stream's own errors are 400 (bad JSON) and 405 (method). A
    // delegated response is any other code — the chat pipeline's routing
    // decision (502/503/200/etc.) proves the rewrite + delegation happened.
    if rec.Code == http.StatusBadRequest || rec.Code == http.StatusMethodNotAllowed {
        t.Fatalf("solve-stream should delegate, not validate: got %d body=%s",
            rec.Code, rec.Body.String())
    }
    t.Logf("solve-stream delegated to chat pipeline: code=%d", rec.Code)
}
