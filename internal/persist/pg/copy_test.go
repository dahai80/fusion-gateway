package pg

import (
    "testing"

    "github.com/google/uuid"

    "github.com/fusion-gateway/fusion-gateway/internal/persist"
    "github.com/fusion-gateway/fusion-gateway/internal/store"
)

func TestSplitBody(t *testing.T) {
    // valid JSON -> jsonb column
    j, r := splitBody([]byte(`{"model":"m"}`))
    if j == nil || r != nil {
        t.Fatalf("valid JSON must go to jsonb: j=%v r=%v", j, r)
    }
    // SSE frames -> raw text column
    sse := []byte("data: {\"id\":\"x\"}\n\n")
    j, r = splitBody(sse)
    if j != nil || r != string(sse) {
        t.Fatalf("SSE must go to raw: j=%v r=%v", j, r)
    }
    // truncated mid-token JSON -> raw
    j, r = splitBody([]byte(`{"messages":[{"role":"user","content":"abc`))
    if j != nil || r == nil {
        t.Fatalf("truncated JSON must go to raw: j=%v r=%v", j, r)
    }
    // empty -> both NULL
    j, r = splitBody(nil)
    if j != nil || r != nil {
        t.Fatalf("empty body must be NULL/NULL: j=%v r=%v", j, r)
    }
}

func TestResolveRowID(t *testing.T) {
    e := &persist.Entry{Log: store.RequestLog{ID: "log-42"}}
    e.Log.RequestID = "0f0e0d0c-1a2b-3c4d-5e6f-7a8b9c0d1e2f"
    got := resolveRowID(e)
    if uuid.UUID(got).String() != e.Log.RequestID {
        t.Fatalf("non-UUID ID must fall back to RequestID, got %s", uuid.UUID(got).String())
    }
    e.Log.RequestID = "also-not-uuid"
    if resolveRowID(e) == ([16]byte{}) {
        t.Fatal("both non-UUID must mint a fresh uuid, not zero")
    }
}
