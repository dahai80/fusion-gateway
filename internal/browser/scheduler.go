package browser

import (
    "errors"
    "fmt"
)

// Scheduler error sentinels. The proxy maps these to distinct 503 codes so
// fusion-cowork can branch retry/recreate logic (RC1: never mask as 502).
var (
    // ErrGlobalQuotaExceeded: the sum of live_sessions across live nodes has
    // reached global_max_sessions. Not retryable (503 quota_exceeded) — the
    // caller must wait for a session to close, not blindly retry.
    ErrGlobalQuotaExceeded = errors.New("browser: global session quota exceeded")
    // ErrNoNodeHeadroom: no live node has a free slot (every live node is at
    // max_sessions, or the candidates that have a slot fail the memory floor).
    // Retryable (503 no_headroom) — a slot may free up on the next poll.
    ErrNoNodeHeadroom = errors.New("browser: no node with headroom")
)

// Scheduler ranks live nodes by headroom and picks the best placement target.
// Pick is a PURE function over a registry snapshot — no I/O, no locks held
// beyond the snapshot copy — so it is deterministic and fully unit-testable.
// The same snapshot always yields the same pick (tie-breaks by config id asc).
type Scheduler struct {
    registry          *Registry
    globalMaxSessions int
    minFreeMBPerSession int
}

// NewScheduler binds a scheduler to a registry + the placement knobs from
// config (global_max_sessions, min_free_mb_per_session).
func NewScheduler(reg *Registry, globalMaxSessions, minFreeMBPerSession int) *Scheduler {
    return &Scheduler{
        registry:            reg,
        globalMaxSessions:   globalMaxSessions,
        minFreeMBPerSession: minFreeMBPerSession,
    }
}

// Pick returns the node id (stable config label) with the most headroom for a
// new session, or a sentinel error. The proxy uses the returned id to look up
// the socket path for the forward. Steps per spec §7:
//  1. Global ceiling: if set and sum(live_sessions) >= ceiling → quota error.
//  2. Headroom filter: live node with live_sessions < max_sessions AND
//     (free_memory_mb==0 [probe failed = unknown, admitted] OR
//      free_memory_mb >= min_free_mb_per_session).
//  3. Rank: most free_memory_mb desc; tie fewest live_sessions asc; final
//     tie config id asc (deterministic). If ALL candidates have free_mem==0,
//     rank by fewest live_sessions asc (contract fallback).
//  4. No candidate → ErrNoNodeHeadroom.
//
// Dead nodes never reach the snapshot filter (IsDead gate + the live-state
// filter in candidateFor). A node with nil Capacity (not yet polled) is
// admitted with free_mem treated as 0 (unknown) — never fabricate a memory
// figure, and never block placement on a slow first poll.
func (s *Scheduler) Pick() (string, error) {
    views := s.registry.Snapshot()

    var (
        totalLive int
        candidates []nodeView
    )
    for _, v := range views {
        if v.State != NodeStateLive {
            continue
        }
        if v.Capacity != nil {
            totalLive += v.Capacity.LiveSessions
        }
        if s.candidateFor(v) {
            candidates = append(candidates, v)
        }
    }

    if s.globalMaxSessions > 0 && totalLive >= s.globalMaxSessions {
        return "", fmt.Errorf("%w: %d live >= %d ceiling",
            ErrGlobalQuotaExceeded, totalLive, s.globalMaxSessions)
    }
    if len(candidates) == 0 {
        return "", ErrNoNodeHeadroom
    }
    return rankCandidates(candidates)[0].NodeID, nil
}

// candidateFor reports whether a live node is eligible for a new session: it
// has a free slot (live_sessions < max_sessions) and meets the memory floor
// (free_memory_mb==0 or >= min). A node with nil Capacity (unpolled) is
// admitted — live_sessions/max_sessions are treated as 0/∞ so an unpolled
// node is never wrongly rejected, and free_mem is unknown (0) so the floor
// check is skipped (the contract's free_mem==0 admission path).
func (s *Scheduler) candidateFor(v nodeView) bool {
    live, max, free := 0, int(1<<31 - 1), 0 // unpolled: 0 live, huge max, 0 free
    if v.Capacity != nil {
        live = v.Capacity.LiveSessions
        max = v.Capacity.MaxSessions
        free = v.Capacity.FreeMemoryMB
    }
    if live >= max {
        return false // no free slot
    }
    if free != 0 && free < s.minFreeMBPerSession {
        return false // below memory floor (free==0 = unknown = admitted)
    }
    return true
}

// rankCandidates sorts candidates by the placement priority: most free memory
// desc, then fewest live sessions asc, then config id asc. If every candidate
// has free_memory_mb==0 (all probes failed / unpolled), the memory comparison
// is a no-op and the sort falls through to fewest-live then id — the contract
// fallback. Returns a new slice; does not mutate the input.
func rankCandidates(c []nodeView) []nodeView {
    ranked := make([]nodeView, len(c))
    copy(ranked, c)
    // In-place stable sort by the composite key. Use a slice of indices to
    // avoid re-slicing nodeView (which holds a pointer via Capacity).
    less := func(i, j int) bool {
        a, b := ranked[i], ranked[j]
        af, bf := freeMem(a), freeMem(b)
        if af != bf {
            return af > bf // most free memory desc
        }
        al, bl := liveSessions(a), liveSessions(b)
        if al != bl {
            return al < bl // fewest live sessions asc
        }
        return a.NodeID < b.NodeID // config id asc (deterministic)
    }
    // insertion sort keeps it dependency-free and stable; candidate sets are
    // small (a handful of nodes), so O(n²) is fine and avoids pulling sort.
    for i := 1; i < len(ranked); i++ {
        for k := i; k > 0 && less(k, k-1); k-- {
            ranked[k], ranked[k-1] = ranked[k-1], ranked[k]
        }
    }
    return ranked
}

// freeMem returns a node's free_memory_mb, or 0 for an unpolled node (nil
// Capacity). Never fabricates a figure.
func freeMem(v nodeView) int {
    if v.Capacity == nil {
        return 0
    }
    return v.Capacity.FreeMemoryMB
}

// liveSessions returns a node's live_sessions, or 0 for an unpolled node.
func liveSessions(v nodeView) int {
    if v.Capacity == nil {
        return 0
    }
    return v.Capacity.LiveSessions
}
