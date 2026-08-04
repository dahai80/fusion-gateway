package server

import (
    "context"
    "encoding/json"
    "fmt"
    "net/http"
    "net/http/httptest"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/adapter"
    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

// blockingProvider simulates an unreachable cloud backend: ListModels blocks
// until the per-provider timeout cancels its context.
type blockingProvider struct {
    mockProvider
}

func (b *blockingProvider) ListModels(ctx context.Context) ([]adapter.ModelInfo, error) {
    <-ctx.Done()
    return nil, ctx.Err()
}

func TestHandleModels_ConcurrentSkipsFailedProvider(t *testing.T) {
    s := newTestServer()
    s.pool.Register("local-mlx", &mockProvider{
        name:   "local-mlx",
        models: []adapter.ModelInfo{{ID: "qwen3", OwnedBy: "fusion-mlx"}},
    }, config.BackendConfig{Type: "fusion-mlx", Enabled: true})
    s.pool.Register("cloud-dead", &mockProvider{
        name:      "cloud-dead",
        modelsErr: fmt.Errorf("connection refused"),
    }, config.BackendConfig{Type: "openai-compatible", Enabled: true})

    req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
    rec := httptest.NewRecorder()
    s.handleModels(rec, req)

    if rec.Code != http.StatusOK {
        t.Fatalf("expected 200, got %d", rec.Code)
    }
    var resp struct {
        Object string              `json:"object"`
        Data   []adapter.ModelInfo `json:"data"`
    }
    if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
        t.Fatalf("decode response: %v", err)
    }
    if resp.Object != "list" {
        t.Errorf("expected object=list, got %q", resp.Object)
    }
    if len(resp.Data) != 1 || resp.Data[0].ID != "qwen3" {
        t.Fatalf("expected only local model qwen3 (cloud failed must be skipped), got %+v", resp.Data)
    }
}

func TestHandleModels_ModeLocalOnlyReturnsLocal(t *testing.T) {
    s := newTestServer()
    s.cfg.Config.Routing.Mode = "local"
    s.pool.Register("local-mlx", &mockProvider{
        name:   "local-mlx",
        models: []adapter.ModelInfo{{ID: "qwen3", OwnedBy: "fusion-mlx"}},
    }, config.BackendConfig{Type: "fusion-mlx", Enabled: true})
    s.pool.Register("cloud-openai", &mockProvider{
        name:   "cloud-openai",
        models: []adapter.ModelInfo{{ID: "gpt-4", OwnedBy: "openai"}},
    }, config.BackendConfig{Type: "openai-compatible", Enabled: true})

    req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
    req = req.WithContext(config.WithSnapshot(req.Context(), s.cfg))
    rec := httptest.NewRecorder()
    s.handleModels(rec, req)

    var resp struct {
        Data []adapter.ModelInfo `json:"data"`
    }
    if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
        t.Fatalf("decode response: %v", err)
    }
    if len(resp.Data) != 1 || resp.Data[0].ID != "qwen3" {
        t.Fatalf("mode=local must only return local provider models, got %+v", resp.Data)
    }
    for _, m := range resp.Data {
        if m.ID == "gpt-4" {
            t.Fatalf("mode=local must not return cloud models, but got gpt-4")
        }
    }
}

func TestHandleModels_PerProviderTimeoutSkipsSlow(t *testing.T) {
    s := newTestServer()
    // slow cloud backend blocks until the 3s per-provider timeout fires
    s.pool.Register("cloud-slow", &blockingProvider{
        mockProvider: mockProvider{name: "cloud-slow"},
    }, config.BackendConfig{Type: "openai-compatible", Enabled: true})
    // local backend returns immediately
    s.pool.Register("local-mlx", &mockProvider{
        name:   "local-mlx",
        models: []adapter.ModelInfo{{ID: "qwen3", OwnedBy: "fusion-mlx"}},
    }, config.BackendConfig{Type: "fusion-mlx", Enabled: true})

    start := time.Now()
    req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
    rec := httptest.NewRecorder()
    s.handleModels(rec, req)
    elapsed := time.Since(start)

    var resp struct {
        Data []adapter.ModelInfo `json:"data"`
    }
    if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
        t.Fatalf("decode response: %v", err)
    }
    if len(resp.Data) != 1 || resp.Data[0].ID != "qwen3" {
        t.Fatalf("expected local qwen3 despite slow cloud backend, got %+v", resp.Data)
    }
    // Worst case is the 3s per-provider timeout, far below the 30s+ serial block.
    if elapsed > 8*time.Second {
        t.Fatalf("handleModels blocked too long: %v (per-provider timeout should cap at ~3s)", elapsed)
    }
}
