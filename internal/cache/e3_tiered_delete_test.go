package cache

import (
    "sync"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

// slowL2 is a fake CacheBackend whose Delete blocks on a release channel, so a
// guard can hold the tieredBackend Delete window open and race a Get into it.
// Get returns the stored value until deletedFlag is set (by Delete completing).
type slowL2 struct {
    mu          sync.Mutex
    val         []byte
    deletedFlag bool
    releaseCh   chan struct{}
}

func (s *slowL2) Get(key string) ([]byte, bool) {
    s.mu.Lock()
    defer s.mu.Unlock()
    if s.deletedFlag {
        return nil, false
    }
    if s.val != nil {
        return s.val, true
    }
    return nil, false
}

func (s *slowL2) Set(key string, value []byte, ttl time.Duration) {
    s.mu.Lock()
    defer s.mu.Unlock()
    s.val = value
    s.deletedFlag = false
}

func (s *slowL2) Delete(key string) {
    // Block until the test releases us — this widens the L1-deleted / L2-not-
    // yet-deleted window so a racing Get is forced into it.
    if s.releaseCh != nil {
        <-s.releaseCh
    }
    s.mu.Lock()
    s.deletedFlag = true
    s.val = nil
    s.mu.Unlock()
}

func (s *slowL2) Stats() (hits, misses int64, size int) { return 0, 0, 0 }
func (s *slowL2) Close()                                {}

// TestE3_TieredDelete_NoStaleRevival: a concurrent Get racing into the
// tieredBackend Delete window must NOT revive a stale value by refilling L1
// from an L2 that has not yet been deleted. Before E3, Delete(L1) then
// Delete(L2) had an unguarded window: Get saw L1 miss → L2 hit (not yet
// deleted) → refilled L1 with the just-invalidated value → stale revival.
// Here the L2 Delete blocks mid-tiered-Delete to force the race; with the E3
// lock the racing Get must block until Delete finishes (L2 gone) → miss.
// Revert (drop the mu lock in tieredBackend.Delete + Get refill): Get returns
// the stale value → FAIL.
func TestE3_TieredDelete_NoStaleRevival(t *testing.T) {
    l1 := New(config.CacheConfig{MaxEntries: 100, TTL: time.Minute})
    l2 := &slowL2{releaseCh: make(chan struct{})}
    tiered := &tieredBackend{l1: l1, l2: l2}

    tiered.Set("k", []byte("stale"), time.Minute)

    var delErr error
    delDone := make(chan struct{})
    go func() {
        defer close(delDone)
        tiered.Delete("k")
        if delErr != nil { // unused, keeps linter calm
            return
        }
    }()

    // Give the Delete goroutine time to enter: it deletes L1 (fast), then
    // blocks in L2.Delete on releaseCh. L1 is now empty, L2 still has "stale".
    time.Sleep(50 * time.Millisecond)

    // Race a Get into the window. With the E3 lock it blocks until Delete
    // finishes; without the lock it refills L1 from the stale L2.
    var got []byte
    var ok bool
    getDone := make(chan struct{})
    go func() {
        defer close(getDone)
        got, ok = tiered.Get("k")
    }()

    // Let the Get reach the L2-refill branch (it will block on mu under the
    // fix, or refill under the bug), then release the L2 Delete so everything
    // resolves.
    time.Sleep(50 * time.Millisecond)
    close(l2.releaseCh)

    <-delDone
    <-getDone

    if ok {
        t.Errorf("E3: concurrent Get must NOT revive a value mid-Delete (stale revival), got %q (L1 refilled from L2 in the Delete window — pre-E3 non-atomic tiered delete bug)", string(got))
    }
}

// TestE3_TieredDelete_BothTiersCleared: after Delete completes, both L1 and L2
// report the key gone — the normal (non-raced) contract. Companion to the
// revival guard so the lock does not skip a tier.
func TestE3_TieredDelete_BothTiersCleared(t *testing.T) {
    l1 := New(config.CacheConfig{MaxEntries: 100, TTL: time.Minute})
    l2 := &slowL2{}
    tiered := &tieredBackend{l1: l1, l2: l2}

    tiered.Set("k", []byte("v"), time.Minute)
    if _, ok := tiered.Get("k"); !ok {
        t.Fatal("setup: Set then Get must hit")
    }

    tiered.Delete("k")

    if _, ok := tiered.Get("k"); ok {
        t.Errorf("E3: after Delete, Get must miss (both tiers cleared), got hit")
    }
}
