package server

import (
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/store"
)

func ms(d float64) time.Duration {
    return time.Duration(d * float64(time.Millisecond))
}

func TestStreamTiming_TTFTAndTPOT(t *testing.T) {
    start := time.Now()
    st := newStreamTiming(start, false)

    st.observe(start.Add(ms(100)), 0) // first chunk: TTFT = 100ms
    st.observe(start.Add(ms(150)), 0) // gap 50ms
    st.observe(start.Add(ms(180)), 0) // gap 30ms
    st.observe(start.Add(ms(220)), 0) // gap 40ms

    if got := st.ttftMS(); got < 99 || got > 101 {
        t.Fatalf("ttftMS = %v, want ~100", got)
    }
    if got := st.tpotMS(); got < 39 || got > 41 {
        t.Fatalf("tpotMS = %v, want ~40 (mean of 50/30/40)", got)
    }
    if st.count != 4 {
        t.Fatalf("count = %d, want 4", st.count)
    }
    // generation window: 100ms..220ms = 120ms, 24 tokens -> 200 tok/s
    if got := st.tokensPerSec(24); got < 199 || got > 201 {
        t.Fatalf("tokensPerSec = %v, want ~200", got)
    }
}

func TestStreamTiming_SingleChunk(t *testing.T) {
    start := time.Now()
    st := newStreamTiming(start, false)
    st.observe(start.Add(ms(50)), 0)

    if got := st.ttftMS(); got < 49 || got > 51 {
        t.Fatalf("ttftMS = %v, want ~50", got)
    }
    if got := st.tpotMS(); got != 0 {
        t.Fatalf("tpotMS = %v, want 0 (no delta with <2 chunks)", got)
    }
    if got := st.tpotP95MS(); got != 0 {
        t.Fatalf("tpotP95MS = %v, want 0", got)
    }
    // degenerate window (first==last) -> 0, not a divide-by-zero
    if got := st.tokensPerSec(10); got != 0 {
        t.Fatalf("tokensPerSec = %v, want 0 for degenerate window", got)
    }
}

func TestStreamTiming_NoChunks(t *testing.T) {
    st := newStreamTiming(time.Now(), false)
    if st.ttftMS() != 0 || st.tpotMS() != 0 || st.tpotP95MS() != 0 || st.count != 0 {
        t.Fatalf("empty timing must be all-zero, got ttft=%v tpot=%v p95=%v count=%d",
            st.ttftMS(), st.tpotMS(), st.tpotP95MS(), st.count)
    }
}

func TestStreamTiming_SampleCapKeepsExactMean(t *testing.T) {
    start := time.Now()
    st := newStreamTiming(start, false)
    total := streamTimingSampleCap + 500
    for i := 1; i <= total; i++ {
        st.observe(start.Add(time.Duration(i) * ms(2)), 0) // uniform 2ms gaps
    }
    if len(st.deltas) != streamTimingSampleCap {
        t.Fatalf("reservoir len = %d, want capped at %d", len(st.deltas), streamTimingSampleCap)
    }
    if got := st.tpotMS(); got < 1.9 || got > 2.1 {
        t.Fatalf("tpotMS = %v, want ~2.0 exactly despite cap", got)
    }
    if st.count != total {
        t.Fatalf("count = %d, want %d", st.count, total)
    }
}

func TestStampTimingMetrics_Stream(t *testing.T) {
    start := time.Now()
    st := newStreamTiming(start, false)
    st.observe(start.Add(ms(100)), 0)
    st.observe(start.Add(ms(150)), 0)

    entry := &store.RequestLog{}
    stampTimingMetrics(entry, st, 10, 999)
    if entry.TTFT < 99 || entry.TTFT > 101 {
        t.Fatalf("entry.TTFT = %v, want ~100", entry.TTFT)
    }
    // per-token formula: (999-100)/(10-1) ≈ 99.9 ms/token
    if entry.TPotMS < 98.8 || entry.TPotMS > 101 {
        t.Fatalf("entry.TPotMS = %v, want ~99.9 (per-token formula)", entry.TPotMS)
    }
    if entry.ChunkCount != 2 {
        t.Fatalf("entry.ChunkCount = %d, want 2", entry.ChunkCount)
    }
    if entry.TokensPerSec <= 0 {
        t.Fatalf("entry.TokensPerSec = %v, want >0", entry.TokensPerSec)
    }
}

func TestStampTimingMetrics_NonStreamAndNil(t *testing.T) {
    entry := &store.RequestLog{}
    stampTimingMetrics(entry, nil, 42, 1234.5)
    if entry.TTFT != 1234.5 {
        t.Fatalf("non-stream TTFT = %v, want duration 1234.5", entry.TTFT)
    }
    if entry.TPotMS != 0 || entry.ChunkCount != 0 || entry.TokensPerSec != 0 {
        t.Fatalf("non-stream must not carry stream-only fields, got %+v", entry)
    }
    // nil entry must not panic
    stampTimingMetrics(nil, nil, 0, 0)
}

