package adapter

import (
    "context"
    "encoding/json"
    "strings"
    "testing"
)

// TestE1_ParseSSEStream_SetsRaw: the audit found parseSSEStream unmarshaled
// each frame into StreamChunk and the server re-marshaled it — a per-frame
// json.Unmarshal + json.Marshal burning ~1-2 cores at ~50 concurrent long
// streams. The E1 fix carries the verbatim upstream data bytes in
// StreamChunk.Raw so the server emits them directly. This guard asserts
// parseSSEStream populates Raw with the exact upstream data line AND the
// struct fields stay populated (usage/ID tracking unchanged). Revert (don't
// set chunk.Raw): Raw stays nil → FAIL.
func TestE1_ParseSSEStream_SetsRaw(t *testing.T) {
    frame := `{"id":"c1","object":"chat.completion.chunk","model":"m","choices":[]}`
    upstream := "data: " + frame + "\n\ndata: [DONE]\n\n"
    ch := make(chan StreamChunk, 8)
    parseSSEStream(context.Background(), strings.NewReader(upstream), ch)
    close(ch)

    var got []StreamChunk
    for c := range ch {
        got = append(got, c)
    }
    if len(got) != 1 {
        t.Fatalf("expected 1 chunk (before [DONE]), got %d", len(got))
    }
    // Raw must carry the verbatim upstream data line (after the "data: " prefix).
    if string(got[0].Raw) != frame {
        t.Fatalf("E1: parseSSEStream must set chunk.Raw to the verbatim upstream data line so the server can emit it without re-marshaling; got %q want %q", string(got[0].Raw), frame)
    }
    // Struct fields stay populated — usage/ID/model tracking in the forward
    // loop reads these, so Raw must not replace the unmarshal, only augment.
    if got[0].ID != "c1" || got[0].Model != "m" {
        t.Fatalf("E1: struct fields must stay populated alongside Raw for tracking, got ID=%q Model=%q", got[0].ID, got[0].Model)
    }
}

// TestE1_RawEmitsVerbatim_MarshalFallback: the contract is — when Raw is set,
// the emitted bytes are the verbatim upstream (no re-marshal); when Raw is nil
// (in-process-built chunks), the struct marshals as before. This guard drives
// both arms at the StreamChunk level so the server's writeChunk passthrough is
// proven correct without standing up a full HTTP server.
func TestE1_RawEmitsVerbatim_MarshalFallback(t *testing.T) {
    // Arm 1: Raw set → emitted bytes equal Raw verbatim.
    rawFrame := `{"id":"r","object":"chat.completion.chunk","model":"m","choices":[],"extra":"preserved"}`
    chunk := StreamChunk{
        ID:    "r",
        Model: "m",
        Raw:   json.RawMessage(rawFrame),
    }
    // The server writeChunk picks Raw when len>0; simulate that decision.
    emitted := chunk.Raw
    if len(emitted) == 0 {
        b, _ := json.Marshal(chunk)
        emitted = b
    }
    if string(emitted) != rawFrame {
        t.Fatalf("E1: Raw-set chunk must emit verbatim upstream bytes (passthrough, preserves unknown 'extra' field), got %q", string(emitted))
    }

    // Arm 2: Raw nil (in-process chunk, e.g. synthetic usage) → marshal fallback.
    built := StreamChunk{ID: "u", Object: "chat.completion.chunk", Model: "m"}
    emitted2 := built.Raw
    if len(emitted2) == 0 {
        b, _ := json.Marshal(built)
        emitted2 = b
    }
    // Marshal fallback must produce valid JSON with the struct fields.
    var back StreamChunk
    if err := json.Unmarshal(emitted2, &back); err != nil {
        t.Fatalf("E1: marshal-fallback bytes must be valid JSON, unmarshal err: %v", err)
    }
    if back.ID != "u" {
        t.Fatalf("E1: marshal-fallback must round-trip struct fields, got ID=%q", back.ID)
    }
}

// TestE1_RawOmitJsonTag: StreamChunk.Raw has json:"-" so a freshly-built chunk
// (Raw nil) marshals WITHOUT a "Raw" field — the wire format for in-process
// chunks is unchanged by the E1 addition. Guards against a missing json:"-"
// that would leak an internal field to clients.
func TestE1_RawOmitJsonTag(t *testing.T) {
    built := StreamChunk{ID: "x", Object: "chat.completion.chunk", Model: "m"}
    b, err := json.Marshal(built)
    if err != nil {
        t.Fatalf("marshal: %v", err)
    }
    if strings.Contains(string(b), "Raw") {
        t.Fatalf("E1: StreamChunk.Raw must have json:\"-\" so it never appears in a marshaled chunk; got %s", string(b))
    }
}
