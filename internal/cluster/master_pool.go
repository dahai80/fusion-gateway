package cluster

// masterPool — #159-C dual-master active-active load balancing.
//
// Fronts N fusion-multi-node masters with least-conn selection + health-check
// failover. Each call (ListNodes/RoutingSummary/...) is routed to the pooled
// master with the fewest in-flight requests among those not circuit-cooled; on
// error the call fails over to the next candidate before returning. A master
// that errors is cooled for coolDuration (exponential backoff up to maxCool),
// during which it is skipped by selection unless no healthy peer remains (then
// it is tried last — fail-open rather than total outage).
//
// Backward-compat: with a single configured address the pool is a 1-element
// wrapper, behaviorally identical to a bare *MasterClient.

import (
    "context"
    "errors"
    "log/slog"
    "net/http"
    "sync"
    "sync/atomic"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/httpx"
)

// masterCoolDuration is the base cooldown applied to a master after a failed
// call. Repeated failures back off up to masterMaxCool.
const (
    masterCoolDuration = 5 * time.Second
    masterMaxCool      = 2 * time.Minute
)

// masterEntry is one pooled master.
type masterEntry struct {
    address string
    // client is the per-master API. *MasterClient in production; tests inject
    // a shim MasterAPI (no network) to drive failover/least-conn assertions.
    client  MasterAPI
    // inFlight counts active calls routed to this master (least-conn key).
    inFlight atomic.Int64
    // coolUntil is the time until which this master is skipped by selection
    // (circuit-cooled after a failure). Zero means healthy/eligible.
    coolUntil atomic.Int64
    // failCount drives exponential backoff of the next cooldown.
    failCount atomic.Int64
}

// masterPool implements MasterAPI across multiple masters.
type masterPool struct {
    entries []*masterEntry
    // rrIndex breaks ties when multiple masters share the same (lowest)
    // in-flight count, giving round-robin spread rather than always-first.
    rrIndex atomic.Uint64
    mu      sync.Mutex
}

// resolveMasterAddresses returns the configured master address list, honoring
// backward-compat: Addresses wins when non-empty, else Address (singular)
// becomes a single-element list. Empty result means no master configured.
func resolveMasterAddresses(cfg config.ClusterMasterConfig) []string {
    if len(cfg.Addresses) > 0 {
        return cfg.Addresses
    }
    if cfg.Address != "" {
        return []string{cfg.Address}
    }
    return nil
}

// NewMasterPool builds a dual-master active-active pool from config. Returns
// nil if no master address is configured (caller falls back to nil master
// client → standalone behavior). Each pooled master gets its own
// TransportForBackend-capped HTTP client (H4 FD-cap parity with the single
// client).
func NewMasterPool(cfg config.ClusterMasterConfig) *masterPool {
    addrs := resolveMasterAddresses(cfg)
    if len(addrs) == 0 {
        return nil
    }
    pool := &masterPool{
        entries: make([]*masterEntry, 0, len(addrs)),
    }
    for _, addr := range addrs {
        mc := &MasterClient{
            address:     addr,
            sharedToken: cfg.SharedToken,
            client: &http.Client{
                Timeout:   10 * time.Second,
                Transport: httpx.TransportForBackend(config.BackendConfig{BaseURL: addr}),
            },
        }
        pool.entries = append(pool.entries, &masterEntry{address: addr, client: mc})
    }
    slog.Info("master pool constructed (#159-C dual-master active-active)",
        "masters", len(addrs), "addresses", addrs)
    return pool
}

// pick returns the best eligible master entry (least in-flight, not cooled).
// If every master is cooled, returns the one with the earliest coolUntil
// (fail-open: a cooled master is preferable to no master at all). Caller must
// call done(e) when the call completes (success or failure).
func (p *masterPool) pick() *masterEntry {
    now := time.Now().UnixNano()
    var best *masterEntry
    var bestInFlight int64 = -1
    var cooledOnly []*masterEntry
    start := int(p.rrIndex.Add(1)-1) % len(p.entries)
    for i := 0; i < len(p.entries); i++ {
        e := p.entries[(start+i)%len(p.entries)]
        if e.coolUntil.Load() > now {
            cooledOnly = append(cooledOnly, e)
            continue
        }
        inf := e.inFlight.Load()
        if best == nil || inf < bestInFlight {
            best = e
            bestInFlight = inf
        }
    }
    if best != nil {
        return best
    }
    // All cooled — fail-open: pick the earliest-to-recover cooled master.
    if len(cooledOnly) > 0 {
        earliest := cooledOnly[0]
        for _, e := range cooledOnly[1:] {
            if e.coolUntil.Load() < earliest.coolUntil.Load() {
                earliest = e
            }
        }
        slog.Warn("master pool all masters cooled, failing open to earliest-recovery master",
            "address", earliest.address)
        return earliest
    }
    return nil
}

