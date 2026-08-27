package server

import (
    "fmt"
    "log/slog"
    "sync"
    "time"
)

// streamEventFrame is one buffered SSE event for replay (issue #116). Seq is a
// monotonic per-stream sequence starting at 1; Frame is the exact bytes the
// forward loop wrote to the client (e.g. "id: <sid>:3\nevent: ...\ndata: ...\n\n").
type streamEventFrame struct {
    seq   int
    frame []byte
}

// streamBufferEntry holds the rolling event window for one stream plus the
// finalized flag the replay endpoint gates live-resume on. The forward loop
// Appends every event it writes; the upstream goroutine MarkFinalizes when the
// pump closes (clean [DONE] / read error / ctx cancel) so a replay knows when
// there are no more events coming.
type streamBufferEntry struct {
    mu        sync.Mutex
    sid       string
    frames    []streamEventFrame
    bytes     int
    maxEvents int
    maxBytes  int
    nextSeq   int
    finalized bool
    finalizedAt time.Time
    createdAt time.Time
    // cond wakes replay waiters when a new frame is appended or the stream is
    // finalized. Recreated per entry on demand via condOnce.
    cond     *sync.Cond
}

// StreamBufferStore holds resumable stream event windows keyed by stream_id
// (the X-Request-ID reused as the resume cursor namespace). Bounded by a TTL
// reaper (reaped entries are dropped — a reconnect after TTL cannot resume and
// must restart, which is the documented degradation) and a per-entry rolling
// cap (maxEvents / maxBytes). Thread-safe.
//
// Design: the buffered local path keeps the upstream pump alive past a client
// disconnect, so the buffer grows until the upstream finishes; a reconnect
// (Last-Event-ID) replays frames after the cursor then drains new frames until
// finalized. This trades holding the local slot for the whole generation for
// true mid-stream resumability — the upstream work is NOT restarted.
type StreamBufferStore struct {
    mu       sync.RWMutex
    buffers  map[string]*streamBufferEntry
    maxEvents int
    maxBytes  int
    ttl      time.Duration
    // maxEntries caps the number of concurrent buffered streams (audit R7). 0 =
    // unlimited. Open evicts the oldest entry (oldest createdAt) when the cap is
    // reached so a concurrent-SSE burst cannot grow the map to maxEntries ×
    // maxBytes before the periodic TTL reaper ticks. The newest live streams
    // stay resumable; the oldest (likely finalized/abandoned) is dropped.
    maxEntries int
}

func NewStreamBufferStore(maxEvents, maxBytes, maxEntries int, ttl time.Duration) *StreamBufferStore {
    if maxEvents <= 0 {
        maxEvents = 256
    }
    if maxBytes <= 0 {
        maxBytes = 1 << 20
    }
    if ttl <= 0 {
        ttl = 10 * time.Minute
    }
    return &StreamBufferStore{
        buffers:    make(map[string]*streamBufferEntry),
        maxEvents:  maxEvents,
        maxBytes:   maxBytes,
        ttl:        ttl,
        maxEntries: maxEntries,
    }
}

