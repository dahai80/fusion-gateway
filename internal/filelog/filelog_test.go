package filelog

import (
    "context"
    "log/slog"
    "os"
    "path/filepath"
    "strings"
    "testing"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

func TestDailyWriter_WriteAndRotate(t *testing.T) {
    dir := t.TempDir()
    // retainDays=0 keeps every day file — the fake 2000-* days are far past
    // any real retention cutoff and would be pruned by rotation otherwise.
    w := &dailyWriter{dir: dir, prefix: "gateway", retainDays: 0}
    if _, err := w.Write([]byte("line0\n")); err != nil {
        t.Fatalf("first write: %v", err)
    }
    // Simulate day changes by rotating directly (Write always targets the
    // real current day, so the day flip is driven here).
    w.mu.Lock()
    for _, day := range []string{"2000-01-01", "2000-01-02"} {
        if err := w.rotateLocked(day); err != nil {
            w.mu.Unlock()
            t.Fatalf("rotate %s: %v", day, err)
        }
        if _, err := w.f.Write([]byte("line-" + day + "\n")); err != nil {
            w.mu.Unlock()
            t.Fatalf("write into %s: %v", day, err)
        }
    }
    w.mu.Unlock()
    if err := w.Close(); err != nil {
        t.Fatalf("close: %v", err)
    }
    files := sortedDayFilenames(dir, "gateway")
    if len(files) != 3 {
        t.Fatalf("want 3 day files after rotations, got %v", files)
    }
    second, err := os.ReadFile(filepath.Join(dir, "gateway-2000-01-02.log"))
    if err != nil || !strings.Contains(string(second), "line-2000-01-02") {
        t.Fatalf("post-rotation write landed in wrong file: %q err=%v", second, err)
    }
}

func TestDailyWriter_PruneExpired(t *testing.T) {
    dir := t.TempDir()
    w := &dailyWriter{dir: dir, prefix: "gateway", retainDays: 2}
    // Seed an expired file (day older than the 2-day cutoff) and a fresh one.
    expired := filepath.Join(dir, "gateway-2000-01-01.log")
    fresh := filepath.Join(dir, "gateway-2999-01-01.log")
    for _, p := range []string{expired, fresh} {
        if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
            t.Fatal(err)
        }
    }
    if _, err := w.Write([]byte("today\n")); err != nil {
        t.Fatalf("write: %v", err)
    }
    defer w.Close()
    if _, err := os.Stat(expired); !os.IsNotExist(err) {
        t.Fatalf("expired file must be pruned, stat err=%v", err)
    }
    if _, err := os.Stat(fresh); err != nil {
        t.Fatalf("fresh file must survive prune: %v", err)
    }
}

func TestDailyWriter_PruneDisabled(t *testing.T) {
    dir := t.TempDir()
    w := &dailyWriter{dir: dir, prefix: "gateway", retainDays: 0}
    expired := filepath.Join(dir, "gateway-2000-01-01.log")
    if err := os.WriteFile(expired, []byte("x"), 0o644); err != nil {
        t.Fatal(err)
    }
    if _, err := w.Write([]byte("today\n")); err != nil {
        t.Fatalf("write: %v", err)
    }
    defer w.Close()
    if _, err := os.Stat(expired); err != nil {
        t.Fatalf("retain_days=0 must keep everything: %v", err)
    }
}

func TestFanoutHandler_BothSinks(t *testing.T) {
    var a, b strings.Builder
    h := &fanoutHandler{hs: []slog.Handler{
        slog.NewTextHandler(&a, nil),
        slog.NewTextHandler(&b, &slog.HandlerOptions{Level: slog.LevelWarn}),
    }}
    slog.New(h).InfoContext(context.Background(), "hello")
    if !strings.Contains(a.String(), "hello") {
        t.Fatalf("first sink missing record: %q", a.String())
    }
    if b.String() != "" {
        t.Fatalf("warn-level sink must skip info record, got %q", b.String())
    }
}

func TestSetup_Disabled(t *testing.T) {
    prev := slog.Default()
    defer slog.SetDefault(prev)
    if got := Setup(config.FileLogConfig{Enabled: false}, "info"); got != nil {
        t.Fatalf("disabled config must return nil closer, got %T", got)
    }
}

func TestSetup_WritesFile(t *testing.T) {
    prev := slog.Default()
    defer slog.SetDefault(prev)
    dir := t.TempDir()
    closer := Setup(config.FileLogConfig{
        Enabled: true, Dir: dir, RetainDays: 7, Level: "debug",
    }, "info")
    if closer == nil {
        t.Fatal("enabled config must return a closer")
    }
    slog.Info("filelog-probe-message")
    closer()
    files := sortedDayFilenames(dir, "gateway")
    if len(files) != 1 {
        t.Fatalf("want 1 day file, got %v", files)
    }
    data, err := os.ReadFile(filepath.Join(dir, files[0]))
    if err != nil {
        t.Fatal(err)
    }
    if !strings.Contains(string(data), "filelog-probe-message") {
        t.Fatalf("probe message missing from file: %s", data)
    }
}
