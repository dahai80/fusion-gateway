package server

import (
    "log/slog"
    "sort"
    "sync"
    "time"
)

// ActiveRequest is one handler observed by activeRequests: enough to name the
// request that held the shutdown drain open.
type ActiveRequest struct {
    Method string
    Path   string
    Trace  string
    Age    time.Duration
}

type activeRequestEntry struct {
    method  string
    path    string
    trace   string
    started time.Time
}

// activeRequests tracks every handler currently inside withMiddleware. It
// exists for one question that used to be unanswerable: "server shutdown drain
// timeout, forcing exit" told the operator nothing about WHAT exceeded the
// window, so every restart that cut a stream was diagnosed blind. Registration
// is a map insert on entry plus a delete on the deferred exit — cheap enough
// for the request path, and nil-safe so &Server{} literals in tests still work.
type activeRequests struct {
    mu    sync.Mutex
    seq   uint64
    items map[uint64]activeRequestEntry
}

func newActiveRequests() *activeRequests {
    return &activeRequests{items: make(map[uint64]activeRequestEntry)}
}

// Begin records a handler start and returns the slot id for End. The query
// string is deliberately dropped — it can carry API keys.
func (a *activeRequests) Begin(method, path, trace string) uint64 {
    if a == nil {
        return 0
    }
    a.mu.Lock()
    a.seq++
    id := a.seq
    a.items[id] = activeRequestEntry{method: method, path: path, trace: trace, started: time.Now()}
    a.mu.Unlock()
    return id
}

func (a *activeRequests) End(id uint64) {
    if a == nil || id == 0 {
        return
    }
    a.mu.Lock()
    delete(a.items, id)
    a.mu.Unlock()
}

// Snapshot returns the tracked handlers oldest first — the oldest is the one
// that has held the drain for longest.
func (a *activeRequests) Snapshot() []ActiveRequest {
    if a == nil {
        return nil
    }
    now := time.Now()
    a.mu.Lock()
    out := make([]ActiveRequest, 0, len(a.items))
    for _, e := range a.items {
        out = append(out, ActiveRequest{Method: e.method, Path: e.path, Trace: e.trace, Age: now.Sub(e.started)})
    }
    a.mu.Unlock()
    sort.Slice(out, func(i, j int) bool { return out[i].Age > out[j].Age })
    return out
}

// LogActiveRequests reports what was still running when the drain deadline
// fired. A non-empty snapshot names the exact requests that got cut; an empty
// one is itself evidence — no handler was mid-flight, so the wait was at the
// connection level (an idle keep-alive, or a route that bypasses
// withMiddleware) and the drain window cannot save anything.
func (s *Server) LogActiveRequests(note string) {
    snap := s.activeReqs.Snapshot()
    if len(snap) == 0 {
        slog.Error("no handler was in flight at the drain deadline — shutdown was held by a connection-level wait, not a running request",
            "note", note)
        return
    }
    slog.Error("in-flight requests at the drain deadline",
        "note", note,
        "count", len(snap),
        "oldest_age", snap[0].Age.Round(time.Second).String(),
    )
    const maxLogged = 20
    for i, ar := range snap {
        if i >= maxLogged {
            slog.Warn("further in-flight requests not listed", "remaining", len(snap)-maxLogged)
            break
        }
        slog.Warn("in-flight request at drain deadline",
            "method", ar.Method,
            "path", ar.Path,
            "trace", ar.Trace,
            "age", ar.Age.Round(time.Second).String(),
        )
    }
}
