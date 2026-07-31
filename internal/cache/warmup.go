package cache

import (
    "encoding/json"
    "log/slog"
    "os"
    "time"
)

type WarmupEntry struct {
    Key   string `json:"key"`
    Value string `json:"value"`
    TTL   string `json:"ttl,omitempty"`
}

func WarmupFromFile(backend CacheBackend, filePath string) int {
    if filePath == "" || backend == nil {
        return 0
    }
    data, err := os.ReadFile(filePath)
    if err != nil {
        slog.Error("cache warmup: failed to read file", "path", filePath, "error", err)
        return 0
    }
    var entries []WarmupEntry
    if err := json.Unmarshal(data, &entries); err != nil {
        slog.Error("cache warmup: failed to parse JSON", "path", filePath, "error", err)
        return 0
    }
    loaded := 0
    for _, e := range entries {
        ttl := 5 * time.Minute
        if e.TTL != "" {
            if d, err := time.ParseDuration(e.TTL); err == nil {
                ttl = d
            }
        }
        backend.Set(e.Key, []byte(e.Value), ttl)
        loaded++
    }
    slog.Info("cache warmup complete", "path", filePath, "entries", loaded)
    return loaded
}
