package memory

import (
    "sync"
    "time"
)

// debouncedPersister is the A2 (#117) shared coalesced-write primitive. It
// collapses a burst of high-frequency mutations into a single atomic write after
// a debounce window, with a synchronous Flush for shutdown. Pre-#117 the same
// timer+lock+AfterFunc block was duplicated verbatim in QuotaStore (per-key
// quota) and TeamsStore (team quota/cost); this extracts one reusable helper so
// the two stores share one strategy and a third consumer can adopt it without
// re-rolling the pattern.
//
// Contract: Schedule arms (or resets) a debounce timer; when it fires it calls
// the installed write callback once. Flush stops any pending timer and runs the
// callback synchronously so the last burst before shutdown reaches disk. A nil
// callback makes Schedule a no-op (memory-only / pre-wiring). The callback runs
// outside the internal lock so the atomic file write does not contend with the
// store's data lock (same property the duplicated blocks preserved).
//
// NOT applied to metadata CRUD (KeyStore/ChannelStore): a deleted key must be
// crash-durable the instant Delete returns, so debouncing metadata would let a
// crash resurrect a revoked key (security regression). Metadata stays on the
// per-call immediate onMutate path; only the high-frequency quota paths — where
// losing at most one debounce window of cost on crash is acceptable — debounce.
// This trade-off is documented in the #117 issue and the memory/redis parity
// note below.
type debouncedPersister struct {
    cb    func()
    d     time.Duration
    mu    sync.Mutex
    timer *time.Timer
    name  string
}

// newDebouncedPersister builds a coalesced-write helper. cb is the atomic write
// (e.g. SaveQuota / SaveTeams); d is the debounce window (defaults to 2s). name
// labels the timer goroutine in logs.
func newDebouncedPersister(name string, d time.Duration, cb func()) *debouncedPersister {
    if d <= 0 {
        d = 2 * time.Second
    }
    return &debouncedPersister{cb: cb, d: d, name: name}
}

// Schedule arms (first call) or resets (subsequent calls within the window) the
// debounce timer. No-op when cb is nil (memory-only). The write fires once after
// d elapses with no further Schedule call.
func (p *debouncedPersister) Schedule() {
    if p == nil || p.cb == nil {
        return
    }
    p.mu.Lock()
    defer p.mu.Unlock()
    if p.timer != nil {
        p.timer.Reset(p.d)
        return
    }
    p.timer = time.AfterFunc(p.d, func() {
        p.mu.Lock()
        p.timer = nil
        p.mu.Unlock()
        p.cb()
    })
}

// Flush drains any pending debounced write synchronously so the last burst
// before shutdown reaches disk. Stops the pending timer (if armed) and runs cb.
func (p *debouncedPersister) Flush() {
    if p == nil {
        return
    }
    p.mu.Lock()
    if p.timer != nil {
        p.timer.Stop()
        p.timer = nil
    }
    p.mu.Unlock()
    if p.cb != nil {
        p.cb()
    }
}

// Pending reports whether a debounced write is armed but not yet fired. Test
// hook for guard assertions (a Flush after Schedule must show Pending=false).
func (p *debouncedPersister) Pending() bool {
    if p == nil {
        return false
    }
    p.mu.Lock()
    defer p.mu.Unlock()
    return p.timer != nil
}

// persistParityNote records the memory/redis write-profile contract from #117.
// Kept as a var so godoc + grep land here.
var persistParityNote = `
memory/redis write-profile parity (A2/#117):
- Team quota (AddCost):   memory = debounced SaveTeams;   redis = per-call SET (full teams JSON).
- Per-key quota (Deduct): memory = debounced SaveQuota;   redis = per-call SET (full quota JSON).
Both survive restart. memory coalesces high-frequency cost bursts into one
atomic write per debounce window; redis does not (per-call SET). This asymmetry
is intentional for single-node scale: the in-memory store is the coalesced-
optimized backend, redis is the externally-shared low-latency backend whose
per-call SET is the simpler correctness floor. Aligning redis to debounce would
add a second coalescing layer with cross-node ordering concerns not justified at
single-node scale. See audit/fusion-gateway-audit-0826.md finding A2.
`
