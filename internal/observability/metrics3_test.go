package observability

import (
    "sync"
    "testing"
)

func TestSuccessRate_LocalZeroTotal(t *testing.T) {
    savedS := localSuccesses.Load()
    savedF := localFailures.Load()
    localSuccesses.Store(0)
    localFailures.Store(0)
    defer func() {
        localSuccesses.Store(savedS)
        localFailures.Store(savedF)
    }()
    rate := SuccessRate("local")
    if rate != 1.0 {
        t.Fatalf("expected 1.0 for zero local requests, got %f", rate)
    }
}

func TestSuccessRate_CloudWithMixedResults(t *testing.T) {
    savedS := cloudSuccesses.Load()
    savedF := cloudFailures.Load()
    defer func() {
        cloudSuccesses.Store(savedS)
        cloudFailures.Store(savedF)
    }()
    cloudSuccesses.Store(3)
    cloudFailures.Store(1)
    rate := SuccessRate("cloud")
    expected := 3.0 / 4.0
    if rate != expected {
        t.Fatalf("expected %f, got %f", expected, rate)
    }
}

func TestSuccessRate_LocalWithMixedResults(t *testing.T) {
    savedS := localSuccesses.Load()
    savedF := localFailures.Load()
    defer func() {
        localSuccesses.Store(savedS)
        localFailures.Store(savedF)
    }()
    localSuccesses.Store(7)
    localFailures.Store(3)
    rate := SuccessRate("local")
    expected := 7.0 / 10.0
    if rate != expected {
        t.Fatalf("expected %f, got %f", expected, rate)
    }
}

func TestRecordRequest_MultipleStatuses(t *testing.T) {
    beforeTotal, beforeLocal, beforeCloud := Stats()
    RecordRequest("local", "m1", "success")
    RecordRequest("local", "m1", "error")
    RecordRequest("local", "m1", "timeout")
    RecordRequest("cloud", "m2", "success")
    RecordRequest("cloud", "m2", "error")
    RecordRequest("cloud", "m2", "timeout")
    total, local, cloud := Stats()
    if total != beforeTotal+6 {
        t.Fatalf("expected total=%d, got %d", beforeTotal+6, total)
    }
    if local != beforeLocal+3 {
        t.Fatalf("expected local=%d, got %d", beforeLocal+3, local)
    }
    if cloud != beforeCloud+3 {
        t.Fatalf("expected cloud=%d, got %d", beforeCloud+3, cloud)
    }
}

func TestStats_ConcurrentAccess(t *testing.T) {
    var wg sync.WaitGroup
    for i := 0; i < 100; i++ {
        wg.Add(1)
        go func() {
            defer wg.Done()
            RecordRequest("local", "concurrent-model", "success")
        }()
    }
    wg.Wait()
    _, _, _ = Stats()
}