func TestStampTimingMetrics_TPOTEdgeCases(t *testing.T) {
    start := time.Now()
    st := newStreamTiming(start, false)
    st.observe(start.Add(ms(100)), 0)
    st.observe(start.Add(ms(150)), 0)

    // single output token: no inter-token interval
    e1 := &store.RequestLog{}
    stampTimingMetrics(e1, st, 1, 500)
    if e1.TPotMS != 0 {
        t.Fatalf("TPotMS = %v, want 0 for single output token", e1.TPotMS)
    }

    // unknown token count: falls back to mean chunk gap (~50)
    e2 := &store.RequestLog{}
    stampTimingMetrics(e2, st, 0, 500)
    if e2.TPotMS < 49 || e2.TPotMS > 51 {
        t.Fatalf("TPotMS = %v, want ~50 (chunk-gap fallback)", e2.TPotMS)
    }

    // degenerate duration <= ttft: 0, not negative
    e3 := &store.RequestLog{}
    stampTimingMetrics(e3, st, 10, 50)
    if e3.TPotMS != 0 {
        t.Fatalf("TPotMS = %v, want 0 when duration <= ttft", e3.TPotMS)
    }
}

func TestStreamTiming_ChunkCapture(t *testing.T) {
    start := time.Now()
    st := newStreamTiming(start, true)
    st.observe(start.Add(ms(100)), 120)
    st.observe(start.Add(ms(150)), 80)

    if len(st.chunks) != 2 {
        t.Fatalf("captured %d chunks, want 2", len(st.chunks))
    }
    if st.chunks[0].Seq != 1 || st.chunks[0].DeltaMS != 0 || st.chunks[0].Bytes != 120 {
        t.Fatalf("first chunk = %+v, want seq=1 delta=0 bytes=120", st.chunks[0])
    }
    if st.chunks[1].Seq != 2 || st.chunks[1].DeltaMS < 49 || st.chunks[1].DeltaMS > 51 || st.chunks[1].Bytes != 80 {
        t.Fatalf("second chunk = %+v, want seq=2 delta~50 bytes=80", st.chunks[1])
    }
    // stamp rides them onto the log entry for the persist sink
    entry := &store.RequestLog{}
    stampTimingMetrics(entry, st, 5, 500)
    if len(entry.Chunks) != 2 {
        t.Fatalf("entry.Chunks len = %d, want 2", len(entry.Chunks))
    }
}

func TestStreamTiming_ChunkCaptureOff(t *testing.T) {
    st := newStreamTiming(time.Now(), false)
    st.observe(time.Now(), 10)
    st.observe(time.Now(), 10)
    if len(st.chunks) != 0 {
        t.Fatalf("capture off must collect nothing, got %d", len(st.chunks))
    }
    entry := &store.RequestLog{}
    stampTimingMetrics(entry, st, 5, 500)
    if len(entry.Chunks) != 0 {
        t.Fatalf("entry.Chunks must stay empty with capture off")
    }
}

func TestStreamTiming_ChunkCaptureCapped(t *testing.T) {
    start := time.Now()
    st := newStreamTiming(start, true)
    for i := 1; i <= streamTimingSampleCap+50; i++ {
        st.observe(start.Add(time.Duration(i)*ms(2)), 1)
    }
    if len(st.chunks) != streamTimingSampleCap {
        t.Fatalf("chunk rows = %d, want capped at %d", len(st.chunks), streamTimingSampleCap)
    }
    if st.count != streamTimingSampleCap+50 {
        t.Fatalf("count = %d, want %d (metrics unaffected by chunk cap)", st.count, streamTimingSampleCap+50)
    }
}

func TestStampTimingMetrics_P95(t *testing.T) {
    start := time.Now()
    st := newStreamTiming(start, false)
    // 10 uniform 10ms gaps then two 500ms stalls: with 12 samples P95 (idx 10)
    // must land on a stall, not the uniform mode.
    at := start
    for i := 0; i < 11; i++ {
        at = at.Add(ms(10))
        st.observe(at, 0)
    }
    at = at.Add(ms(500))
    st.observe(at, 0)
    at = at.Add(ms(500))
    st.observe(at, 0)

    entry := &store.RequestLog{}
    stampTimingMetrics(entry, st, 0, 0)
    if entry.TPotP95 < 499 {
        t.Fatalf("TPotP95 = %v, want the 500ms stall (>=499)", entry.TPotP95)
    }
}
