package server

// Thinking-block stream regression: glm5.2 via LiteLLM emits a thinking
// content_block_start carrying empty "thinking":"" and "signature":"" keys.
// The Anthropic SDK requires a thinking block to carry both keys; a block
// missing them cannot be finalized and Claude Code surfaces
// "API Error: Content block not found" once the thinking deltas drain (the
// "Thought for N s" then crash symptom). selectAnthropicEventData is the
// emit-path selector; these tests pin that it preserves the empty keys
// verbatim via raw passthrough instead of re-marshaling through a struct
// whose omitempty drops them.

import (
    "strings"
    "testing"

    "github.com/fusion-gateway/fusion-gateway/internal/adapter"
)

// TestSelectEventData_ThinkingBlockStartPreservesEmptyKeys is the root-cause
// regression: a thinking content_block_start with empty thinking+signature
// must pass through with both empty keys intact, not be re-marshaled to
// {"type":"thinking"} (which the SDK rejects as a malformed thinking block).
func TestSelectEventData_ThinkingBlockStartPreservesEmptyKeys(t *testing.T) {
    raw := []byte(`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`)
    ev := adapter.AnthropicStreamEvent{
        Type: "content_block_start",
        Index: 0,
        Raw:   append([]byte(nil), raw...),
    }
    out := selectAnthropicEventData(ev)
    if out == nil {
        t.Fatal("expected non-nil event data for thinking content_block_start")
    }
    s := string(out)
    if !strings.Contains(s, `"thinking":""`) {
        t.Errorf("thinking block start lost empty \"thinking\" key (omitempty drop): %s", s)
    }
    if !strings.Contains(s, `"signature":""`) {
        t.Errorf("thinking block start lost empty \"signature\" key (omitempty drop): %s", s)
    }
}

// TestSelectEventData_TextDeltaRawPassthrough confirms a normal text_delta
// block event that already carries index passes through verbatim (the
// happy path most streams take).
func TestSelectEventData_TextDeltaRawPassthrough(t *testing.T) {
    raw := []byte(`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Hello."}}`)
    ev := adapter.AnthropicStreamEvent{
        Type: "content_block_delta",
        Index: 1,
        Raw:   append([]byte(nil), raw...),
    }
    out := selectAnthropicEventData(ev)
    if string(out) != string(raw) {
        t.Errorf("text_delta raw passthrough expected verbatim, got: %s", string(out))
    }
}

// TestSelectEventData_BlockEventMissingIndexInjectsIndex pins the issue #46
// safety net: a block-scoped event whose Raw lacks an "index" field still
// re-marshals so MarshalJSON injects index:0. Raw passthrough must NOT skip
// the injection for index-less upstreams.
func TestSelectEventData_BlockEventMissingIndexInjectsIndex(t *testing.T) {
    raw := []byte(`{"type":"content_block_start","content_block":{"type":"text","text":""}}`)
    ev := adapter.AnthropicStreamEvent{
        Type: "content_block_start",
        Index: 0,
        Raw:   append([]byte(nil), raw...),
    }
    out := selectAnthropicEventData(ev)
    if out == nil {
        t.Fatal("expected non-nil event data for index-less content_block_start")
    }
    if !strings.Contains(string(out), `"index"`) {
        t.Errorf("index-less block event must be re-marshaled to inject index, got: %s", string(out))
    }
}

// TestSelectEventData_SynthesizedEventMarshals pins that in-process
// synthesized events (Raw nil) still marshal through the struct path.
func TestSelectEventData_SynthesizedEventMarshals(t *testing.T) {
    ev := adapter.AnthropicStreamEvent{
        Type:  "message_stop",
    }
    out := selectAnthropicEventData(ev)
    if !strings.Contains(string(out), `"type":"message_stop"`) {
        t.Errorf("synthesized message_stop must marshal, got: %s", string(out))
    }
}
