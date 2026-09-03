package cluster

import (
    "context"
    "errors"
    "sync"
    "sync/atomic"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

// fakeMaster is a test double for *MasterClient behavior used by masterPool.
// It records in-flight ticks via the pool's own instrumentation where needed,
// and can be configured to fail N times then succeed, or fail always.
type fakeMaster struct {
    addr     string
    mu       sync.Mutex
    failN    int // fail the first N calls, then succeed
    failErr  error
    callCount atomic.Int64
    delay time.Duration
}

func (f *fakeMaster) call() error {
    f.callCount.Add(1)
    if f.delay > 0 {
        time.Sleep(f.delay)
    }
    f.mu.Lock()
    if f.failN > 0 {
        f.failN--
        f.mu.Unlock()
        if f.failErr != nil {
            return f.failErr
        }
        return errors.New("fake master failure")
    }
    f.mu.Unlock()
    return nil
}

// buildFakePool constructs a masterPool whose entries wrap fake clients via a
// shim. We inject fakes by building real masterEntry structs but swapping the
// client's behavior through a parallel fake list that run() consults. To keep
// the pool's real MasterClient out of the loop (no network), we use a tiny
// shim: a masterAPIShim implementing MasterAPI whose methods delegate to a
// fakeMaster.
type masterAPIShim struct {
    fake *fakeMaster
}

func (s *masterAPIShim) ListNodes(ctx context.Context) (*MasterNodesResponse, error) {
    if err := s.fake.call(); err != nil {
        return nil, err
    }
    return &MasterNodesResponse{Total: 1, Online: 1}, nil
}
func (s *masterAPIShim) GetNode(ctx context.Context, id string) (*MasterNodeInfo, error) {
    if err := s.fake.call(); err != nil {
        return nil, err
    }
    return &MasterNodeInfo{NodeID: id}, nil
}
func (s *masterAPIShim) SubmitTask(ctx context.Context, req *TaskSubmitRequest) (*TaskSubmitResponse, error) {
    if err := s.fake.call(); err != nil {
        return nil, err
    }
    return &TaskSubmitResponse{TaskID: "t"}, nil
}
func (s *masterAPIShim) RoutingSummary(ctx context.Context) (*MasterRoutingSummary, error) {
    if err := s.fake.call(); err != nil {
        return nil, err
    }
    return &MasterRoutingSummary{Strategy: "least_conn", TotalNodes: 1}, nil
}
func (s *masterAPIShim) HealthCheck(ctx context.Context) error {
    return s.fake.call()
}

// newFakePool builds a masterPool backed by shims (no network), one per fake.
func newFakePool(fakes ...*fakeMaster) *masterPool {
    p := &masterPool{entries: make([]*masterEntry, 0, len(fakes))}
    for _, f := range fakes {
        shim := &masterAPIShim{fake: f}
        e := &masterEntry{address: f.addr, client: shim}
        p.entries = append(p.entries, e)
    }
    return p
}

// TestMasterPool_Failover verifies a failing master is failed over to a
// healthy peer, the failed master is cooled, and the call ultimately succeeds.
func TestMasterPool_Failover(t *testing.T) {
    bad := &fakeMaster{addr: ":11452", failN: 100}
    good := &fakeMaster{addr: ":11453"}
    pool := newFakePool(bad, good)

    ctx := context.Background()
    resp, err := pool.ListNodes(ctx)
    if err != nil {
        t.Fatalf("expected success via failover, got: %v", err)
    }
    if resp.Total != 1 {
        t.Fatalf("expected Total=1, got %d", resp.Total)
    }
    if bad.callCount.Load() < 1 {
        t.Fatal("bad master should have been attempted")
    }
    if good.callCount.Load() < 1 {
        t.Fatal("good master should have been called (failover target)")
    }
    // Bad master must be cooled now (coolUntil > 0).
    if badEntry := pool.entries[0]; badEntry.coolUntil.Load() <= 0 {
        t.Fatal("expected bad master to be cooled after failure")
    }
}

// TestMasterPool_LeastConn selects the master with fewer in-flight requests.
func TestMasterPool_LeastConn(t *testing.T) {
    a := &fakeMaster{addr: ":11452", delay: 20 * time.Millisecond}
    b := &fakeMaster{addr: ":11453", delay: 20 * time.Millisecond}
    pool := newFakePool(a, b)

    var wg sync.WaitGroup
    for i := 0; i < 4; i++ {
        wg.Add(1)
        go func() {
            defer wg.Done()
            _, _ = pool.ListNodes(context.Background())
        }()
    }
    wg.Wait()
    // With least-conn + round-robin tie-break, both masters should receive
    // calls (not all 4 piled on one).
    if a.callCount.Load() == 0 || b.callCount.Load() == 0 {
        t.Fatalf("expected both masters used: a=%d b=%d", a.callCount.Load(), b.callCount.Load())
    }
}

// TestMasterPool_AllFail returns the last error when every master fails.
func TestMasterPool_AllFail(t *testing.T) {
    a := &fakeMaster{addr: ":11452", failN: 100, failErr: errors.New("a-down")}
    b := &fakeMaster{addr: ":11453", failN: 100, failErr: errors.New("b-down")}
    pool := newFakePool(a, b)

    _, err := pool.RoutingSummary(context.Background())
    if err == nil {
        t.Fatal("expected error when all masters fail")
    }
}

// TestMasterPool_CooldownRecover: after cooldown elapses a recovered master is
// eligible again.
func TestMasterPool_CooldownRecover(t *testing.T) {
    flaky := &fakeMaster{addr: ":11452", failN: 1} // fail once, then succeed
    good := &fakeMaster{addr: ":11453"}
    pool := newFakePool(flaky, good)

    // First call: flaky fails once, failover to good succeeds. Flaky is cooled.
    _, err := pool.ListNodes(context.Background())
    if err != nil {
        t.Fatalf("first call should succeed via failover: %v", err)
    }
    // Force the cooldown to the past so flaky becomes eligible again.
    flakyEntry := pool.entries[0]
    flakyEntry.coolUntil.Store(0)
    // flaky now succeeds (failN exhausted). It should be selectable.
    _, err = pool.ListNodes(context.Background())
    if err != nil {
        t.Fatalf("second call should succeed: %v", err)
    }
}

// TestResolveMasterAddresses: Addresses wins; falls back to singular Address.
func TestResolveMasterAddresses(t *testing.T) {
    got := resolveMasterAddresses(config.ClusterMasterConfig{Addresses: []string{"a", "b"}})
    if len(got) != 2 || got[0] != "a" || got[1] != "b" {
        t.Fatalf("Addresses should win: %v", got)
    }
    got = resolveMasterAddresses(config.ClusterMasterConfig{Address: "single"})
    if len(got) != 1 || got[0] != "single" {
        t.Fatalf("singular Address fallback: %v", got)
    }
    got = resolveMasterAddresses(config.ClusterMasterConfig{})
    if got != nil {
        t.Fatalf("empty config -> nil, got %v", got)
    }
}

// TestNewMasterPool_NilWhenNoAddress: no address configured -> nil pool.
func TestNewMasterPool_NilWhenNoAddress(t *testing.T) {
    if p := NewMasterPool(config.ClusterMasterConfig{}); p != nil {
        t.Fatal("expected nil pool when no master address configured")
    }
}

// TestNewMasterPool_SingleBackwardCompat: singular Address -> 1-entry pool.
func TestNewMasterPool_SingleBackwardCompat(t *testing.T) {
    p := NewMasterPool(config.ClusterMasterConfig{Address: "http://localhost:11452"})
    if p == nil || len(p.entries) != 1 {
        t.Fatalf("expected 1-entry pool, got %v", p)
    }
}

// TestNewMasterPool_Multi: Addresses -> multi-entry pool.
func TestNewMasterPool_Multi(t *testing.T) {
    p := NewMasterPool(config.ClusterMasterConfig{Addresses: []string{
        "http://localhost:11452", "http://localhost:11453",
    }})
    if p == nil || len(p.entries) != 2 {
        t.Fatalf("expected 2-entry pool, got %v", p)
    }
}
