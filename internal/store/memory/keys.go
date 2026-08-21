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

func (s *KeyStore) List() ([]*store.APIKeyEntry, error) {
    s.mu.RLock()
    defer s.mu.RUnlock()
    result := make([]*store.APIKeyEntry, 0, len(s.keys))
    for _, k := range s.keys {
        result = append(result, k)
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
    return k, nil
}

func (s *KeyStore) GetByHash(hash string) (*store.APIKeyEntry, error) {
    s.mu.RLock()
    defer s.mu.RUnlock()
    name, ok := s.byHash[hash]
    if !ok {
        return nil, fmt.Errorf("key not found by hash")
    }
    k, ok := s.keys[name]
    if !ok {
        return nil, fmt.Errorf("key not found by hash")
    }
    return k, nil
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
    key.UpdatedAt = time.Now()
    key.QuotaRemaining = key.QuotaLimit - key.QuotaUsed
    s.keys[key.Name] = key
    if key.KeyHash != "" {
        s.byHash[key.KeyHash] = key.Name
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
