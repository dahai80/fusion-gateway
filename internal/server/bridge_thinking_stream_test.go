package server

import (
    "net/http/httptest"
    "strings"
    "testing"
)

// TestSSEBridge_ThinkingStream (#329): reasoning_content deltas open a
// thinking block first; the block seals with signature_delta + stop when text
// begins, and the full sequence stays protocol-valid.
func TestSSEBridge_ThinkingStream(t *testing.T) {
    inner := httptest.NewRecorder()
    b := newAnthropicSSEBridge(inner)
    b.Write([]byte("data: {\"id\":\"c9\",\"model\":\"qwen3\",\"choices\":[{\"delta\":{\"reasoning_content\":\"step one\"}}]}\n\n"))
    b.Write([]byte("data: {\"id\":\"c9\",\"model\":\"qwen3\",\"choices\":[{\"delta\":{\"reasoning_content\":\" step two\"}}]}\n\n"))
    b.Write([]byte("data: {\"id\":\"c9\",\"model\":\"qwen3\",\"choices\":[{\"delta\":{\"content\":\"answer\"}}]}\n\n"))
    b.Write([]byte("data: {\"id\":\"c9\",\"model\":\"qwen3\",\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"))
    b.Write([]byte("data: [DONE]\n\n"))

    var seq []string
    for _, f := range bridgeFrames(t, inner.Body.String()) {
        seq = append(seq, f[0])
    }
    want := []string{
        "message_start",
        "content_block_start", "content_block_delta", "content_block_delta",
        "content_block_delta", "content_block_stop",
        "content_block_start", "content_block_delta", "content_block_stop",
        "message_delta", "message_stop",
    }
    if strings.Join(seq, ",") != strings.Join(want, ",") {
        t.Fatalf("event seq = %v, want %v", seq, want)
    }
    raw := inner.Body.String()
    if !strings.Contains(raw, "thinking_delta") || !strings.Contains(raw, "step one") {
        t.Fatalf("thinking deltas missing: %s", raw)
    }
    if !strings.Contains(raw, "signature_delta") || !strings.Contains(raw, bridgeThinkingSignature) {
        t.Fatalf("signature delta missing: %s", raw)
    }
    // Thinking block index 0 precedes text block index 1.
    if !strings.Contains(raw, "\"index\":0") || !strings.Contains(raw, "\"index\":1") {
        t.Fatalf("block indexes missing: %s", raw)
    }
}

// TestSSEBridge_ThinkingOnlyStream (#329 degenerate case): model emits
// reasoning then EOS with no content — thinking block still seals cleanly.
func TestSSEBridge_ThinkingOnlyStream(t *testing.T) {
    inner := httptest.NewRecorder()
    b := newAnthropicSSEBridge(inner)
    b.Write([]byte("data: {\"id\":\"c9\",\"model\":\"qwen3\",\"choices\":[{\"delta\":{\"reasoning_content\":\"only reasoning\"}}]}\n\n"))
    b.Write([]byte("data: {\"id\":\"c9\",\"model\":\"qwen3\",\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"))
    b.Write([]byte("data: [DONE]\n\n"))

    var seq []string
    for _, f := range bridgeFrames(t, inner.Body.String()) {
        seq = append(seq, f[0])
    }
    want := []string{
        "message_start",
        "content_block_start", "content_block_delta",
        "content_block_delta", "content_block_stop",
        "message_delta", "message_stop",
    }
    if strings.Join(seq, ",") != strings.Join(want, ",") {
        t.Fatalf("event seq = %v, want %v", seq, want)
    }
    if !strings.Contains(inner.Body.String(), "only reasoning") {
        t.Fatalf("reasoning lost: %s", inner.Body.String())
    }
}
