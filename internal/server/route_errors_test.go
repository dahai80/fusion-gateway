package server

import (
    "encoding/json"
    "errors"
    "net/http"
    "net/http/httptest"
    "strings"
    "testing"
    "time"
)

// #438: the embeddings 502 body must carry the upstream error detail (was a
// bare "Embedding failed") so a failure is attributable to gateway routing vs
// the fusion-mlx embeddings handler without log correlation.
func TestEmbeddings_FailureSurfacesUpstreamError(t *testing.T) {
    s := newTestServerWithProvider("test-cloud", &mockProvider{
        name:    "test-cloud",
        healthy: true,
        embErr:  errors.New("mlx embeddings handler timed out after 300s"),
    })
    s.cfg.Config.Routing.Mode = "cloud"

    req := httptest.NewRequest(http.MethodPost, "/v1/embeddings",
        strings.NewReader(`{"model":"text-embedding-ada-002","input":["hello"]}`))
    rec := httptest.NewRecorder()
    s.handleEmbeddings(rec, req)

    if rec.Code != http.StatusBadGateway {
        t.Fatalf("expected 502, got %d, body: %s", rec.Code, rec.Body.String())
    }
    body := rec.Body.String()
    if !strings.Contains(body, "Embedding failed") {
        t.Fatalf("502 body must keep the failure prefix, got: %s", body)
    }
    if !strings.Contains(body, "mlx embeddings handler timed out after 300s") {
        t.Fatalf("502 body must carry the upstream error detail, got: %s", body)
    }
}

func TestRerank_FailureSurfacesUpstreamError(t *testing.T) {
    s := newTestServerWithProvider("test-cloud", &mockProvider{
        name:      "test-cloud",
        healthy:   true,
        rerankErr: errors.New("rerank upstream connection reset"),
    })
    s.cfg.Config.Routing.Mode = "cloud"

    req := httptest.NewRequest(http.MethodPost, "/v1/rerank",
        strings.NewReader(`{"model":"rerank-model","query":"q","documents":["d"]}`))
    rec := httptest.NewRecorder()
    s.handleRerank(rec, req)

    if rec.Code != http.StatusBadGateway {
        t.Fatalf("expected 502, got %d, body: %s", rec.Code, rec.Body.String())
    }
    body := rec.Body.String()
    if !strings.Contains(body, "Rerank failed") {
        t.Fatalf("502 body must keep the failure prefix, got: %s", body)
    }
    if !strings.Contains(body, "rerank upstream connection reset") {
        t.Fatalf("502 body must carry the upstream error detail, got: %s", body)
    }
}

// #438: /health must stay healthy:true (process healthcheck green) while
// surfacing the most recent route failure — the exact wedge signature from
// the issue: embeddings 502 while /health reported fusion-mlx healthy.
func TestHealthSurfacesLastRouteError(t *testing.T) {
    s := newTestServerWithProvider("test-cloud", &mockProvider{
        name:    "test-cloud",
        healthy: true,
        embErr:  errors.New("mlx embeddings handler timed out after 300s"),
    })
    s.cfg.Config.Routing.Mode = "cloud"

    embReq := httptest.NewRequest(http.MethodPost, "/v1/embeddings",
        strings.NewReader(`{"model":"text-embedding-ada-002","input":["hello"]}`))
    s.handleEmbeddings(httptest.NewRecorder(), embReq)

    req := httptest.NewRequest(http.MethodGet, "/health", nil)
    rec := httptest.NewRecorder()
    s.handleHealth(rec, req)

    if rec.Code != http.StatusOK {
        t.Fatalf("expected 200, got %d", rec.Code)
    }
    var body struct {
        Status   string `json:"status"`
        Backends map[string]struct {
            Healthy        bool `json:"healthy"`
            LastRouteError *struct {
                Message string    `json:"message"`
                At      time.Time `json:"at"`
            } `json:"last_route_error"`
        } `json:"backends"`
    }
    if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
        t.Fatalf("invalid health json: %v, body: %s", err, rec.Body.String())
    }
    be, ok := body.Backends["test-cloud"]
    if !ok {
        t.Fatalf("test-cloud missing from health backends: %s", rec.Body.String())
    }
    if !be.Healthy {
        t.Fatal("backend stays healthy:true — the wedge is route-level, not process-level")
    }
    if be.LastRouteError == nil {
        t.Fatalf("health must surface last_route_error, got: %s", rec.Body.String())
    }
    if !strings.Contains(be.LastRouteError.Message, "mlx embeddings handler timed out") {
        t.Fatalf("last_route_error must carry the upstream detail, got: %s", be.LastRouteError.Message)
    }
    if be.LastRouteError.At.IsZero() {
        t.Fatal("last_route_error must carry a timestamp")
    }
}

func TestNoteRouteErrorTruncatesAndIsNilSafe(t *testing.T) {
    var s Server
    s.noteRouteError("", errors.New("x"))
    s.noteRouteError("p", nil)
    if _, ok := s.lastRouteError("p"); ok {
        t.Fatal("nil error must not be recorded")
    }
    long := strings.Repeat("a", 900)
    s.noteRouteError("p", errors.New(long))
    e, ok := s.lastRouteError("p")
    if !ok {
        t.Fatal("error must be recorded")
    }
    if len(e.Message) != 512 {
        t.Fatalf("message must be capped at 512, got %d", len(e.Message))
    }
}
