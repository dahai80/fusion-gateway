package adapter

// #671 regression: fusion-mlx emits rerank results[].document as null
// (return_documents=false) or an object {"text": ...} (true) — the old
// *string shape failed every real decode with "cannot unmarshal object into
// Go struct field RerankResult.results.document of type string". The field
// is verbatim passthrough (json.RawMessage): no gateway consumer reads it.

import (
    "context"
    "net/http"
    "net/http/httptest"
    "testing"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

func Test671_RerankDocumentObjectAndNull(t *testing.T) {
    upstreamBody := `{"id":"rerank-1","model":"bge-reranker-v2-m3","results":[
        {"index":0,"relevance_score":0.27,"document":null},
        {"index":1,"relevance_score":0.81,"document":{"text":"matching doc"}}]}`

    for _, tc := range []struct {
        name    string
        backend string
    }{
        {"fusion-mlx", "fusion-mlx"},
        {"openai-compatible", "openai-compatible"},
    } {
        t.Run(tc.name, func(t *testing.T) {
            srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
                w.WriteHeader(http.StatusOK)
                _, _ = w.Write([]byte(upstreamBody))
            }))
            defer srv.Close()

            var p Provider
            if tc.backend == "fusion-mlx" {
                p = NewFusionMLXProvider(config.BackendConfig{BaseURL: srv.URL}, config.RoutingConfig{})
            } else {
                p = NewOpenAICompatibleProvider("doc", config.BackendConfig{BaseURL: srv.URL})
            }
            resp, err := p.Rerank(context.Background(), &RerankRequest{
                Model: "bge-reranker-v2-m3", Query: "q", Documents: []string{"a", "b"},
            })
            if err != nil {
                t.Fatalf("decode failed: %v", err)
            }
            if len(resp.Results) != 2 {
                t.Fatalf("want 2 results, got %d", len(resp.Results))
            }
            if got := string(resp.Results[0].Document); got != "null" {
                t.Fatalf("result[0].document = %q, want verbatim null", got)
            }
            if got := string(resp.Results[1].Document); got != `{"text":"matching doc"}` {
                t.Fatalf("result[1].document = %q, want verbatim object", got)
            }
        })
    }
}
