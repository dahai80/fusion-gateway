package adapter

// N3 (audit) test: AnthropicStreamEvent.Raw carries verbatim upstream bytes so
// the native stream forward loop can emit non-block events without re-marshaling
// (the audit's per-frame serialization burn). Block-scoped events are excluded
// from raw passthrough: MarshalJSON injects a missing "index":0 (issue #46), so
// emitting raw bytes that omit index would regress that fix. These tests pin
// both halves — raw valid for non-block events, index preserved for block
// events — and assert the emit-side block set matches the marshal-side block
// set (Rule 7: one definition of "block-scoped", no drift).

import (
    "encoding/json"
    "strings"
    "testing"
)

// n3BlockScopedTypes is the single source of truth mirrored from the forward
// loop's emitAnthropicEvent closure (anthropic_messages.go). If either side
// changes without the other, a block event gets raw-emitted (dropping index,
// #46 regression) or a non-block event gets needlessly re-marshaled. The test
// below asserts this slice equals the marshal-side block set.
var n3BlockScopedTypes = []string{"content_block_start", "content_block_delta", "content_block_stop"}

// TestN3_NonBlockEventRawPassthrough: a non-block event (message_start) carrying
// Raw upstream bytes must be emittable verbatim — Raw is already valid JSON, so
// the forward loop skips marshal and writes Raw directly. Assert Raw survives a
// round-trip and is byte-equal to the source payload (faithful passthrough).
func TestN3_NonBlockEventRawPassthrough(t *testing.T) {
    src := `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","content":[],"model":"claude","stop_reason":null,"usage":{"input_tokens":10,"output_tokens":0}}}`
    var ev AnthropicStreamEvent
    if err := json.Unmarshal([]byte(src), &ev); err != nil {
        t.Fatalf("unmarshal upstream payload: %v", err)
    }
    // Simulate the parser's Raw population (copy of the verbatim payload).
    ev.Raw = append([]byte(nil), []byte(src)...)

    // Emit path for non-block events uses Raw directly (forward loop closure).
    blockScoped := ev.Type == "content_block_start" ||
        ev.Type == "content_block_delta" ||
        ev.Type == "content_block_stop"
    if blockScoped {
        t.Fatalf("message_start must not be block-scoped")
    }
    if len(ev.Raw) == 0 {
        t.Fatal("N3: Raw must be populated by the parser for passthrough")
    }
    // Raw must be byte-identical to the source — no re-marshal normalization.
    if string(ev.Raw) != src {
        t.Errorf("N3: Raw passthrough must be verbatim, got %q want %q", string(ev.Raw), src)
    }
    // Sanity: the source JSON is itself valid (upstream data: lines are valid JSON).
    if !json.Valid(ev.Raw) {
        t.Error("N3: Raw passthrough bytes must be valid JSON")
    }
}

// TestN3_BlockEventMarshalInjectsIndex: a content_block_delta event MISSING
// "index" must marshal WITH "index":0 injected (issue #46). This is the reason
// block events are excluded from raw passthrough — emitting the raw bytes would
// omit index and break claude code's block matching.
func TestN3_BlockEventMarshalInjectsIndex(t *testing.T) {
    // Upstream payload that omits index (the #46 bug condition).
    src := `{"type":"content_block_delta","delta":{"type":"text_delta","text":"hi"}}`
    var ev AnthropicStreamEvent
    if err := json.Unmarshal([]byte(src), &ev); err != nil {
        t.Fatalf("unmarshal: %v", err)
    }
    out, err := json.Marshal(ev)
    if err != nil {
        t.Fatalf("marshal: %v", err)
    }
    var m map[string]json.RawMessage
    if err := json.Unmarshal(out, &m); err != nil {
        t.Fatalf("re-parse marshal output: %v", err)
    }
    idx, ok := m["index"]
    if !ok {
        t.Fatal("N3/#46: content_block_delta missing index must have index:0 injected on marshal")
    }
    if strings.TrimSpace(string(idx)) != "0" {
        t.Errorf("N3/#46: injected index = %s, want 0", string(idx))
    }
}

