package server

import (
    "sort"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/store"
)

// streamTiming captures per-stream latency signals for the TTFT/TPOT report
// pipeline (plan §5.2, archi doc fusion-gateway-seriazation-1015.md):
//   - ttft: request start to first content chunk
//   - tpot: mean inter-chunk gap (chunk-delta mean, uncapped count)
//   - tpot_p95: P95 of the inter-chunk gaps (bounded sample reservoir)
//   - tokens_per_sec: output tokens over the generation window
//
// Pure observation — the forward loops call observe() alongside their existing
// bookkeeping and nothing on the hot path blocks. The delta sample reservoir
// is capped so a pathological 100k-chunk stream cannot grow memory linearly;
// the cap only affects P95 precision (mean stays exact via totalDelta/deltaN).
type streamTiming struct {
    start   time.Time
    firstAt time.Time
    lastAt  time.Time
    count   int
    // deltas holds a bounded sample of inter-chunk gaps in ms for the P95
    // estimate; totalDelta/deltaN keep the exact mean regardless of cap.
    deltas     []float64
    totalDelta float64
    deltaN     int
}

// streamTimingSampleCap bounds the P95 sample reservoir. 2048 samples give a
// stable P95 for typical streams (< 500 chunks) and a bounded ~16 KB for
// degenerate firehose streams.
const streamTimingSampleCap = 2048

func newStreamTiming(start time.Time) *streamTiming {
    return &streamTiming{
        start:  start,
        deltas: make([]float64, 0, 64),
    }
}

// observe records one chunk arrival. Call with time.Now() at the same spot
// the forward loop already stamps lastChunkAt/lastEventAt.
func (st *streamTiming) observe(now time.Time) {
    st.count++
    if st.firstAt.IsZero() {
        st.firstAt = now
    }
    if !st.lastAt.IsZero() {
        d := float64(now.Sub(st.lastAt)) / float64(time.Millisecond)
        st.totalDelta += d
        st.deltaN++
        if len(st.deltas) < streamTimingSampleCap {
            st.deltas = append(st.deltas, d)
        }
    }
    st.lastAt = now
}

// ttftMS returns the first-chunk latency in ms, or 0 when no chunk arrived.
func (st *streamTiming) ttftMS() float64 {
    if st.firstAt.IsZero() {
        return 0
    }
    return float64(st.firstAt.Sub(st.start)) / float64(time.Millisecond)
}

// tpotMS returns the mean inter-chunk gap in ms (0 when fewer than 2 chunks).
func (st *streamTiming) tpotMS() float64 {
    if st.deltaN == 0 {
        return 0
    }
    return st.totalDelta / float64(st.deltaN)
}

// tpotP95MS returns the P95 inter-chunk gap over the sample reservoir.
func (st *streamTiming) tpotP95MS() float64 {
    if len(st.deltas) == 0 {
        return 0
    }
    sorted := make([]float64, len(st.deltas))
    copy(sorted, st.deltas)
    sort.Float64s(sorted)
    idx := int(float64(len(sorted)-1) * 0.95)
    return sorted[idx]
}

// tokensPerSec returns output tokens over the generation window (first chunk
// to last chunk); 0 when no chunk arrived or the window is degenerate.
func (st *streamTiming) tokensPerSec(outputTokens int) float64 {
    if st.firstAt.IsZero() || st.lastAt.Sub(st.firstAt) <= 0 || outputTokens <= 0 {
        return 0
    }
    secs := st.lastAt.Sub(st.firstAt).Seconds()
    return float64(outputTokens) / secs
}

// stamp writes the captured signals onto the request-log entry (plan §4.2:
// RequestLog carries the analysis columns; bodies go to the P2 persistence
// sink). TPOT/TPotP95/ChunkCount are stream-only — non-stream callers pass a
// nil timing and only TTFT/duration land.
func stampTimingMetrics(entry *store.RequestLog, st *streamTiming, outputTokens int, durationMS float64) {
    if entry == nil {
        return
    }
    if st != nil {
        entry.TTFT = st.ttftMS()
        entry.TPotMS = st.tpotMS()
        entry.TPotP95 = st.tpotP95MS()
        entry.ChunkCount = st.count
        entry.TokensPerSec = st.tokensPerSec(outputTokens)
        return
    }
    // Non-stream: no chunk stream exists; first byte == full response.
    entry.TTFT = durationMS
}
