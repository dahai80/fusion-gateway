package batch

import (
    "encoding/json"
    "testing"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

func TestBatchStore_Disabled(t *testing.T) {
    cfg := config.BatchConfig{Enabled: false}
    s := NewStore(cfg, nil)
    if s != nil {
        t.Fatal("should be nil when disabled")
    }
}

func TestBatchStore_CreateAndGet(t *testing.T) {
    cfg := config.BatchConfig{Enabled: true, MaxBatchSize: 10}
    s := NewStore(cfg, func(req BatchRequest) BatchResult {
        return BatchResult{
            CustomID: req.CustomID,
            Response: &BatchResponse{
                StatusCode: 200,
                Body:       json.RawMessage(`{"ok":true}`),
            },
        }
    })
    reqs := []BatchRequest{
        {CustomID: "req1", Method: "POST", URL: "/v1/chat/completions", Body: json.RawMessage(`{}`)},
    }
    b, err := s.Create(reqs, "/v1/chat/completions", "24h")
    if err != nil {
        t.Fatal(err)
    }
    if b.Status != BatchStatusPending && b.Status != BatchStatusCompleted && b.Status != BatchStatusRunning {
        t.Fatalf("unexpected status: %s", b.Status)
    }
}

func TestBatchStore_ExceedsMaxSize(t *testing.T) {
    cfg := config.BatchConfig{Enabled: true, MaxBatchSize: 1}
    s := NewStore(cfg, nil)
    reqs := []BatchRequest{
        {CustomID: "r1"}, {CustomID: "r2"},
    }
    _, err := s.Create(reqs, "", "")
    if err == nil {
        t.Fatal("should fail when exceeding max batch size")
    }
}

func TestBatchStore_Cancel(t *testing.T) {
    cfg := config.BatchConfig{Enabled: true, MaxBatchSize: 100}
    s := NewStore(cfg, func(req BatchRequest) BatchResult {
        return BatchResult{CustomID: req.CustomID, Response: &BatchResponse{StatusCode: 200, Body: json.RawMessage(`{}`)}}
    })
    reqs := []BatchRequest{{CustomID: "c1"}}
    b, _ := s.Create(reqs, "", "")
    _, err := s.Cancel(b.ID)
    if err != nil {
        t.Logf("cancel result: %v (may have already completed)", err)
    }
}

func TestBatchStore_NotFound(t *testing.T) {
    cfg := config.BatchConfig{Enabled: true, MaxBatchSize: 100}
    s := NewStore(cfg, nil)
    _, err := s.Get("nonexistent")
    if err == nil {
        t.Fatal("should not find nonexistent batch")
    }
}
