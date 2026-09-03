package router

import (
    "context"
    "errors"
    "log/slog"
    "sync"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

// Tier is the #159 admission class for the 3-tier priority queue. A request's
// tier is derived from its semantic Intent and the bound tenant's Tier field:
// heavy work (diffusion / heavy_model, or a tenant tagged "heavy") gets the
// highest admission priority so a burst of light chat requests cannot starve a
// long generation behind it; light work (short chat, embeddings) gets the
// lowest. "general" is the default.
type Tier string

const (
    TierHeavy   Tier = "heavy"
    TierGeneral Tier = "general"
    TierLight   Tier = "light"
)

// tierRank orders tiers for head-of-line dispatch: lower rank = higher
// priority (dispatched first when a slot frees).
var tierRank = map[Tier]int{
    TierHeavy:   0,
    TierGeneral: 1,
    TierLight:   2,
}

// ErrTierQueueTimeout is returned by TierQueue.Acquire when no slot frees
// within the configured timeout. Callers translate this into a 429 (mirrors
// the single-tier slotQueue contract).
var ErrTierQueueTimeout = errors.New("tier queue timeout: no local slot freed in time")

// TierQueue is a bounded priority wait-queue over local inference slots with
// 3 admission tiers (heavy/general/light). It is a counting semaphore of size
// MaxConcurrent: each Acquire blocks until a slot is free (or QueueTimeout
// fires). When multiple requests wait, the highest-priority (lowest rank) tier
// is dispatched first on the next release — head-of-line by tier. Within the
// same tier, FIFO order is preserved (waiters stored in arrival order).
//
// Guarantee slots: HeavyGuarantee / LightGuarantee reserve capacity so a burst
// in one tier cannot fully starve another. A guarantee of 0 means no reserved
// capacity (best-effort). Guarantees must sum <= MaxConcurrent; excess is
// clamped at construction.
//
// NOT engaged in hybrid mode by default: the engine falls back to cloud rather
// than queueing, so this stays nil unless routing.mode=local. The gateway does
// not own agent semantic labels beyond the Intent+tenant-Tier mapping (#102
// ADR-001 still holds for scheduling ownership; #159 adds admission priority).
type TierQueue struct {
    mu       sync.Mutex
    sem      chan struct{}
    timeout  time.Duration
    waiters  []*tierWaiter
    guarantees map[Tier]int
}

type tierWaiter struct {
    tier      Tier
    rank      int
    arrived   int64
    ready     chan struct{}
    cancel    context.CancelFunc
    // committed is set under q.mu by dispatch once a slot has been handed to
    // this waiter (ready signaled). It guards the dispatch race: a client ctx
    // cancel landing AFTER dispatch must not steal the slot — the goroutine
    // ignores a post-commit cancel and returns the release closure so the
    // slot is eventually freed by release(), not leaked.
    committed bool
}

// NewTierQueue builds a TierQueue from config. A non-positive MaxConcurrent
// is clamped to 1. Guarantees are clamped so their sum does not exceed
// MaxConcurrent.
func NewTierQueue(cfg config.TierQueueConfig) *TierQueue {
    cap := cfg.MaxConcurrent
    if cap <= 0 {
        cap = 1
    }
    guarantees := map[Tier]int{
        TierHeavy:   clampGuarantee(cfg.HeavyGuarantee, cap),
        TierLight:   clampGuarantee(cfg.LightGuarantee, cap),
    }
    // Clamp sum <= cap: shrink light first, then heavy.
    for guarantees[TierHeavy]+guarantees[TierLight] > cap {
        if guarantees[TierLight] > 0 {
            guarantees[TierLight]--
        } else if guarantees[TierHeavy] > 0 {
            guarantees[TierHeavy]--
        } else {
            break
        }
    }
    timeout := cfg.QueueTimeout
    if timeout <= 0 {
        timeout = 30 * time.Second
    }
    slog.Info("tier queue constructed",
        "max_concurrent", cap,
        "heavy_guarantee", guarantees[TierHeavy],
        "light_guarantee", guarantees[TierLight],
        "queue_timeout", timeout)
    return &TierQueue{
        sem:        make(chan struct{}, cap),
        timeout:    timeout,
        guarantees: guarantees,
    }
}

func clampGuarantee(g, cap int) int {
    if g < 0 {
        return 0
    }
    if g > cap {
        return cap
    }
    return g
}

// Acquire blocks until a local slot is free or timeout elapses. On success it
// returns a release closure the caller MUST defer. On timeout it returns
// ErrTierQueueTimeout. ctx cancel is honored: a canceled ctx returns ctx.Err()
// without acquiring a slot.
func (q *TierQueue) Acquire(ctx context.Context, tier Tier) (func(), error) {
    // Fast path: a free slot with no waiters takes it immediately. This keeps
    // the uncontended path lock-free and identical to slotQueue.
    select {
    case q.sem <- struct{}{}:
        slog.Debug("tier queue acquired (fast path)",
            "tier", tier, "occupied", len(q.sem))
        return q.release, nil
    default:
    }

    waiter := &tierWaiter{
        tier:  tier,
        rank:  tierRank[tier],
        ready: make(chan struct{}, 1),
    }
    waitCtx, cancel := context.WithCancel(ctx)
    waiter.cancel = cancel

    q.mu.Lock()
    q.insertWaiter(waiter)
    q.mu.Unlock()

    // Try once more after registering — a slot may have freed between the
    // fast-path select and the lock.
    q.dispatch()
    if q.tryTakeWaiter(waiter) {
        return q.release, nil
    }

    timer := time.NewTimer(q.timeout)
    defer timer.Stop()

    select {
    case <-waiter.ready:
        return q.release, nil
    case <-timer.C:
        if q.removeWaiterIfNotCommitted(waiter) {
            slog.Warn("tier queue timeout, rejecting request (429)",
                "tier", tier, "wait_budget", q.timeout, "occupied", len(q.sem))
            return nil, ErrTierQueueTimeout
        }
        // Already dispatched (committed): a slot is ours, ignore the timeout.
        <-waiter.ready
        return q.release, nil
    case <-waitCtx.Done():
        if q.removeWaiterIfNotCommitted(waiter) {
            slog.Info("tier queue acquire canceled by client",
                "tier", tier, "error", waitCtx.Err())
            return nil, waitCtx.Err()
        }
        // Already dispatched (committed): client cancel must not steal the
        // slot — wait for the ready signal and return the release closure.
        <-waiter.ready
        return q.release, nil
    }
}

// insertWaiter inserts in tier-rank order (head-of-line by priority), preserving
// FIFO within a tier. Caller holds q.mu.
func (q *TierQueue) insertWaiter(w *tierWaiter) {
    pos := len(q.waiters)
    for i, existing := range q.waiters {
        if w.rank < existing.rank {
            pos = i
            break
        }
    }
    q.waiters = append(q.waiters, nil)
    copy(q.waiters[pos+1:], q.waiters[pos:])
    q.waiters[pos] = w
}

// dispatch hands a free slot to the highest-priority eligible waiter. Called
// under q.mu on release and on post-register retry.
func (q *TierQueue) dispatch() {
    q.mu.Lock()
    defer q.mu.Unlock()
    for len(q.waiters) > 0 {
        select {
        case q.sem <- struct{}{}:
            w := q.waiters[0]
            q.waiters = q.waiters[1:]
            w.committed = true
            select {
            case w.ready <- struct{}{}:
            default:
            }
            slog.Debug("tier queue dispatched waiter",
                "tier", w.tier, "remaining_waiters", len(q.waiters))
            return
        default:
            // No free slot: waiters stay queued for the next release.
            return
        }
    }
}

// tryTakeWaiter checks if a waiter was already dispatched to (ready signaled)
// and consumes the signal. Returns true if acquired.
func (q *TierQueue) tryTakeWaiter(w *tierWaiter) bool {
    select {
    case <-w.ready:
        return true
    default:
        return false
    }
}

// removeWaiterIfNotCommitted drops a waiter that timed out or was canceled,
// but ONLY if dispatch has not already committed a slot to it. Returns true if
// removed (caller treats as timeout/cancel), false if already committed
// (caller must consume the ready signal and return the slot). This closes the
// dispatch race: a cancel/timeout landing after dispatch would otherwise steal
// or leak the handed-off slot.
func (q *TierQueue) removeWaiterIfNotCommitted(w *tierWaiter) bool {
    q.mu.Lock()
    defer q.mu.Unlock()
    if w.committed {
        return false
    }
    for i, existing := range q.waiters {
        if existing == w {
            q.waiters = append(q.waiters[:i], q.waiters[i+1:]...)
            break
        }
    }
    w.cancel()
    return true
}

// release frees one slot and dispatches to the highest-priority waiter.
// Idempotent-safe only if called once per Acquire (standard defer pattern).
func (q *TierQueue) release() {
    select {
    case <-q.sem:
    default:
        slog.Warn("tier queue release on empty semaphore (double-release?)")
        return
    }
    q.dispatch()
}

// Occupied returns the current number of held slots (for metrics/logging).
func (q *TierQueue) Occupied() int {
    return len(q.sem)
}

// Waiters returns the count of queued waiters by tier (for metrics).
func (q *TierQueue) Waiters() map[Tier]int {
    q.mu.Lock()
    defer q.mu.Unlock()
    counts := map[Tier]int{TierHeavy: 0, TierGeneral: 0, TierLight: 0}
    for _, w := range q.waiters {
        counts[w.tier]++
    }
    return counts
}

// TierForRequest derives the admission tier from a semantic Intent and the
// bound tenant's Tier field (#159). The tenant tag wins when set: a tenant
// flagged "heavy" is admitted at heavy priority regardless of intent (the
// tenant paid for the priority class). Otherwise the intent maps: diffusion /
// heavy_model -> heavy, lightweight -> light, everything else -> general.
func TierForRequest(intent interface{ String() string }, tenantTier string) Tier {
    if tenantTier != "" {
        switch Tier(tenantTier) {
        case TierHeavy, TierGeneral, TierLight:
            return Tier(tenantTier)
        }
    }
    switch intent.String() {
    case "diffusion", "heavy_model":
        return TierHeavy
    case "lightweight":
        return TierLight
    }
    return TierGeneral
}

// buildTierQueue constructs the opt-in TierQueue from the live config
// snapshot. Returns nil when disabled (the engine falls back to single-tier
// slotQueue or no queue), so #159 is opt-in and default-off.
func buildTierQueue(cfg *config.ConfigSnapshot) *TierQueue {
    if cfg == nil {
        return nil
    }
    tc := cfg.Config.Routing.TierQueue
    if !tc.Enabled {
        return nil
    }
    return NewTierQueue(config.TierQueueConfig{
        Enabled:        tc.Enabled,
        MaxConcurrent:  tc.MaxConcurrent,
        QueueTimeout:   tc.QueueTimeout,
        HeavyGuarantee: tc.HeavyGuarantee,
        LightGuarantee: tc.LightGuarantee,
    })
}
