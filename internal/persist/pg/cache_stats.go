// #421: fg_cache_stats writer — 30s cumulative-counter snapshots of the
// gateway's in-process caches (response / semantic). The counters are
// atomic.Int64 in-memory and reset on restart; the insights overview derives
// per-window deltas between adjacent samples, so a restart only loses one
// sampling window. Best-effort side channel like the hw sampler: failures
// log at WARN and retry on the next tick, never block the request path.
package pg

import (
    "context"
    "log/slog"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/lifecycle"
)

// cacheSampleInterval: one row per cache per 30s, same cadence as hw samples.
const cacheSampleInterval = 30 * time.Second

// CacheCounterSnapshot is one cache's cumulative counters at an instant.
type CacheCounterSnapshot struct {
    Name   string
    Hits   int64
    Misses int64
    Size   int64
}

// writeCacheStats inserts one snapshot row; a (ts, cache) PK collision
// (restart within the same second) is ignored.
func (w *copyWriter) writeCacheStats(ctx context.Context, ts time.Time, s CacheCounterSnapshot) error {
    _, err := w.pool.Exec(ctx,
        `INSERT INTO fg_cache_stats (ts, cache, hits, misses, size)
         VALUES ($1,$2,$3,$4,$5)
         ON CONFLICT (ts, cache) DO NOTHING`,
        ts, s.Name, s.Hits, s.Misses, s.Size)
    return err
}

// StartCacheSampler launches the 30s cache-counter worker (lifecycle.Worker —
// joined by Close). sources are closures over the server's caches; a source
// returning nil (cache disabled) is skipped for that tick. No-op when the
// writer is a test fake or persistence is disabled.
func (s *Sink) StartCacheSampler(parent context.Context, sources []func() *CacheCounterSnapshot) {
    if _, ok := s.writer.(*copyWriter); !ok {
        return
    }
    if len(sources) == 0 {
        return
    }
    s.cacheSampler = lifecycle.Start(parent, "pg-cache-sampler", func(ctx context.Context) {
        s.cacheSampleLoop(ctx, sources)
    })
    slog.Info("pg cache sampler started", "interval", cacheSampleInterval.String(),
        "sources", len(sources))
}

// cacheSampleLoop mirrors hwSampleLoop: immediate first sample, then every
// 30s. Insert failures log at WARN and wait for the next tick.
func (s *Sink) cacheSampleLoop(ctx context.Context, sources []func() *CacheCounterSnapshot) {
    s.cacheSampleOnce(ctx, sources)
    ticker := time.NewTicker(cacheSampleInterval)
    defer ticker.Stop()
    for {
        select {
        case <-ctx.Done():
            return
        case <-s.stopCh:
            return
        case <-ticker.C:
            s.cacheSampleOnce(ctx, sources)
        }
    }
}

func (s *Sink) cacheSampleOnce(ctx context.Context, sources []func() *CacheCounterSnapshot) {
    cw, ok := s.writer.(*copyWriter)
    if !ok {
        return
    }
    ts := time.Now().UTC()
    for _, src := range sources {
        snap := src()
        if snap == nil || snap.Name == "" {
            continue
        }
        writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
        if err := cw.writeCacheStats(writeCtx, ts, *snap); err != nil {
            slog.Warn("pg cache stats write failed", "cache", snap.Name, "error", err)
        }
        cancel()
    }
}
