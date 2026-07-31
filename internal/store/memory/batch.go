package memory

import (
    "fmt"
    "log/slog"
    "sync"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/store"
)

type BatchSubStore struct {
    mu      sync.RWMutex
    batches map[string]*store.Batch
    maxBatch int
}

func NewBatchSubStore(maxBatch int) *BatchSubStore {
    if maxBatch <= 0 {
        maxBatch = 100
    }
    return &BatchSubStore{
        batches:  make(map[string]*store.Batch),
        maxBatch: maxBatch,
    }
}

func (s *BatchSubStore) Create(requests []store.BatchRequest, endpoint, window string) (*store.Batch, error) {
    if len(requests) > s.maxBatch {
        return nil, fmt.Errorf("batch size %d exceeds max %d", len(requests), s.maxBatch)
    }
    id := fmt.Sprintf("batch_%d", time.Now().UnixNano())
    b := &store.Batch{
        ID:               id,
        Status:           store.BatchStatusPending,
        Requests:         requests,
        Results:          make([]store.BatchResult, 0),
        Total:            len(requests),
        CreatedAt:        time.Now(),
        Endpoint:         endpoint,
        CompletionWindow: window,
    }
    s.mu.Lock()
    s.batches[id] = b
    s.mu.Unlock()
    slog.Info("batch created", "id", id, "total", len(requests))
    return b, nil
}

func (s *BatchSubStore) Get(id string) (*store.Batch, error) {
    s.mu.RLock()
    defer s.mu.RUnlock()
    b, ok := s.batches[id]
    if !ok {
        return nil, fmt.Errorf("batch %s not found", id)
    }
    cp := *b
    cp.Results = make([]store.BatchResult, len(b.Results))
    copy(cp.Results, b.Results)
    cp.Requests = make([]store.BatchRequest, len(b.Requests))
    copy(cp.Requests, b.Requests)
    return &cp, nil
}

func (s *BatchSubStore) List() []*store.Batch {
    s.mu.RLock()
    defer s.mu.RUnlock()
    result := make([]*store.Batch, 0, len(s.batches))
    for _, b := range s.batches {
        result = append(result, b)
    }
    return result
}

func (s *BatchSubStore) Cancel(id string) (*store.Batch, error) {
    s.mu.Lock()
    defer s.mu.Unlock()
    b, ok := s.batches[id]
    if !ok {
        return nil, fmt.Errorf("batch %s not found", id)
    }
    if b.Status == store.BatchStatusCompleted || b.Status == store.BatchStatusCancelled {
        return b, fmt.Errorf("batch already %s", b.Status)
    }
    b.Status = store.BatchStatusCancelled
    now := time.Now()
    b.CompletedAt = &now
    slog.Info("batch cancelled", "id", id)
    return b, nil
}

func (s *BatchSubStore) Update(batch *store.Batch) error {
    s.mu.Lock()
    defer s.mu.Unlock()
    if _, ok := s.batches[batch.ID]; !ok {
        return fmt.Errorf("batch %s not found", batch.ID)
    }
    s.batches[batch.ID] = batch
    return nil
}
