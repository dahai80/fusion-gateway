package adapter

import (
    "context"
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "testing"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

// #501 regression: a request id spelled with "/" must be rewritten to the
// engine-canonical dash form when that form is in the engine registry —
// otherwise fusion-mlx loads a second copy of weights already resident under
// the dash spelling, doubling memory and tripping mlx_memory_overload.

func canonicalTestProvider(t *testing.T) *FusionMLXProvider {
    t.Helper()
    p := NewFusionMLXProvider(config.BackendConfig{
        Type:     "fusion-mlx",
        BaseURL:  "http://127.0.0.1:1",
    }, config.RoutingConfig{})
    p.modelInfos.Store(map[string]ModelInfo{
        "mlx-community--Qwen3.5-4B-MLX-4bit": {ID: "mlx-community--Qwen3.5-4B-MLX-4bit"},
        "BAAI--bge-m3":                       {ID: "BAAI--bge-m3"},
    })
    return p
}

func TestCanonicalizeModelID_SlashToDashWhenKnown(t *testing.T) {
    p := canonicalTestProvider(t)
    got := p.canonicalizeModelID("mlx-community/Qwen3.5-4B-MLX-4bit")
    if got != "mlx-community--Qwen3.5-4B-MLX-4bit" {
        t.Fatalf("slash id must rewrite to dash form, got %q", got)
    }
}

func TestCanonicalizeModelID_DashUnknownStaysUntouched(t *testing.T) {
    p := canonicalTestProvider(t)
    got := p.canonicalizeModelID("vendor/unknown-model")
    if got != "vendor/unknown-model" {
        t.Fatalf("unknown id must pass through unchanged, got %q", got)
    }
}

func TestCanonicalizeModelID_NoSlashUnchanged(t *testing.T) {
    p := canonicalTestProvider(t)
    got := p.canonicalizeModelID("BAAI--bge-m3")
    if got != "BAAI--bge-m3" {
        t.Fatalf("id without slash must be unchanged, got %q", got)
    }
}

func TestCanonicalizeModelID_NilInfosStaysUntouched(t *testing.T) {
    p := NewFusionMLXProvider(config.BackendConfig{Type: "fusion-mlx", BaseURL: "http://127.0.0.1:1"}, config.RoutingConfig{})
    got := p.canonicalizeModelID("mlx-community/Qwen3.5-4B-MLX-4bit")
    if got != "mlx-community/Qwen3.5-4B-MLX-4bit" {
        t.Fatalf("nil registry must pass through unchanged, got %q", got)
    }
}

// Full-path: Chat must marshal the canonicalized id into the upstream body.
func TestChat_CanonicalizesModelID_Upstream(t *testing.T) {
    var upstreamModel string
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        var body map[string]any
        _ = json.NewDecoder(r.Body).Decode(&body)
        upstreamModel, _ = body["model"].(string)
        w.Header().Set("Content-Type", "application/json")
        _ = json.NewEncoder(w).Encode(map[string]any{
            "id": "cmpl-x", "object": "chat.completion", "model": upstreamModel,
            "choices": []map[string]any{{"index": 0, "finish_reason": "stop",
                "message": map[string]any{"role": "assistant", "content": "ok"}}},
            "usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
        })
    }))
    defer srv.Close()

    p := NewFusionMLXProvider(config.BackendConfig{Type: "fusion-mlx", BaseURL: srv.URL}, config.RoutingConfig{})
    p.modelInfos.Store(map[string]ModelInfo{
        "mlx-community--Qwen3.5-4B-MLX-4bit": {ID: "mlx-community--Qwen3.5-4B-MLX-4bit"},
    })
    resp, err := p.Chat(context.Background(), &ChatRequest{Model: "mlx-community/Qwen3.5-4B-MLX-4bit"})
    if err != nil {
        t.Fatalf("chat: %v", err)
    }
    if upstreamModel != "mlx-community--Qwen3.5-4B-MLX-4bit" {
        t.Fatalf("upstream must receive canonical dash id, got %q", upstreamModel)
    }
    if resp == nil || resp.Model != "mlx-community--Qwen3.5-4B-MLX-4bit" {
        t.Fatalf("response model must echo canonical id, got %+v", resp)
    }
}
