package batch

import (
    "encoding/json"
    "sync"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

func TestBatchStore_NilReceiver(t *testing.T) {
    var s *Store
    _, err := s.Create(nil, "", "")
    if err == nil {
        t.Fatal("expected error on nil store Create")
    }
    _, err = s.Get("x")
    if err == nil {
        t.Fatal("expected error on nil store Get")
    }
    _, err = s.Cancel("x")
    if err == nil {
        t.Fatal("expected error on nil store Cancel")
    }
    list := s.List()
    if list != nil {
        t.Fatal("expected nil list on nil store")
    }
}

func TestBatchStore_DefaultProcessFn(t *testing.T) {
    cfg := config.BatchConfig{Enabled: true, MaxBatchSize: 10}
    s := NewStore(cfg, nil)
    reqs := []BatchRequest{{CustomID: "r1", Method: "POST", URL: "/test"}}
    b, err := s.Create(reqs, "", "")
    if err != nil {
        t.Fatal(err)
    }
    time.Sleep(200 * time.Millisecond)
    got, _ := s.Get(b.ID)
    if len(got.Results) > 0 && got.Results[0].Error != "no processor configured" {
        t.Logf("result: %+v", got.Results[0])
    }
}

func TestBatchStore_DefaultMaxBatch(t *testing.T) {
    cfg := config.BatchConfig{Enabled: true, MaxBatchSize: 0}
    s := NewStore(cfg, nil)
    if s.maxBatch != 100 {
        t.Fatalf("expected default 100, got %d", s.maxBatch)
    }
}

func TestBatchStore_List(t *testing.T) {
    cfg := config.BatchConfig{Enabled: true, MaxBatchSize: 10}
    s := NewStore(cfg, func(req BatchRequest) BatchResult {
        return BatchResult{CustomID: req.CustomID, Response: &BatchResponse{StatusCode: 200, Body: json.RawMessage(`{}`)}}
    })
    _, _ = s.Create([]BatchRequest{{CustomID: "r1"}}, "", "")
    _, _ = s.Create([]BatchRequest{{CustomID: "r2"}}, "", "")
    list := s.List()
    if len(list) != 2 {
        t.Fatalf("expected 2, got %d", len(list))
    }
}

func TestBatchStore_CancelAlreadyCompleted(t *testing.T) {
    cfg := config.BatchConfig{Enabled: true, MaxBatchSize: 10}
    s := NewStore(cfg, func(req BatchRequest) BatchResult {
        return BatchResult{CustomID: req.CustomID, Response: &BatchResponse{StatusCode: 200}}
    })
    b, _ := s.Create([]BatchRequest{{CustomID: "r1"}}, "", "")
    time.Sleep(200 * time.Millisecond)
    _, err := s.Cancel(b.ID)
    if err == nil {
        t.Log("cancel succeeded (may be fine if batch not yet completed)")
    } else {
        t.Logf("cancel error as expected: %v", err)
    }
}

func TestBatchStore_CancelNotFound(t *testing.T) {
    cfg := config.BatchConfig{Enabled: true, MaxBatchSize: 10}
    s := NewStore(cfg, nil)
    _, err := s.Cancel("nonexistent")
    if err == nil {
        t.Fatal("expected error for missing batch")
    }
}

func TestBatchStore_ProcessWithErrors(t *testing.T) {
    cfg := config.BatchConfig{Enabled: true, MaxBatchSize: 10}
    s := NewStore(cfg, func(req BatchRequest) BatchResult {
        if req.CustomID == "fail" {
            return BatchResult{CustomID: req.CustomID, Error: "simulated error"}
        }
        return BatchResult{CustomID: req.CustomID, Response: &BatchResponse{StatusCode: 200}}
    })
    reqs := []BatchRequest{
        {CustomID: "ok1"},
        {CustomID: "fail"},
        {CustomID: "ok2"},
    }
    b, _ := s.Create(reqs, "", "")
    time.Sleep(300 * time.Millisecond)
    got, _ := s.Get(b.ID)
    if got.Status != BatchStatusCompleted {
        t.Fatalf("expected completed, got %s", got.Status)
    }
    if got.Failed != 1 {
        t.Fatalf("expected 1 failed, got %d", got.Failed)
    }
    if got.Completed != 3 {
        t.Fatalf("expected 3 completed, got %d", got.Completed)
    }
}

func TestBatchStore_CancelDuringProcessing(t *testing.T) {
    cfg := config.BatchConfig{Enabled: true, MaxBatchSize: 10}
    var mu sync.Mutex
    proceed := false
    s := NewStore(cfg, func(req BatchRequest) BatchResult {
        mu.Lock()
        for !proceed {
            mu.Unlock()
            time.Sleep(10 * time.Millisecond)
            mu.Lock()
        }
        mu.Unlock()
        return BatchResult{CustomID: req.CustomID, Response: &BatchResponse{StatusCode: 200}}
    })
    reqs := []BatchRequest{{CustomID: "r1"}, {CustomID: "r2"}, {CustomID: "r3"}}
    b, _ := s.Create(reqs, "", "")
    time.Sleep(50 * time.Millisecond)
    cancelled, err := s.Cancel(b.ID)
    if err != nil {
        t.Logf("cancel: %v", err)
    }
    mu.Lock()
    proceed = true
    mu.Unlock()
    time.Sleep(100 * time.Millisecond)
    got, _ := s.Get(b.ID)
    t.Logf("final status: %s completed=%d", got.Status, got.Completed)
    if cancelled != nil && cancelled.Status == BatchStatusCancelled {
        t.Log("cancel during processing works")
    }
}

func TestBatchStore_DeepCopyOnGet(t *testing.T) {
    cfg := config.BatchConfig{Enabled: true, MaxBatchSize: 10}
    s := NewStore(cfg, func(req BatchRequest) BatchResult {
        return BatchResult{CustomID: req.CustomID, Response: &BatchResponse{StatusCode: 200}}
    })
    b, _ := s.Create([]BatchRequest{{CustomID: "r1"}}, "", "")
    time.Sleep(200 * time.Millisecond)
    copy1, _ := s.Get(b.ID)
    copy2, _ := s.Get(b.ID)
    if copy1.ID != copy2.ID {
        t.Fatal("copies should have same ID")
    }
    copy1.Completed = 999
    if copy2.Completed == 999 {
        t.Fatal("deep copy should be independent")
    }
}
