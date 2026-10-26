package server

import (
    "sort"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/store"
)

// streamTiming captures per-stream latency signals for the TTFT/TPOT report
// pipeline (plan §5.2, archi doc fusion-gateway-seriazation-1015.md):
//   - ttft: request start to first content chunk
//   - tpot: per-token, (duration - ttft) / (output_tokens - 1) at stamp time;
//     the chunk-gap mean is only a fallback while tokens are unknown
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
    // captureChunks enables per-chunk record collection for the
    // fg_stream_chunks table (G10, capture.stream_chunks — default off).
    // chunks is capped at streamTimingSampleCap; the cap only bounds rows
    // persisted for degenerate firehose streams, not the live metrics.
    captureChunks bool
    chunks        []store.ChunkTiming
}

// streamTimingSampleCap bounds the P95 sample reservoir. 2048 samples give a
// stable P95 for typical streams (< 500 chunks) and a bounded ~16 KB for
// degenerate firehose streams.
const streamTimingSampleCap = 2048

func newStreamTiming(start time.Time, captureChunks bool) *streamTiming {
    return &streamTiming{
    start:         start,
    deltas:        make([]float64, 0, 64),
    captureChunks: captureChunks,
    }
}

// observe records one chunk arrival. Call with time.Now() at the same spot
// the forward loop already stamps lastChunkAt/lastEventAt. bytes is the raw
// passthrough payload length (0 for frames the gateway marshals in-process).
func (st *streamTiming) observe(now time.Time, bytes int) {
    st.count++
    if st.firstAt.IsZero() {
        st.firstAt = now
    }
    delta := 0.0
    if !st.lastAt.IsZero() {
        delta = float64(now.Sub(st.lastAt)) / float64(time.Millisecond)
        st.totalDelta += delta
        st.deltaN++
        if len(st.deltas) < streamTimingSampleCap {
            st.deltas = append(st.deltas, delta)
        }
    }
    if st.captureChunks && len(st.chunks) < streamTimingSampleCap {
        st.chunks = append(st.chunks, store.ChunkTiming{
            Seq:     st.count,
            TS:      now,
            DeltaMS: delta,
            Bytes:   bytes,
        })
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
// Per-chunk basis — NOT the per-token TPOT that lands in RequestLog; see
// tpotPerTokenMS for the stored semantic.
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

// tpotPerTokenMS returns the per-token TPOT per the plan §5.2 formula:
// (duration - ttft) / (output_tokens - 1). When the output token count is
// unknown (<=0) it falls back to the mean inter-chunk gap so the signal is
// not lost; a single output token has no inter-token interval, so 0.
func tpotPerTokenMS(st *streamTiming, outputTokens int, durationMS float64) float64 {
    ttft := st.ttftMS()
    if outputTokens > 1 {
        if durationMS > ttft {
            return (durationMS - ttft) / float64(outputTokens-1)
        }
        return 0
    }
    if outputTokens == 1 {
        return 0
    }
    return st.tpotMS()
}

// stamp writes the captured signals onto the request-log entry (plan §4.2:
// RequestLog carries the analysis columns; bodies go to the P2 persistence
// sink). TPOT is per-token ((duration-ttft)/(output-1)); TPotP95 stays a
// per-chunk gap quantile. TPOT/TPotP95/ChunkCount are stream-only — non-stream
// callers pass a nil timing and only TTFT/duration land.
func stampTimingMetrics(entry *store.RequestLog, st *streamTiming, outputTokens int, durationMS float64) {
    if entry == nil {
        return
    }
    if st != nil {
        entry.TTFT = st.ttftMS()
        entry.TPotMS = tpotPerTokenMS(st, outputTokens, durationMS)
        entry.TPotP95 = st.tpotP95MS()
        entry.ChunkCount = st.count
        entry.TokensPerSec = st.tokensPerSec(outputTokens)
        // G10: per-chunk records ride the shared log entry to the persist
        // sink (json:"-" — never part of the in-memory log API).
        entry.Chunks = st.chunks
        return
    }
    // Non-stream: no chunk stream exists; first byte == full response.
    entry.TTFT = durationMS
}
