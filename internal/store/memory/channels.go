package memory

import (
    "fmt"
    "sync"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/store"
)

type ChannelStore struct {
    mu       sync.RWMutex
    channels map[string]*store.ChannelEntry
    onMutate func()
}

func NewChannelStore() *ChannelStore {
    return &ChannelStore{
        channels: make(map[string]*store.ChannelEntry),
    }
}

func (s *ChannelStore) SetOnMutate(fn func()) { s.onMutate = fn }

func (s *ChannelStore) List() ([]*store.ChannelEntry, error) {
    s.mu.RLock()
    defer s.mu.RUnlock()
    result := make([]*store.ChannelEntry, 0, len(s.channels))
    for _, ch := range s.channels {
        result = append(result, ch)
    }
    return result, nil
}

func (s *ChannelStore) Get(name string) (*store.ChannelEntry, error) {
    s.mu.RLock()
    defer s.mu.RUnlock()
    ch, ok := s.channels[name]
    if !ok {
        return nil, fmt.Errorf("channel not found: %s", name)
    }
    return ch, nil
}

func (s *ChannelStore) Create(ch *store.ChannelEntry) error {
    s.mu.Lock()
    if _, exists := s.channels[ch.Name]; exists {
        s.mu.Unlock()
        return fmt.Errorf("channel already exists: %s", ch.Name)
    }
    now := time.Now()
    ch.CreatedAt = now
    ch.UpdatedAt = now
    if ch.Status == "" {
        ch.Status = "online"
    }
    s.channels[ch.Name] = ch
    s.mu.Unlock()
    if s.onMutate != nil {
        s.onMutate()
    }
    return nil
}

func (s *ChannelStore) Update(ch *store.ChannelEntry) error {
    s.mu.Lock()
    if _, exists := s.channels[ch.Name]; !exists {
        s.mu.Unlock()
        return fmt.Errorf("channel not found: %s", ch.Name)
    }
    ch.UpdatedAt = time.Now()
    s.channels[ch.Name] = ch
    s.mu.Unlock()
    if s.onMutate != nil {
        s.onMutate()
    }
    return nil
}

func (s *ChannelStore) Delete(name string) error {
    s.mu.Lock()
    if _, exists := s.channels[name]; !exists {
        s.mu.Unlock()
        return fmt.Errorf("channel not found: %s", name)
    }
    delete(s.channels, name)
    s.mu.Unlock()
    if s.onMutate != nil {
        s.onMutate()
    }
    return nil
}

func (s *ChannelStore) LoadFromConfig(channels map[string]*store.ChannelEntry) {
    s.mu.Lock()
    defer s.mu.Unlock()
    for name, ch := range channels {
        s.channels[name] = ch
    }
}
