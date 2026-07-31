package batch

// Batch processing for v0.5.0 Task #74.
// Importers: internal/server/server.go handleBatchCreate/Get/Cancel.
// User instruction: "按照0.3.0, 0.4.0, 0.5.0顺序全面实施" — PLAN.md: "BatchCreate/BatchGet/BatchCancel, async processing".
// Schema: Batch(id/status/requests/results), BatchStore with background worker.

import (
    "encoding/json"
    "fmt"
    "log/slog"
    "sync"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

type BatchStatus string

const (
    BatchStatusPending   BatchStatus = "pending"
    BatchStatusRunning   BatchStatus = "running"
    BatchStatusCompleted BatchStatus = "completed"
    BatchStatusFailed    BatchStatus = "failed"
    BatchStatusCancelled BatchStatus = "cancelled"
)

type BatchRequest struct {
    CustomID string          `json:"custom_id"`
    Method   string          `json:"method"`
    URL      string          `json:"url"`
    Body     json.RawMessage `json:"body"`
}

type BatchResult struct {
    CustomID string         `json:"custom_id"`
    Response *BatchResponse `json:"response,omitempty"`
    Error    string         `json:"error,omitempty"`
}

type BatchResponse struct {
    StatusCode int             `json:"status_code"`
    Body       json.RawMessage `json:"body"`
}

type Batch struct {
    ID               string         `json:"id"`
    Status           BatchStatus    `json:"status"`
    Requests         []BatchRequest `json:"requests"`
    Results          []BatchResult  `json:"results,omitempty"`
    Total            int            `json:"total"`
    Completed        int            `json:"completed"`
    Failed           int            `json:"failed"`
    CreatedAt        time.Time      `json:"created_at"`
    CompletedAt      *time.Time     `json:"completed_at,omitempty"`
    Endpoint         string         `json:"endpoint,omitempty"`
    CompletionWindow string         `json:"completion_window,omitempty"`
}

type ProcessFn func(req BatchRequest) BatchResult

type Store struct {
    mu        sync.RWMutex
    batches   map[string]*Batch
    processFn ProcessFn
    maxBatch  int
}

func NewStore(cfg config.BatchConfig, processFn ProcessFn) *Store {
    if !cfg.Enabled {
        return nil
    }
    maxBatch := cfg.MaxBatchSize
    if maxBatch <= 0 {
        maxBatch = 100
    }
    if processFn == nil {
        processFn = defaultProcessFn
    }
    s := &Store{
        batches:   make(map[string]*Batch),
        processFn: processFn,
        maxBatch:  maxBatch,
    }
    slog.Info("batch store initialized", "max_batch_size", maxBatch)
    return s
}

func defaultProcessFn(req BatchRequest) BatchResult {
    return BatchResult{
        CustomID: req.CustomID,
        Error:    "no processor configured",
    }
}

func (s *Store) Create(requests []BatchRequest, endpoint, window string) (*Batch, error) {
    if s == nil {
        return nil, fmt.Errorf("batch not enabled")
    }
    if len(requests) > s.maxBatch {
        return nil, fmt.Errorf("batch size %d exceeds max %d", len(requests), s.maxBatch)
    }
    id := fmt.Sprintf("batch_%d", time.Now().UnixNano())
    b := &Batch{
        ID:               id,
        Status:           BatchStatusPending,
        Requests:         requests,
        Results:          make([]BatchResult, 0),
        Total:            len(requests),
        CreatedAt:        time.Now(),
        Endpoint:         endpoint,
        CompletionWindow: window,
    }
    s.mu.Lock()
    s.batches[id] = b
    s.mu.Unlock()
    go s.process(b)
    slog.Info("batch created", "id", id, "total", len(requests))
    return b, nil
}

func (s *Store) process(b *Batch) {
    s.mu.Lock()
    b.Status = BatchStatusRunning
    s.mu.Unlock()

    for _, req := range b.Requests {
        s.mu.RLock()
        if b.Status == BatchStatusCancelled {
            s.mu.RUnlock()
            break
        }
        s.mu.RUnlock()

        result := s.processFn(req)
        s.mu.Lock()
        b.Results = append(b.Results, result)
        b.Completed++
        if result.Error != "" {
            b.Failed++
        }
        s.mu.Unlock()
    }

    s.mu.Lock()
    if b.Status != BatchStatusCancelled {
        b.Status = BatchStatusCompleted
        now := time.Now()
        b.CompletedAt = &now
    }
    s.mu.Unlock()
    slog.Info("batch processed", "id", b.ID, "completed", b.Completed, "failed", b.Failed)
}

func (s *Store) Get(id string) (*Batch, error) {
    if s == nil {
        return nil, fmt.Errorf("batch not enabled")
    }
    s.mu.RLock()
    defer s.mu.RUnlock()
    b, ok := s.batches[id]
    if !ok {
        return nil, fmt.Errorf("batch %s not found", id)
    }
    return b, nil
}

func (s *Store) Cancel(id string) (*Batch, error) {
    if s == nil {
        return nil, fmt.Errorf("batch not enabled")
    }
    s.mu.Lock()
    defer s.mu.Unlock()
    b, ok := s.batches[id]
    if !ok {
        return nil, fmt.Errorf("batch %s not found", id)
    }
    if b.Status == BatchStatusCompleted || b.Status == BatchStatusCancelled {
        return b, fmt.Errorf("batch already %s", b.Status)
    }
    b.Status = BatchStatusCancelled
    now := time.Now()
    b.CompletedAt = &now
    slog.Info("batch cancelled", "id", id)
    return b, nil
}

func (s *Store) List() []*Batch {
    if s == nil {
        return nil
    }
    s.mu.RLock()
    defer s.mu.RUnlock()
    result := make([]*Batch, 0, len(s.batches))
    for _, b := range s.batches {
        result = append(result, b)
    }
    return result
}
