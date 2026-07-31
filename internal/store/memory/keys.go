package memory

import (
    "fmt"
    "sync"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/store"
)

type KeyStore struct {
    mu   sync.RWMutex
    keys map[string]*store.APIKeyEntry
}

func NewKeyStore() *KeyStore {
    return &KeyStore{
        keys: make(map[string]*store.APIKeyEntry),
    }
}

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

func (s *KeyStore) Create(key *store.APIKeyEntry) error {
    s.mu.Lock()
    defer s.mu.Unlock()
    if _, exists := s.keys[key.Name]; exists {
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
    return nil
}

func (s *KeyStore) Update(key *store.APIKeyEntry) error {
    s.mu.Lock()
    defer s.mu.Unlock()
    if _, exists := s.keys[key.Name]; !exists {
        return fmt.Errorf("key not found: %s", key.Name)
    }
    key.UpdatedAt = time.Now()
    key.QuotaRemaining = key.QuotaLimit - key.QuotaUsed
    s.keys[key.Name] = key
    return nil
}

func (s *KeyStore) Delete(name string) error {
    s.mu.Lock()
    defer s.mu.Unlock()
    if _, exists := s.keys[name]; !exists {
        return fmt.Errorf("key not found: %s", name)
    }
    delete(s.keys, name)
    return nil
}

func (s *KeyStore) LoadFromConfig(keys map[string]*store.APIKeyEntry) {
    s.mu.Lock()
    defer s.mu.Unlock()
    for name, k := range keys {
        s.keys[name] = k
    }
}
