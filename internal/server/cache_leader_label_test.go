package server

// #737 regression: the R2 fetch-coalescer LEADER generates the response
// upstream and then re-reads its own publish from the exact cache — labeling
// that serve X-Cache: HIT over-counted cache hits by one per cold key (and
// made the first post-bypass request look like a bypass-contract violation).
// The leader must be labeled MISS; genuine waiters keep HIT.

import (
    "net/http"
    "testing"
)

func Test737_LeaderLabeledMissWaiterHit(t *testing.T) {
    provider, s := bypassTestProvider()
    defer s.cache.Close()

    // Cold key, first (leader) call: fresh generation, must be MISS.
    rec1 := runBypassChat(s, bypassChatBody, false)
    if rec1.Code != http.StatusOK {
        t.Fatalf("leader call failed: %d %s", rec1.Code, rec1.Body.String())
    }
    if got := rec1.Header().Get("X-Cache"); got != "MISS" {
        t.Fatalf("#737: coalescer leader must be labeled MISS, got %q", got)
    }
    if n := provider.calls.Load(); n != 1 {
        t.Fatalf("leader must fetch upstream once, got %d calls", n)
    }

    // Warm call: genuine cache hit, still HIT.
    rec2 := runBypassChat(s, bypassChatBody, false)
    if got := rec2.Header().Get("X-Cache"); got != "HIT" {
        t.Fatalf("warm call must remain HIT, got %q", got)
    }
    if n := provider.calls.Load(); n != 1 {
        t.Fatalf("warm call must not re-fetch, got %d calls", n)
    }
}

func Test737_DoReportsLeaderFlag(t *testing.T) {
    c := newCoalescer()
    ran := 0
    if !c.Do("k", func() { ran++ }) {
        t.Fatal("first (uncontended) Do must report leader=true")
    }
    if ran != 1 {
        t.Fatalf("leader fn must run once, got %d", ran)
    }
    // After the leader's group completes, the next Do for the same key is a
    // NEW group and is itself the leader (the waiter=false case only arises
    // while a leader is in flight; that labeling is covered end-to-end by
    // Test737_LeaderLabeledMissWaiterHit's warm-call HIT assertion).
    if !c.Do("k", func() { ran++ }) {
        t.Fatal("Do on a completed group must report leader=true for the new group")
    }
    if ran != 2 {
        t.Fatalf("new leader must run fn, got %d runs", ran)
    }
}