// Open registers a new stream buffer for sid and returns it. If sid already
// exists (reused X-Request-ID), the existing entry is reset to empty so a
// fresh generation does not mix with stale frames. Returns nil only if sid is
// empty.
func (s *StreamBufferStore) Open(sid string) *streamBufferEntry {
    if sid == "" {
        return nil
    }
    e := &streamBufferEntry{
        sid:       sid,
        maxEvents: s.maxEvents,
        maxBytes:  s.maxBytes,
        createdAt: time.Now(),
    }
    e.cond = sync.NewCond(&e.mu)
    s.mu.Lock()
    if old, ok := s.buffers[sid]; ok {
        slog.Warn("stream buffer: reusing sid, resetting stale entry", "sid", sid, "old_frames", len(old.frames))
    }
    // R7: enforce the global entries cap. When the map is at/over the cap and
    // this is a NEW sid (not a reuse above), evict the oldest entry (oldest
    // createdAt) before inserting — the periodic TTL reaper is tick-based, not
    // threshold-triggered, so a concurrent-SSE burst would otherwise grow the
    // map to maxEntries × maxBytes (1 GiB at 1 MiB/entry) before the next tick.
    // Evicting the oldest keeps the newest live streams resumable.
    if s.maxEntries > 0 && len(s.buffers) >= s.maxEntries {
        if _, exists := s.buffers[sid]; !exists {
            var oldestSid string
            var oldestAt time.Time
            for bSid, b := range s.buffers {
                if oldestSid == "" || b.createdAt.Before(oldestAt) {
                    oldestSid = bSid
                    oldestAt = b.createdAt
                }
            }
            if oldestSid != "" {
                delete(s.buffers, oldestSid)
                slog.Warn("stream buffer: entries cap reached, evicted oldest entry",
                    "evicted_sid", oldestSid, "cap", s.maxEntries, "opening_sid", sid)
            }
        }
    }
    s.buffers[sid] = e
    s.mu.Unlock()
    slog.Debug("stream buffer: opened", "sid", sid, "max_events", s.maxEvents, "max_bytes", s.maxBytes, "max_entries", s.maxEntries)
    return e
}

// Get returns the buffer for sid (nil if absent / evicted).
func (s *StreamBufferStore) Get(sid string) *streamBufferEntry {
    s.mu.RLock()
    e := s.buffers[sid]
    s.mu.RUnlock()
    return e
}

// Release evicts a buffer immediately (stream goroutine done, no replay wanted
// in-process). The TTL reaper is the backstop for entries whose goroutine did
// not call Release (client vanished + reconnect window).
func (s *StreamBufferStore) Release(sid string) {
    if sid == "" {
        return
    }
    s.mu.Lock()
    _, ok := s.buffers[sid]
    delete(s.buffers, sid)
    s.mu.Unlock()
    if ok {
        slog.Debug("stream buffer: released", "sid", sid)
    }
}

// ReapExpired drops entries older than TTL, returning the count. now is injected
// for deterministic tests. A finalized entry past its TTL is unrecoverable; a
// non-finalized entry past TTL means the upstream hung longer than the window —
// drop it (the local slot's own watchdog reaps the goroutine separately).
func (s *StreamBufferStore) ReapExpired(now time.Time) int {
    if s.ttl <= 0 {
        return 0
    }
    deadline := now.Add(-s.ttl)
    s.mu.Lock()
    reaped := 0
    for sid, e := range s.buffers {
        if !e.createdAt.After(deadline) {
            delete(s.buffers, sid)
            reaped++
        }
    }
    s.mu.Unlock()
    if reaped > 0 {
        slog.Warn("stream buffer: reaped expired entries", "reaped", reaped, "ttl", s.ttl.String())
    }
    return reaped
}

// Append records one event for the stream: it assigns the next seq, builds the
// SSE frame "id: <sid>:<seq>\ndata: <data>\n\n", stores it, and returns (seq,
// frame). The caller writes the SAME frame bytes to the client, so the buffer
// and the wire are byte-identical (a replay re-emits the exact event). The
// rolling window trims the oldest frame when maxEvents or maxBytes is exceeded
// — a replay can only reach the last N events, the documented bounded-window
// behavior. Returns (0, nil) when e is nil (resume disabled / empty sid).
func (e *streamBufferEntry) Append(data []byte) (seq int, frame []byte) {
    if e == nil {
        return 0, nil
    }
    e.mu.Lock()
    e.nextSeq++
    seq = e.nextSeq
    frame = []byte(fmt.Sprintf("id: %s:%d\ndata: %s\n\n", e.sid, seq, data))
    e.frames = append(e.frames, streamEventFrame{seq: seq, frame: frame})
    e.bytes += len(frame)
    trimmed := 0
    for (e.maxEvents > 0 && len(e.frames) > e.maxEvents) ||
        (e.maxBytes > 0 && e.bytes > e.maxBytes && len(e.frames) > 1) {
        dropped := e.frames[0]
        e.frames = e.frames[1:]
        e.bytes -= len(dropped.frame)
        trimmed++
    }
    e.cond.Broadcast()
    e.mu.Unlock()
    if trimmed > 0 {
        slog.Debug("stream buffer: trimmed oldest frames", "sid", e.sid, "trimmed", trimmed, "remaining", len(e.frames))
    }
    return seq, frame
}

