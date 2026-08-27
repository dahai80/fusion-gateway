package server

import (
    "testing"
    "time"
)

// TestR7_StreamBuffer_CapEvictsOldest: when the entries cap is reached, Open
// evicts the oldest entry (oldest createdAt) before inserting the new one, so
// the map never exceeds maxEntries. Guard: revert the eviction branch (just
// insert) and the count stays at cap+1.
func TestR7_StreamBuffer_CapEvictsOldest(t *testing.T) {
    store := NewStreamBufferStore(256, 1<<20, 3, 10*time.Minute)
    if store.Open("sid-a") == nil {
        t.Fatal("Open sid-a nil")
    }
    time.Sleep(2 * time.Millisecond)
    if store.Open("sid-b") == nil {
        t.Fatal("Open sid-b nil")
    }
    time.Sleep(2 * time.Millisecond)
    if store.Open("sid-c") == nil {
        t.Fatal("Open sid-c nil")
    }
    time.Sleep(2 * time.Millisecond)
    // map at cap (3). Opening sid-d must evict sid-a (oldest createdAt).
    d := store.Open("sid-d")
    if d == nil {
        t.Fatal("Open sid-d nil")
    }
    store.mu.RLock()
    count := len(store.buffers)
    _, aGone := store.buffers["sid-a"]
    _, dPresent := store.buffers["sid-d"]
    store.mu.RUnlock()
    if count != 3 {
        t.Fatalf("entries count = %d, want 3 (cap enforced)", count)
    }
    if aGone {
        t.Fatal("sid-a should have been evicted as oldest, but still present")
    }
    if !dPresent {
        t.Fatal("sid-d should be present after Open")
    }
    if store.Get("sid-a") != nil {
        t.Fatal("Get(sid-a) must be nil after eviction")
    }
    if store.Get("sid-d") == nil {
        t.Fatal("Get(sid-d) must be non-nil")
    }
}

// TestR7_StreamBuffer_CapZeroUnlimited: maxEntries=0 means no global cap.
// Opening many sids must keep them all (backward-compat with prior behavior).
// Guard: revert the `maxEntries > 0` gate (always evict) and count drops.
func TestR7_StreamBuffer_CapZeroUnlimited(t *testing.T) {
    store := NewStreamBufferStore(256, 1<<20, 0, 10*time.Minute)
    for i := 0; i < 50; i++ {
        sid := "sid-" + string(rune('a'+i%26)) + string(rune('0'+i/26))
        if store.Open(sid) == nil {
            t.Fatalf("Open %s nil", sid)
        }
    }
    store.mu.RLock()
    count := len(store.buffers)
    store.mu.RUnlock()
    if count != 50 {
        t.Fatalf("entries count = %d, want 50 (cap=0 unlimited)", count)
    }
}

// TestR7_StreamBuffer_ReuseSidAtCapDoesNotEvict: reusing an existing sid when
// at the cap must NOT evict another entry — it resets in place. sid-b is the
// OLDEST (opened first); reopening sid-a must not touch sid-b. Guard: revert
// the `!exists` guard (evict even on reuse) and sid-b vanishes.
func TestR7_StreamBuffer_ReuseSidAtCapDoesNotEvict(t *testing.T) {
    store := NewStreamBufferStore(256, 1<<20, 2, 10*time.Minute)
    if store.Open("sid-b") == nil {
        t.Fatal("Open sid-b nil")
    }
    time.Sleep(2 * time.Millisecond)
    if store.Open("sid-a") == nil {
        t.Fatal("Open sid-a nil")
    }
    // map at cap (2). sid-b is oldest. Reopening sid-a must reset it, not
    // evict sid-b.
    if store.Open("sid-a") == nil {
        t.Fatal("reopen sid-a nil")
    }
    store.mu.RLock()
    count := len(store.buffers)
    _, bPresent := store.buffers["sid-b"]
    store.mu.RUnlock()
    if count != 2 {
        t.Fatalf("entries count = %d, want 2 (reuse must not grow map)", count)
    }
    if !bPresent {
        t.Fatal("sid-b evicted on sid-a reuse — must be preserved")
    }
    if store.Get("sid-b") == nil {
        t.Fatal("Get(sid-b) nil after sid-a reuse")
    }
}