// run executes fn against the best master, failing over to the next candidate
// on error. Each candidate is charged in-flight for the duration of its
// attempt. A successful call resets the candidate's fail count (cooldown
// recovers); a failed call cools it (exponential backoff) before failover.
func (p *masterPool) run(ctx context.Context, fn func(MasterAPI) error) error {
    var lastErr error
    for attempt := 0; attempt < len(p.entries); attempt++ {
        e := p.pick()
        if e == nil {
            break
        }
        e.inFlight.Add(1)
        err := fn(e.client)
        e.inFlight.Add(-1)
        if err == nil {
            p.markHealthy(e)
            return nil
        }
        lastErr = err
        p.markFailed(e)
        slog.Warn("master pool call failed, failing over to next master",
            "address", e.address, "attempt", attempt+1, "error", err)
    }
    if lastErr == nil {
        return errors.New("master pool: no eligible master configured")
    }
    return lastErr
}

// markHealthy resets a master's fail count so its next cooldown (if any later
// failure) starts at the base duration again.
func (p *masterPool) markHealthy(e *masterEntry) {
    if e.failCount.Swap(0) != 0 {
        e.coolUntil.Store(0)
        slog.Info("master pool master recovered, cooldown cleared",
            "address", e.address)
    }
}

// markFailed cools a master with exponential backoff: base * 2^(failCount-1),
// capped at masterMaxCool. Subsequent failures lengthen the cooldown; a
// success clears it.
func (p *masterPool) markFailed(e *masterEntry) {
    n := e.failCount.Add(1)
    backoff := masterCoolDuration << uint(n-1)
    if backoff > masterMaxCool || backoff <= 0 {
        backoff = masterMaxCool
    }
    e.coolUntil.Store(time.Now().Add(backoff).UnixNano())
    slog.Warn("master pool master cooled after failure",
        "address", e.address, "fail_count", n, "cooldown", backoff)
}

// inFlightTotal returns the sum of in-flight calls across all pooled masters
// (for metrics/logging).
func (p *masterPool) inFlightTotal() int64 {
    var sum int64
    for _, e := range p.entries {
        sum += e.inFlight.Load()
    }
    return sum
}

// ListNodes routes to the best master with failover.
func (p *masterPool) ListNodes(ctx context.Context) (*MasterNodesResponse, error) {
    var result *MasterNodesResponse
    err := p.run(ctx, func(mc MasterAPI) error {
        r, e := mc.ListNodes(ctx)
        if e != nil {
            return e
        }
        result = r
        return nil
    })
    return result, err
}

// GetNode routes to the best master with failover.
func (p *masterPool) GetNode(ctx context.Context, nodeID string) (*MasterNodeInfo, error) {
    var result *MasterNodeInfo
    err := p.run(ctx, func(mc MasterAPI) error {
        r, e := mc.GetNode(ctx, nodeID)
        if e != nil {
            return e
        }
        result = r
        return nil
    })
    return result, err
}

// SubmitTask routes to the best master with failover.
func (p *masterPool) SubmitTask(ctx context.Context, req *TaskSubmitRequest) (*TaskSubmitResponse, error) {
    var result *TaskSubmitResponse
    err := p.run(ctx, func(mc MasterAPI) error {
        r, e := mc.SubmitTask(ctx, req)
        if e != nil {
            return e
        }
        result = r
        return nil
    })
    return result, err
}

// RoutingSummary routes to the best master with failover.
func (p *masterPool) RoutingSummary(ctx context.Context) (*MasterRoutingSummary, error) {
    var result *MasterRoutingSummary
    err := p.run(ctx, func(mc MasterAPI) error {
        r, e := mc.RoutingSummary(ctx)
        if e != nil {
            return e
        }
        result = r
        return nil
    })
    return result, err
}

// HealthCheck routes to the best master with failover.
func (p *masterPool) HealthCheck(ctx context.Context) error {
    return p.run(ctx, func(mc MasterAPI) error {
        return mc.HealthCheck(ctx)
    })
}
