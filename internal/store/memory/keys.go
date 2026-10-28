package memory

import (
    "fmt"
    "sync"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/store"
)

type KeyStore struct {
    mu       sync.RWMutex
    keys     map[string]*store.APIKeyEntry
    byHash   map[string]string
    onMutate func()
}

func NewKeyStore() *KeyStore {
    return &KeyStore{
        keys:   make(map[string]*store.APIKeyEntry),
        byHash: make(map[string]string),
    }
}

func (s *KeyStore) SetOnMutate(fn func()) { s.onMutate = fn }

// copyEntry returns a shallow copy so callers (admin JSON serialization,
// auth middleware) never hold the internal pointer while QuotaStore mutates
// usage fields under its own lock (audit B1 data race). Slices/maps inside
// the copy stay shared — they are replaced wholesale on Update, never
// mutated in place.
func copyEntry(k *store.APIKeyEntry) *store.APIKeyEntry {
    cp := *k
    return &cp
}

func (s *KeyStore) List() ([]*store.APIKeyEntry, error) {
    s.mu.RLock()
    defer s.mu.RUnlock()
    result := make([]*store.APIKeyEntry, 0, len(s.keys))
    for _, k := range s.keys {
        result = append(result, copyEntry(k))
    }
    return result, nil
}

func (s *KeyStore) Get(name string) (*store.APIKeyEntry, error) {
    s.mu.RLock()
    defer s.mu.RUnlock()
    k, ok := s.keys[name]
    if !ok {
        return nil, fmt.Errorf("key not found: %s", name)
    }
    return copyEntry(k), nil
}

func (s *KeyStore) GetByHash(hash string) (*store.APIKeyEntry, error) {
    s.mu.RLock()
    defer s.mu.RUnlock()
    name, ok := s.byHash[hash]
    if !ok {
        // #377: a hash miss is an ordinary invalid-key 401, not a store
        // failure. Returning (nil, nil) keeps the auth middleware's AH5
        // ERROR branch (real store outages) quiet against poller noise.
        return nil, nil
    }
    k, ok := s.keys[name]
    if !ok {
        return nil, nil
    }
    return copyEntry(k), nil
}

func (s *KeyStore) Create(key *store.APIKeyEntry) error {
    s.mu.Lock()
    if _, exists := s.keys[key.Name]; exists {
        s.mu.Unlock()
        return fmt.Errorf("key already exists: %s", key.Name)
    }
    now := time.Now()
    key.CreatedAt = now
    key.UpdatedAt = now
    if key.Status == "" {
        key.Status = "active"
    }
    key.QuotaRemaining = key.QuotaLimit - key.QuotaUsed
    s.keys[key.Name] = key
    if key.KeyHash != "" {
        s.byHash[key.KeyHash] = key.Name
    }
    // #262 K2: index the pre-rotation hash too so a rotated-but-still-graced
    // key resolves to its entry; the auth layer enforces the grace window.
    if key.KeyPrevHash != "" && key.KeyPrevHash != key.KeyHash {
        s.byHash[key.KeyPrevHash] = key.Name
    }
    s.mu.Unlock()
    if s.onMutate != nil {
        s.onMutate()
    }
    return nil
}

func (s *KeyStore) Update(key *store.APIKeyEntry) error {
    s.mu.Lock()
    old, exists := s.keys[key.Name]
    if !exists {
        s.mu.Unlock()
        return fmt.Errorf("key not found: %s", key.Name)
    }
    if old.KeyHash != "" && old.KeyHash != key.KeyHash {
        delete(s.byHash, old.KeyHash)
    }
    if old.KeyPrevHash != "" && old.KeyPrevHash != key.KeyPrevHash {
        delete(s.byHash, old.KeyPrevHash)
    }
    key.UpdatedAt = time.Now()
    key.QuotaRemaining = key.QuotaLimit - key.QuotaUsed
    s.keys[key.Name] = key
    if key.KeyHash != "" {
        s.byHash[key.KeyHash] = key.Name
    }
    // #262 K2: keep the pre-rotation hash indexed (dual-active grace window).
    if key.KeyPrevHash != "" && key.KeyPrevHash != key.KeyHash {
        s.byHash[key.KeyPrevHash] = key.Name
    }
    s.mu.Unlock()
    if s.onMutate != nil {
        s.onMutate()
    }
    return nil
}

func (s *KeyStore) Delete(name string) error {
    s.mu.Lock()
    k, exists := s.keys[name]
    if !exists {
        s.mu.Unlock()
        return fmt.Errorf("key not found: %s", name)
    }
    if k.KeyHash != "" {
        delete(s.byHash, k.KeyHash)
    }
    delete(s.keys, name)
    s.mu.Unlock()
    if s.onMutate != nil {
        s.onMutate()
    }
    return nil
}

// UpdateUsage writes the quota-display fields of the internal entry under
// keys.mu (audit B1): QuotaStore mutates them on the hot path while admin
// handlers and auth middleware read the entry — without this lock the
// accesses race. Callers may hold q.mu (order q.mu → keys.mu is the only
// nesting used).
func (s *KeyStore) UpdateUsage(name string, quotaUsed, quotaRemaining, dailyUsed float64, dailyDate string) {
    s.mu.Lock()
    if k, ok := s.keys[name]; ok {
        k.QuotaUsed = quotaUsed
        k.QuotaRemaining = quotaRemaining
        k.DailyUsed = dailyUsed
        k.DailyDate = dailyDate
    }
    s.mu.Unlock()
}

func (s *KeyStore) LoadFromConfig(keys map[string]*store.APIKeyEntry) {
    s.mu.Lock()
    defer s.mu.Unlock()
    for name, k := range keys {
        s.keys[name] = k
        if k.KeyHash != "" {
            s.byHash[k.KeyHash] = name
        }
    }
}
