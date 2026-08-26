package server

import (
    "context"
    "encoding/json"
    "fmt"
    "io"
    "log/slog"
    "net/http"
    "sort"
    "strings"
    "sync"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/adapter"
    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/middleware"
    "github.com/fusion-gateway/fusion-gateway/internal/safego"
)

// listModelsPerProviderTimeout bounds each provider's ListModels call so a
// single unreachable cloud backend cannot stall the /v1/models endpoint.
const listModelsPerProviderTimeout = 3 * time.Second

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
    ctx := adapter.WithFusionHeaders(r.Context(), r)
    snap := config.SnapshotFromContext(ctx)

    providerNames := s.pool.ListProviders()

    // mode=local: only return models from local providers (fusion-mlx/kb/hub),
    // so a downstream health check never waits on slow cloud backends.
    if snap.Config.Routing.Mode == "local" {
        localOnly := make([]string, 0, len(providerNames))
        for _, name := range providerNames {
            if s.pool.IsLocalProvider(name) {
                localOnly = append(localOnly, name)
            }
        }
        slog.Debug("mode=local, listing only local providers", "total", len(providerNames), "local", len(localOnly))
        providerNames = localOnly
    }

    models := s.listModelsConcurrent(ctx, providerNames)

    // Mark Loaded on models actually resident in a local engine. The
    // authoritative source is fusion-mlx /health `loaded_models` (ModelSet
    // reflects /v1/models, which lists registered — not necessarily loaded —
    // models, so it cannot distinguish servable from merely catalogued, #59).
    loadedSet := map[string]bool{}
    if mlx := s.pool.GetFusionMLX(); mlx != nil {
        detail := mlx.HealthDetail(ctx)
        for _, id := range detail.LoadedModels {
            loadedSet[id] = true
        }
        if detail.FetchError != nil {
            slog.Warn("models endpoint: fusion-mlx health detail failed, loaded flags may be stale", "error", detail.FetchError)
        }
    }
    for i := range models {
        models[i].Loaded = loadedSet[models[i].ID]
    }

    // Order models so local (owned_by="local") models precede cloud models,
    // and within local, loaded models precede unloaded ones; alphabetical by
    // ID within each tier. Cloud providers sit at the top of config `backends:`,
    // so without this sort their models dominate the head of the response and
    // mask which models are actually served locally (#108 observation 2).
    sort.SliceStable(models, func(i, j int) bool {
        return modelListLess(models[i], models[j])
    })

    w.Header().Set("Content-Type", "application/json")
    _ = json.NewEncoder(w).Encode(map[string]interface{}{
        "object": "list",
        "data":   models,
    })
}

// modelListLess is the comparator for the /v1/models ordering (#108). Local
// models (owned_by="local") outrank cloud; loaded local models outrank
// unloaded local; otherwise alphabetical by ID. It is total only over the
// (local-vs-cloud, loaded-vs-unloaded, id) key tuple, so sort.SliceStable
// keeps equal-tier entries in provider-arrival order.
func modelListLess(a, b adapter.ModelInfo) bool {
    aLocal := a.OwnedBy == "local"
    bLocal := b.OwnedBy == "local"
    if aLocal != bLocal {
        return aLocal
    }
    if aLocal {
        if a.Loaded != b.Loaded {
            return a.Loaded
        }
    }
    return a.ID < b.ID
}

// listModelsConcurrent fans out ListModels across providers with a per-provider
// timeout. Failed or timed-out providers are skipped (logged at Warn) so the
// fast local models are never blocked by a slow/unreachable cloud backend, but
// a misconfigured/route-rejected backend still surfaces visibly (#29, Rule 12).
func (s *Server) listModelsConcurrent(ctx context.Context, names []string) []adapter.ModelInfo {
    type providerResult struct {
        name   string
        models []adapter.ModelInfo
        err    error
    }

    results := make(chan providerResult, len(names))
    var wg sync.WaitGroup

    for _, name := range names {
        wg.Add(1)
        safego.Go("server_list_models_fanout", func() {
            defer wg.Done()
            provider, ok := s.pool.Get(name)
            if !ok {
                results <- providerResult{name: name, err: fmt.Errorf("provider not found")}
                return
            }
            pCtx, cancel := context.WithTimeout(ctx, listModelsPerProviderTimeout)
            defer cancel()
            providerModels, err := provider.ListModels(pCtx)
            results <- providerResult{name: name, models: providerModels, err: err}
        })
    }

    wg.Wait()
    close(results)

    models := make([]adapter.ModelInfo, 0)
    for res := range results {
        if res.err != nil {
            slog.Warn("list models failed for provider, skipping", "provider", res.name, "error", res.err)
            continue
        }
        for _, m := range res.models {
            m.AvailableBackends = []string{res.name}
            models = append(models, m)
        }
    }
    return models
}

