package filelog

// #649 regression: server setupLogging must extend the filelog fanout via
// AddSink instead of slog.SetDefault. A plain SetDefault replaced the fanout,
// so the daily JSON file went silent right after boot — request and shutdown
// lines never reached gateway-YYYY-MM-DD.log, and graceful SIGTERM shutdowns
// looked like silent process deaths in the operator-visible file sink.

import (
    "bytes"
    "encoding/json"
    "log/slog"
    "os"
    "path/filepath"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

func Test649_AddSinkForwardsToFanout(t *testing.T) {
    dir := t.TempDir()
    cfg := config.FileLogConfig{Enabled: true, Dir: dir, RetainDays: 1}
    closer := Setup(cfg, "info")
    if closer == nil {
        t.Fatal("Setup returned nil closer with file_log enabled")
    }
    defer closer()

    var buf bytes.Buffer
    if !AddSink(&buf, "info") {
        t.Fatal("AddSink returned false after Setup installed the fanout")
    }

    slog.Info("shutting down", "signal", "terminated")

    if !bytes.Contains(buf.Bytes(), []byte("shutting down")) {
        t.Fatalf("extra sink missing the record: %q", buf.String())
    }
    day := time.Now().Format("2006-01-02")
    data, err := os.ReadFile(filepath.Join(dir, "gateway-"+day+".log"))
    if err != nil {
        t.Fatalf("daily file unreadable: %v", err)
    }
    var rec map[string]any
    if err := json.Unmarshal(lastLine(data), &rec); err != nil {
        t.Fatalf("daily file line not json: %v", err)
    }
    if rec["msg"] != "shutting down" {
        t.Fatalf("file sink record msg = %v", rec["msg"])
    }
}

func Test649_AddSinkBeforeSetupIsNoop(t *testing.T) {
    // Simulate the file_log disabled path: Setup never ran, so AddSink must
    // refuse and leave the default logger alone (caller falls back to its own
    // SetDefault).
    installed.Store(false)
    current = nil
    if AddSink(&bytes.Buffer{}, "info") {
        t.Fatal("AddSink must return false when Setup never ran")
    }
}

// lastLine returns the final newline-terminated record of the daily file.
func lastLine(data []byte) []byte {
    trimmed := bytes.TrimRight(data, "\n")
    if i := bytes.LastIndexByte(trimmed, '\n'); i >= 0 {
        return trimmed[i+1:]
    }
    return trimmed
}
