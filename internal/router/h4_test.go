package router

// H4 guard test: the intent-classifier http.Client runs on the Decide hot path
// (P-1 semantic intent) for every request when enabled. It must route through
// adapter.TransportForBackend so it inherits the MaxConnsPerHost FD cap — a
// bare &http.Client{} inherits http.DefaultTransport (MaxConnsPerHost=0 =
// unlimited), the FD-exhaustion vector H4 closes. Revert the Transport assignment
// → MaxConnsPerHost assertion fails.

import (
    "net/http"
    "testing"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

func TestH4_IntentClassifier_CappedTransport(t *testing.T) {
    c := NewRouterLightClassifier(config.IntentClassifierConfig{
        Enabled:   true,
        Endpoint:  "http://127.0.0.1:11434",
        BaseModel: "qwen2.5-1.5b",
    })
    if c == nil {
        t.Fatal("H4: expected non-nil classifier (base_model set)")
    }
    if c.httpClient == nil {
        t.Fatal("H4: classifier httpClient is nil")
    }
    tpt, ok := c.httpClient.Transport.(*http.Transport)
    if !ok {
        t.Fatalf("H4: classifier Transport not *http.Transport (got %T) — bare client, no FD cap", c.httpClient.Transport)
    }
    if tpt.MaxConnsPerHost <= 0 {
        t.Fatalf("H4: classifier MaxConnsPerHost=%d — FD cap missing (0/unlimited is the H4 bug)", tpt.MaxConnsPerHost)
    }
}
