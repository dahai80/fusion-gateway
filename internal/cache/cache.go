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

    go c.evictExpired()
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

    c.mu.Lock()
    defer c.mu.Unlock()

    elem, ok := c.items[key]
    if !ok {
        c.misses++
        return nil, false
    }

    e := elem.Value.(*entry)
    if time.Now().After(e.expiresAt) {
        c.removeElement(elem)
        c.misses++
        return nil, false
    }

    c.order.MoveToFront(elem)
    c.hits++
    return e.value, true
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
        c.mu.Lock()
        now := time.Now()
        var toRemove []*list.Element
        for _, elem := range c.items {
            e := elem.Value.(*entry)
            if now.After(e.expiresAt) {
                toRemove = append(toRemove, elem)
            }
        }
        for _, elem := range toRemove {
            c.removeElement(elem)
        }
        c.mu.Unlock()

        if len(toRemove) > 0 {
            slog.Debug("cache expired entries evicted", "count", len(toRemove))
        }
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

func ComputeCacheKey(model string, messages interface{}, temperature *float64, maxTokens *int, topP *float64) string {
    return cacheKey(model, messages, map[string]interface{}{
        "temperature": temperature,
        "max_tokens":  maxTokens,
        "top_p":       topP,
    })
}
