// Package persist defines the request-persistence contract (plan §4.2):
// an async, bounded, non-blocking sink that survives gateway restarts by
// serializing every request log (+ optionally its payloads) to PostgreSQL.
//
// The hot path NEVER blocks on persistence: Enqueue is a channel send with a
// keep-new drop policy; the batch writer drains on its own worker.
package persist

import (
    "context"
    "errors"

    "github.com/fusion-gateway/fusion-gateway/internal/store"
)

// ErrDropped is returned by Enqueue when the bounded queue was full and an
// entry was discarded (the oldest, keep-new policy). Callers log-and-move-on:
// persistence is observability, never a request-path dependency.
var ErrDropped = errors.New("persist: queue full, entry dropped")

// Entry is one fully-assembled persistence row: the request log plus the
// captured bodies. Assembled ONCE at request finalize (all exits — success,
// error, client cancel, watchdog) and never mutated after Enqueue.
type Entry struct {
    Log store.RequestLog

    // Endpoint is the bare path ("/v1/chat/completions"); RequestType on Log
    // keeps the historical "METHOD path" composite for the in-memory store.
    Endpoint    string
    BackendType string // local | cloud (Log.ChannelType carries this too — denormalized for the PG column)
    ClientIP    string
    UserAgent   string

    ReqBody  []byte // truncated to capture.max_body_bytes, nil when disabled
    RespBody []byte // truncated; for streams this is the raw SSE byte stream

    ReqTruncated  bool
    RespTruncated bool
    // RespRawMime marks how RespBody was captured: "application/json"
    // (non-stream) or "text/event-stream" (raw frames, not reassembled).
    RespRawMime string

    // Log.Chunks carries the optional per-chunk timings (capture.stream_chunks,
    // default off, plan §4.1 fg_stream_chunks).
}

// Sink is the persistence backend contract. Implementations must be safe for
// concurrent Enqueue calls and must drain on Close (shutdown drain, R1/R6
// pattern: no in-flight entry lost on graceful stop).
type Sink interface {
    // Enqueue is non-blocking. Returns ErrDropped when the queue is full.
    Enqueue(entry *Entry) error
    // Close drains the queue then releases resources. Context deadline caps
    // the drain wait.
    Close(ctx context.Context) error
}