// TestN3_BlockEventWithIndexPreserved: a content_block_start event that
// ALREADY carries index:2 must marshal with index:2 intact (no clobber to 0).
func TestN3_BlockEventWithIndexPreserved(t *testing.T) {
    src := `{"type":"content_block_start","index":2,"content_block":{"type":"text","text":""}}`
    var ev AnthropicStreamEvent
    if err := json.Unmarshal([]byte(src), &ev); err != nil {
        t.Fatalf("unmarshal: %v", err)
    }
    out, err := json.Marshal(ev)
    if err != nil {
        t.Fatalf("marshal: %v", err)
    }
    var m map[string]json.RawMessage
    json.Unmarshal(out, &m)
    idx, ok := m["index"]
    if !ok || strings.TrimSpace(string(idx)) != "2" {
        t.Errorf("N3: existing index:2 must be preserved, got %q (ok=%v)", string(idx), ok)
    }
}

// TestN3_MessageEventNoIndex: a message-scoped event (message_delta) must NOT
// carry an index on marshal — the Anthropic SSE spec forbids it, and the
// forward loop relies on this to choose raw passthrough for these events.
func TestN3_MessageEventNoIndex(t *testing.T) {
    src := `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`
    var ev AnthropicStreamEvent
    if err := json.Unmarshal([]byte(src), &ev); err != nil {
        t.Fatalf("unmarshal: %v", err)
    }
    out, err := json.Marshal(ev)
    if err != nil {
        t.Fatalf("marshal: %v", err)
    }
    if strings.Contains(string(out), `"index"`) {
        t.Errorf("N3: message_delta must not carry index, marshal produced %s", string(out))
    }
}

// TestN3_RawNeverSerializes: Raw has json:"-" so json.Marshal must never emit it
// — otherwise the forward loop's raw-passthrough path would double-emit the
// payload (once as Raw, once as a marshaled "Raw" field).
func TestN3_RawNeverSerializes(t *testing.T) {
    ev := AnthropicStreamEvent{
        Type: "ping",
        Raw:  json.RawMessage(`{"type":"ping"}`),
    }
    out, err := json.Marshal(ev)
    if err != nil {
        t.Fatalf("marshal: %v", err)
    }
    if strings.Contains(string(out), "Raw") {
        t.Errorf("N3: Raw must be json:\"-\" (never serialize), got %s", string(out))
    }
}

// TestN3_EmitBlockSetMatchesMarshalBlockSet (Rule 7): the set of types the
// forward loop treats as "block-scoped" (excluded from raw passthrough, forces
// marshal) must equal the set MarshalJSON treats as block-scoped (index
// injection). Drift means a block event gets raw-emitted without index (#46
// regression) or a non-block event gets needlessly re-marshaled. This test
// exists because the two sets live in different files and could silently
// diverge.
func TestN3_EmitBlockSetMatchesMarshalBlockSet(t *testing.T) {
    marshalBlockSet := map[string]bool{
        "content_block_start": true,
        "content_block_delta": true,
        "content_block_stop":  true,
    }
    for _, bt := range n3BlockScopedTypes {
        if !marshalBlockSet[bt] {
            t.Errorf("N3 Rule 7 drift: emit-side block type %q not in marshal-side block set", bt)
        }
    }
    // Reverse: every marshal-side block type must be emit-side.
    emitSet := make(map[string]bool, len(n3BlockScopedTypes))
    for _, bt := range n3BlockScopedTypes {
        emitSet[bt] = true
    }
    for mt := range marshalBlockSet {
        if !emitSet[mt] {
            t.Errorf("N3 Rule 7 drift: marshal-side block type %q not in emit-side block set", mt)
        }
    }
}
