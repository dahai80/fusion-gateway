package adapter

import (
    "context"
    "encoding/json"
    "io"
    "log/slog"
    "net/http"
    "strings"
    "sync"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

// maxAdapterIndexBytes caps the response body we decode from fusion-mlx. The
// adapter index is a small manifest; 10 MiB is the API-response OOM cap used
// across streaming adapters (anthropic/openai-compatible/builtins) and gives a
// generous upper bound without unbounded allocation on a malformed/malicious
// response.
const maxAdapterIndexBytes = 10 * 1024 * 1024

// AdapterInfo is one LoRA adapter entry as reported by fusion-mlx's
// GET /admin/api/fine-tune/adapters endpoint. Fields mirror the upstream
// service list_adapters() dict (adapter_name, model_id, has_weights,
// has_config, lora_rank); extra fields are ignored on decode.
type AdapterInfo struct {
    AdapterName string `json:"adapter_name"`
    ModelID     string `json:"model_id"`
    HasWeights  bool   `json:"has_weights"`
    HasConfig   bool   `json:"has_config"`
    LoraRank    int    `json:"lora_rank"`
}

// AdapterIndex caches the set of LoRA adapters available on the local
// fusion-mlx backend. It polls GET /admin/api/fine-tune/adapters on demand via
// Refresh; List/Has serve from the in-memory snapshot under an RWMutex. The
// router's heuristic classifier can validate a configured code_adapter against
// this index (best-effort: an empty/never-refreshed index skips validation).
type AdapterIndex struct {
    baseURL    string
    apiKey     string
    httpClient *http.Client

    mu          sync.RWMutex
    adapters    []AdapterInfo
    byName      map[string]AdapterInfo
    lastErr     error
    refreshedAt time.Time
}

// NewAdapterIndex builds an index that polls the fusion-mlx backend described
// by backendCfg. baseURL/apiKey come from BackendConfig; the transport inherits
// outbound UDS when backendCfg.SocketPath is set (via TransportForBackend), so
// the index fetch rides the same zero-copy path as inference traffic.
func NewAdapterIndex(backendCfg config.BackendConfig) *AdapterIndex {
    timeout := backendCfg.Timeout
    if timeout == 0 {
        timeout = 10 * time.Second
    }
    // Trim a trailing slash so baseURL+"/admin/..." never produces a double
    // slash. The documented UDS convention uses the dummy host http://unix/
    // (trailing slash), while TCP base URLs like http://127.0.0.1:11434 do not;
    // normalizing here lets both styles join cleanly.
    base := strings.TrimRight(backendCfg.BaseURL, "/")
    return &AdapterIndex{
        baseURL: base,
        apiKey:  backendCfg.APIKey,
        httpClient: &http.Client{
            Timeout:   timeout,
            Transport: TransportForBackend(backendCfg),
        },
    }
}

// Refresh fetches the adapter list from fusion-mlx and swaps the cached
// snapshot. A non-nil error is retained in lastErr and surfaced via LastError;
// a successful refresh clears it. Empty/missing endpoint responses are valid
// (zero adapters), not errors.
func (a *AdapterIndex) Refresh(ctx context.Context) error {
    if a.baseURL == "" {
        slog.Debug("adapter index: empty base_url, skipping refresh")
        return nil
    }

    url := a.baseURL + "/admin/api/fine-tune/adapters"
    req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
    if err != nil {
        a.mu.Lock()
        a.lastErr = err
        a.mu.Unlock()
        slog.Warn("adapter index: build request failed", "url", url, "error", err)
        return err
    }
    if a.apiKey != "" {
        req.Header.Set("Authorization", "Bearer "+a.apiKey)
    }
    req.Header.Set("Accept", "application/json")

    resp, err := a.httpClient.Do(req)
    if err != nil {
        a.mu.Lock()
        a.lastErr = err
        a.mu.Unlock()
        slog.Warn("adapter index: fetch failed", "url", url, "error", err)
        return err
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusOK {
        err := errUnexpectedStatus(url, resp.StatusCode)
        a.mu.Lock()
        a.lastErr = err
        a.mu.Unlock()
        slog.Warn("adapter index: non-200 status", "url", url, "status", resp.StatusCode)
        return err
    }

    body, err := io.ReadAll(io.LimitReader(resp.Body, maxAdapterIndexBytes))
    if err != nil {
        a.mu.Lock()
        a.lastErr = err
        a.mu.Unlock()
        slog.Warn("adapter index: read body failed", "url", url, "error", err)
        return err
    }

    var list []AdapterInfo
    if err := json.Unmarshal(body, &list); err != nil {
        a.mu.Lock()
        a.lastErr = err
        a.mu.Unlock()
        slog.Warn("adapter index: decode failed", "url", url, "error", err, "bytes", len(body))
        return err
    }

    byName := make(map[string]AdapterInfo, len(list))
    for _, ad := range list {
        if ad.AdapterName == "" {
            continue
        }
        byName[ad.AdapterName] = ad
    }

    a.mu.Lock()
    a.adapters = list
    a.byName = byName
    a.lastErr = nil
    a.refreshedAt = time.Now()
    a.mu.Unlock()

    slog.Info("adapter index refreshed", "count", len(list), "url", url)
    return nil
}

// Has reports whether an adapter with the given name is present in the cached
// snapshot. False does not imply the adapter is absent on the backend, only
// that the index has not seen it (the snapshot may be stale or never
// refreshed). Callers should treat false as "unvalidated", not "missing".
func (a *AdapterIndex) Has(name string) bool {
    if name == "" {
        return false
    }
    a.mu.RLock()
    defer a.mu.RUnlock()
    _, ok := a.byName[name]
    return ok
}

// List returns a copy of the cached adapter snapshot. Safe for concurrent use.
func (a *AdapterIndex) List() []AdapterInfo {
    a.mu.RLock()
    defer a.mu.RUnlock()
    if len(a.adapters) == 0 {
        return nil
    }
    out := make([]AdapterInfo, len(a.adapters))
    copy(out, a.adapters)
    return out
}

// LastError returns the most recent Refresh error (nil after a successful
// refresh). Useful for health checks / admin surfacing.
func (a *AdapterIndex) LastError() error {
    a.mu.RLock()
    defer a.mu.RUnlock()
    return a.lastErr
}

// RefreshedAt returns the time of the last successful refresh. Zero value means
// no successful refresh has occurred yet.
func (a *AdapterIndex) RefreshedAt() time.Time {
    a.mu.RLock()
    defer a.mu.RUnlock()
    return a.refreshedAt
}

func errUnexpectedStatus(url string, status int) error {
    return &adapterIndexError{url: url, status: status}
}

type adapterIndexError struct {
    url    string
    status int
}

func (e *adapterIndexError) Error() string {
    return "adapter index: unexpected status " + http.StatusText(e.status) + " from " + e.url
}