// MarkFinalized signals that the upstream pump has closed — no more frames will
// be appended. Wakes any replay waiter so it can drain to the end and exit.
func (e *streamBufferEntry) MarkFinalized() {
    if e == nil {
        return
    }
    e.mu.Lock()
    if !e.finalized {
        e.finalized = true
        e.finalizedAt = time.Now()
        slog.Debug("stream buffer: finalized", "sid", e.sid, "frames", len(e.frames))
    }
    e.cond.Broadcast()
    e.mu.Unlock()
}

// IsFinalized reports whether the upstream pump has closed.
func (e *streamBufferEntry) IsFinalized() bool {
    if e == nil {
        return true
    }
    e.mu.Lock()
    defer e.mu.Unlock()
    return e.finalized
}

// FramesAfter returns a snapshot of frames with seq > afterSeq, in order. The
// returned slices are copies safe to write to a client outside the lock.
func (e *streamBufferEntry) FramesAfter(afterSeq int) []streamEventFrame {
    if e == nil {
        return nil
    }
    e.mu.Lock()
    defer e.mu.Unlock()
    out := make([]streamEventFrame, 0)
    for _, f := range e.frames {
        if f.seq > afterSeq {
            cp := make([]byte, len(f.frame))
            copy(cp, f.frame)
            out = append(out, streamEventFrame{seq: f.seq, frame: cp})
        }
    }
    return out
}

// LastSeq returns the highest seq currently buffered (0 if empty). Used by the
// replay endpoint to echo the cursor when no Last-Event-ID was sent.
func (e *streamBufferEntry) LastSeq() int {
    if e == nil {
        return 0
    }
    e.mu.Lock()
    defer e.mu.Unlock()
    if len(e.frames) == 0 {
        return 0
    }
    return e.frames[len(e.frames)-1].seq
}

// WaitForNew blocks until a frame with seq > afterSeq is appended, the stream
// is finalized, or timeout elapses. Returns the frames after the cursor (may be
// empty on timeout/finalized) and whether the stream is finalized. The replay
// endpoint loops this to drain live frames after the buffered replay.
func (e *streamBufferEntry) WaitForNew(afterSeq int, timeout time.Duration) (frames []streamEventFrame, finalized bool) {
    if e == nil {
        return nil, true
    }
    e.mu.Lock()
    defer e.mu.Unlock()
    // Fast path: frames already pending or already finalized.
    hasNew := false
    for _, f := range e.frames {
        if f.seq > afterSeq {
            hasNew = true
            break
        }
    }
    if !hasNew && !e.finalized && timeout > 0 {
        // Cond has no native timeout; spawn a timer to broadcast so Wait wakes.
        done := make(chan struct{})
        timer := time.AfterFunc(timeout, func() {
            e.mu.Lock()
            e.cond.Broadcast()
            e.mu.Unlock()
            close(done)
        })
        for !hasNew && !e.finalized {
            e.cond.Wait()
            for _, f := range e.frames {
                if f.seq > afterSeq {
                    hasNew = true
                    break
                }
            }
        }
        timer.Stop()
        select {
        case <-done:
        default:
        }
    }
    out := make([]streamEventFrame, 0)
    for _, f := range e.frames {
        if f.seq > afterSeq {
            cp := make([]byte, len(f.frame))
            copy(cp, f.frame)
            out = append(out, streamEventFrame{seq: f.seq, frame: cp})
        }
    }
    return out, e.finalized
}
