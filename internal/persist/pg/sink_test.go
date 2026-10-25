package pg

import (
    "context"
    "errors"
    "sync"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/persist"
    "github.com/fusion-gateway/fusion-gateway/internal/store"
)

type fakeWriter struct {
    mu       sync.Mutex
    batches  [][]*persist.Entry
    failNext int // number of consecutive WriteBatch calls to fail
    pingErr  error
}

func (f *fakeWriter) WriteBatch(ctx context.Context, entries []*persist.Entry) error {
    f.mu.Lock()
    defer f.mu.Unlock()
    if f.failNext > 0 {
        f.failNext--
        return errors.New("fake pg outage")
    }
    cp := make([]*persist.Entry, len(entries))
    copy(cp, entries)
    f.batches = append(f.batches, cp)
    return nil
}

func (f *fakeWriter) Ping(ctx context.Context) error { return f.pingErr }
func (f *fakeWriter) Close()                          {}

func (f *fakeWriter) total() int {
    f.mu.Lock()
    defer f.mu.Unlock()
    n := 0
    for _, b := range f.batches {
        n += len(b)
    }
    return n
}

func testCfg(capacity, batch int, flushMS int) config.PersistenceConfig {
    return config.PersistenceConfig{
        Enabled: true,
        Postgres: config.PersistencePostgresConfig{DsnEnv: "TEST_PG_DSN", MaxOpenConns: 2, MaxIdleConns: 1, ConnTimeoutSecs: 1},
        Queue:   config.PersistenceQueueConfig{Capacity: capacity, BatchSize: batch, FlushIntervalMS: flushMS},
        Capture: config.PersistenceCaptureConfig{MaxBodyBytes: 1024},
        Retention: config.PersistenceRetentionConfig{RequestLogsDays: 90, RequestPayloadsDays: 30},
    }
}

func entryFor(i int) *persist.Entry {
    return &persist.Entry{
        Log: store.RequestLog{RequestID: string(rune('a' + i%26)), Model: "m"},
    }
}

func TestSink_FlushByInterval(t *testing.T) {
    fw := &fakeWriter{}
    s := newSinkWithWriter(testCfg(16, 100, 50), fw)
    s.Start(context.Background())
    defer s.Close(context.Background())

    for i := 0; i < 3; i++ {
        if err := s.Enqueue(entryFor(i)); err != nil {
            t.Fatalf("enqueue %d: %v", i, err)
        }
    }
    // below batch size — the interval tick must flush anyway
    deadline := time.Now().Add(2 * time.Second)
    for fw.total() < 3 && time.Now().Before(deadline) {
        time.Sleep(20 * time.Millisecond)
    }
    if fw.total() != 3 {
        t.Fatalf("wrote %d entries, want 3 (interval flush)", fw.total())
    }
}

func TestSink_FlushByBatchSize(t *testing.T) {
    fw := &fakeWriter{}
    // 1s flush interval, batch 4 — 5 entries must flush on batch size, not wait
    s := newSinkWithWriter(testCfg(64, 4, 1000), fw)
    s.Start(context.Background())
    defer s.Close(context.Background())

    for i := 0; i < 4; i++ {
        s.Enqueue(entryFor(i))
    }
    deadline := time.Now().Add(1 * time.Second)
    for fw.total() < 4 && time.Now().Before(deadline) {
        time.Sleep(10 * time.Millisecond)
    }
    if fw.total() != 4 {
        t.Fatalf("wrote %d entries, want 4 flushed by batch size before interval", fw.total())
    }
}

func TestSink_QueueFullKeepsNew(t *testing.T) {
    fw := &fakeWriter{}
    // tiny queue, NO workers started: Enqueue's keep-new eviction is
    // deterministic without a flusher racing the queue
    s := newSinkWithWriter(testCfg(4, 100, 60_000), fw)

    for i := 0; i < 6; i++ {
        if err := s.Enqueue(entryFor(i)); err != nil {
            t.Fatalf("enqueue %d must evict-oldest, not error: %v", i, err)
        }
    }
    enq, _, dropped := s.Stats()
    if enq != 6 {
        t.Fatalf("enqueued = %d, want 6", enq)
    }
    if dropped != 2 {
        t.Fatalf("dropped = %d, want 2 (oldest evicted)", dropped)
    }
    // the queue must hold the NEWEST 4: drain and inspect ids
    var ids []string
    for {
        select {
        case e := <-s.queue:
            ids = append(ids, e.Log.RequestID)
        default:
            goto check
        }
    }
check:
    if len(ids) != 4 {
        t.Fatalf("queue holds %d, want 4", len(ids))
    }
    for i, want := range []string{"c", "d", "e", "f"} {
        if ids[i] != want {
            t.Fatalf("queue[%d] = %q, want %q (newest kept)", i, ids[i], want)
        }
    }
}

func TestSink_WriteFailureRetries(t *testing.T) {
    fw := &fakeWriter{failNext: 1}
    s := newSinkWithWriter(testCfg(16, 100, 50), fw)
    s.Start(context.Background())
    defer s.Close(context.Background())

    s.Enqueue(entryFor(0))
    // first write fails, second (after backoff) succeeds
    deadline := time.Now().Add(3 * time.Second)
    for fw.total() < 1 && time.Now().Before(deadline) {
        time.Sleep(50 * time.Millisecond)
    }
    if fw.total() != 1 {
        t.Fatalf("wrote %d after retry, want 1", fw.total())
    }
}

func TestSink_CloseDrains(t *testing.T) {
    fw := &fakeWriter{}
    // long interval so nothing flushes before Close
    s := newSinkWithWriter(testCfg(16, 100, 60_000), fw)
    s.Start(context.Background())

    for i := 0; i < 5; i++ {
        s.Enqueue(entryFor(i))
    }
    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()
    if err := s.Close(ctx); err != nil {
        t.Fatalf("close: %v", err)
    }
    if got := fw.total(); got != 5 {
        t.Fatalf("shutdown drain wrote %d, want 5", got)
    }
    // Enqueue after Close must refuse, not panic
    if err := s.Enqueue(entryFor(99)); !errors.Is(err, persist.ErrDropped) {
        t.Fatalf("post-close enqueue = %v, want ErrDropped", err)
    }
}