// resolveDefaultModel backfills an empty request model (issue #28).
// Priority: routing.default_model config value, then the first model returned
// by a local provider (fusion-mlx/kb/hub) via auto-discovery. Returns "" if
// nothing usable is found, leaving the original empty model to flow on.
func (s *Server) resolveDefaultModel(ctx context.Context) (string, error) {
    if dm := strings.TrimSpace(s.cfg.Config.Routing.DefaultModel); dm != "" {
        slog.Debug("using configured default_model", "model", dm)
        return dm, nil
    }

    // Auto-discover: only query local providers so a slow/unreachable cloud
    // backend never blocks the request (mirrors the /v1/models fix in #21).
    localNames := make([]string, 0)
    for _, name := range s.pool.ListProviders() {
        if s.pool.IsLocalProvider(name) {
            localNames = append(localNames, name)
        }
    }
    if len(localNames) == 0 {
        return "", fmt.Errorf("no local provider registered for default-model auto-discovery")
    }

    models := s.listModelsConcurrent(ctx, localNames)
    if len(models) == 0 {
        return "", fmt.Errorf("default_model not configured and no local model loaded")
    }
    slog.Debug("auto-discovered default model", "model", models[0].ID, "candidates", len(models))
    return models[0].ID, nil
}

func (s *Server) handleImages(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        http.Error(w, `{"error":{"message":"Method not allowed","type":"invalid_request"}}`, http.StatusMethodNotAllowed)
        return
    }

    maxBodySize := int64(s.cfg.Config.Server.MaxRequestBodySize)
    if maxBodySize <= 0 {
        maxBodySize = 5 << 20
    }
    body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodySize))
    if err != nil {
        http.Error(w, `{"error":{"message":"Failed to read request","type":"invalid_request"}}`, http.StatusBadRequest)
        return
    }
    defer r.Body.Close()

    var req adapter.ImageRequest
    if err := json.Unmarshal(body, &req); err != nil {
        slog.Error("invalid json in image request", "error", err)
        http.Error(w, `{"error":{"message":"Invalid JSON","type":"invalid_request"}}`, http.StatusBadRequest)
        return
    }

    if req.N <= 0 {
        req.N = 1
    }

    cloudBackend := s.cfg.Config.Routing.Fallback.CloudDefault
    if cloudBackend == "" {
        cloudBackend = "openai"
    }

    provider, ok := s.pool.Get(cloudBackend)
    if !ok {
        http.Error(w, `{"error":{"message":"Image generation backend not available","type":"server_error"}}`, http.StatusServiceUnavailable)
        return
    }

    imgProvider, ok := provider.(interface {
        Images(ctx context.Context, req *adapter.ImageRequest) (*adapter.ImageResponse, error)
    })
    if !ok {
        http.Error(w, `{"error":{"message":"Selected backend does not support image generation","type":"server_error"}}`, http.StatusBadRequest)
        return
    }

    start := time.Now()
    resp, err := imgProvider.Images(r.Context(), &req)
    if err != nil {
        slog.Error("image generation failed", "provider", provider.Name(), "error", err)
        http.Error(w, `{"error":{"message":"Image generation failed","type":"server_error"}}`, http.StatusBadGateway)
        return
    }

    if logEntry := middleware.GetRequestLog(r.Context()); logEntry != nil {
        logEntry.Model = req.Model
        logEntry.ChannelName = provider.Name()
        logEntry.ChannelType = "cloud"
        logEntry.StatusCode = http.StatusOK
    }

    slog.Info("image generation completed",
        "model", req.Model,
        "provider", provider.Name(),
        "latency_ms", time.Since(start).Milliseconds(),
    )

    w.Header().Set("Content-Type", "application/json")
    _ = json.NewEncoder(w).Encode(resp)
}
