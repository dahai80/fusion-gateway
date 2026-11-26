package server

// #725: the tap adopts the caller-supplied X-Fusion-Rollout-Id so one agent
// turn (several chat rounds, one stable id from the office apps) produces
// tap records that join the outcome plane. Absent/malformed header = mint.

import (
    "net/http"
    "net/http/httptest"
    "strings"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/adapter"
    "github.com/fusion-gateway/fusion-gateway/internal/rollout"
)

func Test725_AdoptRolloutHeaderID(t *testing.T) {
    cases := []struct {
        name string
        val  string
        want string
    }{
        {"absent", "", ""},
        {"adopted", "turn-abc-123", "turn-abc-123"},
        {"trimmed", "  turn-x  ", "turn-x"},
        {"too long", strings.Repeat("a", rolloutHeaderMaxLen+1), ""},
        {"control char", "turn\x01x", ""},
        {"space inside", "turn x", ""},
        {"non ascii", "turn-é", ""},
    }
    for _, tc := range cases {
        h := http.Header{}
        if tc.val != "" {
            h.Set("X-Fusion-Rollout-Id", tc.val)
        }
        if got := adoptRolloutHeaderID(h); got != tc.want {
            t.Fatalf("%s: got %q want %q", tc.name, got, tc.want)
        }
    }
}

func Test725_EmitAdoptsHeaderRolloutID(t *testing.T) {
    // Adopted id lands in the sink record verbatim; the tap must not mint.
    s := &Server{}
    tap, path := rollout608Tap(t, rollout.ContentModeRaw, nil)
    s.rolloutTap = tap

    rec := httptest.NewRecorder()
    capw := newRolloutTapCapture(rec, 0, "master")
    _, _ = capw.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
    req := &adapter.ChatRequest{
        Model:    "test-model",
        Messages: []adapter.ChatMessage{{Role: "user", Content: "hi"}},
    }
    s.emitRolloutTapRecord(req, capw, time.Now(), "turn-e2e-9")
    recs := rollout608WaitRecords(t, path, 1)
    if recs[0].RolloutID != "turn-e2e-9" {
        t.Fatalf("header id not adopted: %q", recs[0].RolloutID)
    }
}

func Test725_EmitEmptyIDStillMints(t *testing.T) {
    // Empty adopted id = tap mints its own ro- id (previous behavior).
    s := &Server{}
    tap, path := rollout608Tap(t, rollout.ContentModeRaw, nil)
    s.rolloutTap = tap

    rec := httptest.NewRecorder()
    capw := newRolloutTapCapture(rec, 0, "master")
    _, _ = capw.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
    req := &adapter.ChatRequest{
        Model:    "test-model",
        Messages: []adapter.ChatMessage{{Role: "user", Content: "hi"}},
    }
    s.emitRolloutTapRecord(req, capw, time.Now(), "")
    recs := rollout608WaitRecords(t, path, 1)
    if !strings.HasPrefix(recs[0].RolloutID, "ro-") {
        t.Fatalf("expected minted ro- id, got %q", recs[0].RolloutID)
    }
}
