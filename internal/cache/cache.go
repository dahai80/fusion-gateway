package cache

import (
    "container/list"
    "crypto/sha256"
    "encoding/json"
    "fmt"
    "log/slog"
    "sync"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/safego"
)

type entry struct {
    key       string
    value     []byte
    expiresAt time.Time
    size      int
}

type Cache struct {
    mu         sync.RWMutex
    items      map[string]*list.Element
    order      *list.List
    maxEntries int
    ttl        time.Duration
    maxBytes   int64
    usedBytes  int64
    hits       int64
    misses     int64
}

func New(cfg config.CacheConfig) *Cache {
    if !cfg.Enabled {
        return nil
    }

    maxEntries := cfg.MaxEntries
    if maxEntries <= 0 {
        maxEntries = 10000
    }

    ttl := cfg.TTL
    if ttl <= 0 {
        ttl = 5 * time.Minute
    }

    var maxBytes int64
    if cfg.MaxMemoryMB > 0 {
        maxBytes = int64(cfg.MaxMemoryMB) * 1024 * 1024
    }

    c := &Cache{
        items:      make(map[string]*list.Element),
        order:      list.New(),
        maxEntries: maxEntries,
        ttl:        ttl,
        maxBytes:   maxBytes,
    }

    // M2 fix: use safeGo for panic recovery on background goroutines
    safego.Go("cache_evict_expired", c.evictExpired)
    return c
}

func cacheKey(model string, messages interface{}, params interface{}) string {
    h := sha256.New()
    fmt.Fprintf(h, "%s:", model)
    if data, err := json.Marshal(messages); err == nil {
        h.Write(data)
    }
    fmt.Fprintf(h, ":")
    if data, err := json.Marshal(params); err == nil {
        h.Write(data)
    }
    return fmt.Sprintf("%x", h.Sum(nil))
}

func (c *Cache) Get(key string) ([]byte, bool) {
    if c == nil {
        return nil, false
    }

    // L3 fix: RLock fast path for cache miss
    c.mu.RLock()
    elem, ok := c.items[key]
    if !ok {
        c.mu.RUnlock()
        c.mu.Lock()
        c.misses++
        c.mu.Unlock()
        return nil, false
    }

    e := elem.Value.(*entry)
    if time.Now().After(e.expiresAt) {
        c.mu.RUnlock()
        c.mu.Lock()
        c.removeElement(elem)
        c.misses++
        c.mu.Unlock()
        return nil, false
    }

    value := e.value
    c.mu.RUnlock()

    // Upgrade to write lock for LRU move-to-front + hit counter
    c.mu.Lock()
    c.order.MoveToFront(elem)
    c.hits++
    c.mu.Unlock()

    return value, true
}

func (c *Cache) Set(key string, value []byte) {
    if c == nil {
        return
    }

    c.mu.Lock()
    defer c.mu.Unlock()

    if elem, ok := c.items[key]; ok {
        c.removeElement(elem)
    }

    e := &entry{
        key:       key,
        value:     value,
        expiresAt: time.Now().Add(c.ttl),
        size:      len(value),
    }

    elem := c.order.PushFront(e)
    c.items[key] = elem
    c.usedBytes += int64(e.size)

    for (c.maxBytes > 0 && c.usedBytes > c.maxBytes) || len(c.items) > c.maxEntries {
        oldest := c.order.Back()
        if oldest == nil {
            break
        }
        c.removeElement(oldest)
    }
}

func (c *Cache) removeElement(elem *list.Element) {
    e := elem.Value.(*entry)
    c.order.Remove(elem)
    delete(c.items, e.key)
    c.usedBytes -= int64(e.size)
}

func (c *Cache) evictExpired() {
    if c == nil {
        return
    }
    ticker := time.NewTicker(30 * time.Second)
    defer ticker.Stop()

    for range ticker.C {
        // P1 fix: collect expired keys under RLock (fast), then delete under Lock (short)
        c.mu.RLock()
        now := time.Now()
        var toRemove []string
        for key, elem := range c.items {
            e := elem.Value.(*entry)
            if now.After(e.expiresAt) {
                toRemove = append(toRemove, key)
            }
        }
        c.mu.RUnlock()

        if len(toRemove) == 0 {
            continue
        }

        c.mu.Lock()
        for _, key := range toRemove {
            if elem, ok := c.items[key]; ok {
                c.removeElement(elem)
            }
        }
        c.mu.Unlock()

        slog.Debug("cache expired entries evicted", "count", len(toRemove))
    }
}

func (c *Cache) Stats() (hits, misses int64, size int) {
    if c == nil {
        return 0, 0, 0
    }
    c.mu.RLock()
    defer c.mu.RUnlock()
    return c.hits, c.misses, len(c.items)
}

// UpdateConfig updates cache configuration at runtime (硬伤2 fix)
func (c *Cache) UpdateConfig(cfg config.CacheConfig) {
    if c == nil {
        return
    }
    c.mu.Lock()
    defer c.mu.Unlock()

    if cfg.MaxEntries > 0 {
        c.maxEntries = cfg.MaxEntries
        // Evict excess entries immediately
        for len(c.items) > c.maxEntries {
            oldest := c.order.Back()
            if oldest == nil {
                break
            }
            c.removeElement(oldest)
        }
        slog.Info("cache maxEntries updated", "max_entries", c.maxEntries, "current_size", len(c.items))
    }

    if cfg.TTL > 0 {
        c.ttl = cfg.TTL
        slog.Info("cache TTL updated", "ttl", c.ttl)
    }

    if cfg.MaxMemoryMB > 0 {
        c.maxBytes = int64(cfg.MaxMemoryMB) * 1024 * 1024
        // Evict entries exceeding new memory limit
        for c.maxBytes > 0 && c.usedBytes > c.maxBytes {
            oldest := c.order.Back()
            if oldest == nil {
                break
            }
            c.removeElement(oldest)
        }
        slog.Info("cache maxBytes updated", "max_bytes", c.maxBytes, "used_bytes", c.usedBytes)
    }
}

func (c *Cache) Delete(key string) {
    if c == nil {
        return
    }
    c.mu.Lock()
    defer c.mu.Unlock()
    if elem, ok := c.items[key]; ok {
        c.removeElement(elem)
    }
}

func ComputeCacheKey(model string, messages interface{}, temperature *float64, maxTokens *int, topP *float64, extra ...interface{}) string {
    params := map[string]interface{}{
        "temperature": temperature,
        "max_tokens":  maxTokens,
        "top_p":       topP,
    }
    // F6 fix: include tenant, tools, tool_choice, stop in cache key
    for i := 0; i+1 < len(extra); i += 2 {
        key, ok := extra[i].(string)
        if ok {
            params[key] = extra[i+1]
        }
    }
    return cacheKey(model, messages, params)
}
