package router

import (
    "log/slog"
    "sync"
    "time"
)

type affinityEntry struct {
    ProviderName string
    ExpiresAt    time.Time
}

type SessionAffinity struct {
    mu      sync.RWMutex
    entries map[string]affinityEntry
    ttl     time.Duration
    done    chan struct{}
}

func NewSessionAffinity(ttl time.Duration) *SessionAffinity {
    if ttl <= 0 {
        ttl = 30 * time.Minute
    }
    sa := &SessionAffinity{
        entries: make(map[string]affinityEntry),
        ttl:     ttl,
        done:    make(chan struct{}),
    }
    go sa.evictLoop()
    slog.Info("session affinity initialized", "ttl", ttl)
    return sa
}

func (sa *SessionAffinity) Stop() {
    close(sa.done)
}

func (sa *SessionAffinity) Record(spaceID, providerName string) {
    if spaceID == "" || providerName == "" {
        return
    }
    sa.mu.Lock()
    defer sa.mu.Unlock()
    sa.entries[spaceID] = affinityEntry{
        ProviderName: providerName,
        ExpiresAt:    time.Now().Add(sa.ttl),
    }
    slog.Debug("session affinity recorded", "space_id", spaceID, "provider", providerName)
}

func (sa *SessionAffinity) Lookup(spaceID string) (providerName string, ok bool) {
    if spaceID == "" {
        return "", false
    }
    sa.mu.RLock()
    defer sa.mu.RUnlock()
    entry, exists := sa.entries[spaceID]
    if !exists {
        return "", false
    }
    if time.Now().After(entry.ExpiresAt) {
        return "", false
    }
    return entry.ProviderName, true
}

func (sa *SessionAffinity) Remove(spaceID string) {
    sa.mu.Lock()
    defer sa.mu.Unlock()
    delete(sa.entries, spaceID)
}

func (sa *SessionAffinity) Size() int {
    sa.mu.RLock()
    defer sa.mu.RUnlock()
    return len(sa.entries)
}

func (sa *SessionAffinity) evictLoop() {
    ticker := time.NewTicker(5 * time.Minute)
    defer ticker.Stop()
    for {
        select {
        case <-sa.done:
            return
        case <-ticker.C:
            sa.evictExpired()
        }
    }
}

func (sa *SessionAffinity) evictExpired() {
    now := time.Now()
    sa.mu.Lock()
    defer sa.mu.Unlock()
    evicted := 0
    for k, v := range sa.entries {
        if now.After(v.ExpiresAt) {
            delete(sa.entries, k)
            evicted++
        }
    }
    if evicted > 0 {
        slog.Debug("session affinity evicted expired entries", "evicted", evicted, "remaining", len(sa.entries))
    }
}
