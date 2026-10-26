// Package filelog implements the G9 daily-rolled file logging handler:
// gateway-YYYY-MM-DD.log under file_log.dir, rotated on the first write past
// local midnight, pruning files older than file_log.retain_days at rotation.
// Self-implemented (~100 lines) — a rolling dependency for one daily-open +
// prune behavior is not worth an import.
package filelog

import (
    "context"
    "fmt"
    "log/slog"
    "os"
    "path/filepath"
    "sort"
    "strings"
    "sync"
    "sync/atomic"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

// dailyWriter is the rotating sink. Rotation check happens on Write under the
// mutex — no background goroutine needed (EI10: nothing to join).
type dailyWriter struct {
    mu         sync.Mutex
    dir        string
    prefix     string
    retainDays int
    day        string
    f          *os.File
}

func (w *dailyWriter) Write(p []byte) (int, error) {
    w.mu.Lock()
    defer w.mu.Unlock()
    day := time.Now().Format("2006-01-02")
    if w.f == nil || day != w.day {
        if err := w.rotateLocked(day); err != nil {
            return 0, err
        }
    }
    return w.f.Write(p)
}

// Close flushes and closes the current file (graceful shutdown path).
func (w *dailyWriter) Close() error {
    w.mu.Lock()
    defer w.mu.Unlock()
    if w.f == nil {
        return nil
    }
    err := w.f.Close()
    w.f = nil
    return err
}

// rotateLocked opens the new day file and prunes expired ones. A rotation
// failure keeps the writer usable: the next Write retries.
//
// Logging here goes STRAIGHT to stderr, never through slog: this runs inside
// Write with w.mu held, and slog's fanout would re-enter this writer (and
// JSONHandler holds its own mutex across Handle → Write, so a nested log
// deadlocks both).
func (w *dailyWriter) rotateLocked(day string) error {
    if w.f != nil {
        _ = w.f.Close()
        w.f = nil
    }
    if err := os.MkdirAll(w.dir, 0o755); err != nil {
        return fmt.Errorf("filelog mkdir %s: %w", w.dir, err)
    }
    path := filepath.Join(w.dir, w.prefix+"-"+day+".log")
    f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
    if err != nil {
        return fmt.Errorf("filelog open %s: %w", path, err)
    }
    w.f = f
    w.day = day
    if err := w.pruneLocked(); err != nil {
        // Prune failure must not break logging — the file is already open.
        fmt.Fprintf(os.Stderr, "filelog prune failed dir=%s error=%v\n", w.dir, err)
    }
    fmt.Fprintf(os.Stderr, "filelog rotated path=%s retain_days=%d\n", path, w.retainDays)
    return nil
}

// pruneLocked deletes prefix-YYYY-MM-DD.log files older than retainDays.
// retainDays <= 0 keeps everything.
func (w *dailyWriter) pruneLocked() error {
    if w.retainDays <= 0 {
        return nil
    }
    entries, err := os.ReadDir(w.dir)
    if err != nil {
        return err
    }
    cutoff := time.Now().AddDate(0, 0, -w.retainDays).Format("2006-01-02")
    for _, e := range entries {
        name := e.Name()
        if e.IsDir() || !strings.HasPrefix(name, w.prefix+"-") || !strings.HasSuffix(name, ".log") {
            continue
        }
        day := strings.TrimSuffix(strings.TrimPrefix(name, w.prefix+"-"), ".log")
        if len(day) != 10 || day >= cutoff {
            continue
        }
        if err := os.Remove(filepath.Join(w.dir, name)); err != nil {
            return err
        }
    }
    return nil
}

// fanoutHandler forwards each record to every enabled handler. Enabled is
// OR-ed so the noisiest handler decides; Handle clones the record per sink
// (slog contract: a handler may mutate the record it receives).
type fanoutHandler struct {
    hs []slog.Handler
}

func (h *fanoutHandler) Enabled(ctx context.Context, l slog.Level) bool {
    for _, sub := range h.hs {
        if sub.Enabled(ctx, l) {
            return true
        }
    }
    return false
}

func (h *fanoutHandler) Handle(ctx context.Context, r slog.Record) error {
    var firstErr error
    for _, sub := range h.hs {
        if !sub.Enabled(ctx, r.Level) {
            continue
        }
        if err := sub.Handle(ctx, r.Clone()); err != nil && firstErr == nil {
            firstErr = err
        }
    }
    return firstErr
}

func (h *fanoutHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
    next := &fanoutHandler{hs: make([]slog.Handler, len(h.hs))}
    for i, sub := range h.hs {
        next.hs[i] = sub.WithAttrs(attrs)
    }
    return next
}

func (h *fanoutHandler) WithGroup(name string) slog.Handler {
    next := &fanoutHandler{hs: make([]slog.Handler, len(h.hs))}
    for i, sub := range h.hs {
        next.hs[i] = sub.WithGroup(name)
    }
    return next
}

// parseLevel maps the config string to a slog.Level (empty = info).
func parseLevel(s string) slog.Level {
    switch s {
    case "debug":
        return slog.LevelDebug
    case "warn":
        return slog.LevelWarn
    case "error":
        return slog.LevelError
    default:
        return slog.LevelInfo
    }
}

// installed guards against double-Setup: a second call would wrap the
// fanout in another fanout, and slog.SetDefault's std-log rewiring would
// then recurse fanout → defaultHandler → log.Logger → fanout → … forever.
var installed atomic.Bool

// Setup installs the file handler as the default logger, fanning out to
// stderr (explicit TextHandler — the previous default handler must NOT be
// wrapped: SetDefault rewires the std log package through the new default,
// so nesting the old handler creates an infinite log loop / mutex deadlock)
// and the rolling file. Returns a Close func for graceful shutdown, or nil
// when file logging is disabled or already installed.
func Setup(cfg config.FileLogConfig, fallbackLevel string) func() {
    if !cfg.Enabled {
        return nil
    }
    if !installed.CompareAndSwap(false, true) {
        return nil
    }
    dir := cfg.Dir
    if dir == "" {
        dir = "~/.fusion-gateway/logs"
    }
    dir = config.ExpandPath(dir)
    level := cfg.Level
    if level == "" {
        level = fallbackLevel
    }
    w := &dailyWriter{dir: dir, prefix: "gateway", retainDays: cfg.RetainDays}
    stderrH := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: parseLevel(fallbackLevel)})
    fileH := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: parseLevel(level)})
    slog.SetDefault(slog.New(&fanoutHandler{hs: []slog.Handler{stderrH, fileH}}))
    slog.Info("filelog enabled", "dir", dir, "level", level, "retain_days", cfg.RetainDays)
    return func() {
        if err := w.Close(); err != nil {
            slog.Warn("filelog close failed", "error", err)
        }
    }
}

// sortedDayFilenames is a test helper: the day files currently in dir,
// sorted by name.
func sortedDayFilenames(dir, prefix string) []string {
    entries, err := os.ReadDir(dir)
    if err != nil {
        return nil
    }
    var out []string
    for _, e := range entries {
        if !e.IsDir() && strings.HasPrefix(e.Name(), prefix+"-") && strings.HasSuffix(e.Name(), ".log") {
            out = append(out, e.Name())
        }
    }
    sort.Strings(out)
    return out
}
