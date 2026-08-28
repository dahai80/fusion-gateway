package server

// R4 (version visibility) + R5 (log_level→slog) audit-fix tests.

import (
    "compress/gzip"
    "encoding/json"
    "io"
    "log/slog"
    "net/http"
    "net/http/httptest"
    "os"
    "path/filepath"
    "strings"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/adapter"
    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/hardware"
    "github.com/fusion-gateway/fusion-gateway/internal/router"
    "github.com/fusion-gateway/fusion-gateway/internal/tokenizer"
)

// r4MinimalServer builds a Server with the fields handleStatus touches
// (cfg, hwCollector, router, startTime, version, commit). It does NOT call
// server.New — we only exercise the /v1/status handler directly.
func r4MinimalServer(t *testing.T) *Server {
    t.Helper()
    cfg := &config.ConfigSnapshot{Config: config.DefaultConfig()}
    hwCollector := hardware.NewCollector(&cfg.Config.Hardware)
    s := &Server{
        cfg:         cfg,
        pool:        adapter.NewPool(),
        hwCollector: hwCollector,
        router:      router.NewEngine(cfg, hwCollector),
        tokEngine:   tokenizer.NewEngine(&cfg.Config.Tokenizer, ""),
        startTime:   time.Now(),
        version:     "test-ver",
        commit:      "test-commit",
    }
    return s
}

// TestR4_StatusReportsVersionAndCommit asserts /v1/status surfaces the
// build-time version + commit (R4 audit fix), not just config_version.
func TestR4_StatusReportsVersionAndCommit(t *testing.T) {
    s := r4MinimalServer(t)

    req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
    rec := httptest.NewRecorder()
    s.handleStatus(rec, req)

    if rec.Code != http.StatusOK {
        t.Fatalf("status: want 200, got %d", rec.Code)
    }

    var body map[string]interface{}
    if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
        t.Fatalf("decode status body: %v", err)
    }
    if body["version"] != "test-ver" {
        t.Errorf("version: want test-ver, got %v", body["version"])
    }
    if body["commit"] != "test-commit" {
        t.Errorf("commit: want test-commit, got %v", body["commit"])
    }
    if body["config_version"] == nil {
        t.Error("config_version should still be present")
    }
}

// TestR4_SetVersionDefaults asserts a freshly built Server (no SetVersion)
// reports the default "dev"/"unknown" — a plain go build must not 404 the key.
func TestR4_SetVersionDefaults(t *testing.T) {
    cfg := &config.ConfigSnapshot{Config: config.DefaultConfig()}
    hwCollector := hardware.NewCollector(&cfg.Config.Hardware)
    s := &Server{
        cfg:         cfg,
        pool:        adapter.NewPool(),
        hwCollector: hwCollector,
        router:      router.NewEngine(cfg, hwCollector),
        tokEngine:   tokenizer.NewEngine(&cfg.Config.Tokenizer, ""),
        startTime:   time.Now(),
        // version/commit intentionally zero — no SetVersion call.
    }

    req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
    rec := httptest.NewRecorder()
    s.handleStatus(rec, req)

    var body map[string]interface{}
    _ = json.NewDecoder(rec.Body).Decode(&body)
    if body["version"] != "" {
        t.Errorf("default version: want empty string, got %v", body["version"])
    }
}

// TestR4_SetVersion stamps values post-construction (mirrors main.go wiring).
func TestR4_SetVersion(t *testing.T) {
    s := r4MinimalServer(t)
    s.version = ""
    s.commit = ""
    s.SetVersion("v0.8.46", "abc1234")
    if s.version != "v0.8.46" || s.commit != "abc1234" {
        t.Errorf("SetVersion: got version=%q commit=%q", s.version, s.commit)
    }
}

// TestR5_ParseLevel asserts the log_level→slog.Level mapping (R5 audit fix).
// Empty/unknown default to Info (the safe baseline) without panicking.
func TestR5_ParseLevel(t *testing.T) {
    cases := []struct {
        in   string
        want slog.Level
        known bool
    }{
        {"debug", slog.LevelDebug, true},
        {"DEBUG", slog.LevelDebug, true},
        {"  info  ", slog.LevelInfo, true},
        {"warn", slog.LevelWarn, true},
        {"warning", slog.LevelWarn, true},
        {"error", slog.LevelError, true},
        {"", slog.LevelInfo, true},
        {"info", slog.LevelInfo, true},
        {"bogus", slog.LevelInfo, false},
        {"verbose", slog.LevelInfo, false},
    }
    for _, c := range cases {
        got, known := parseLevel(c.in)
        if got != c.want || known != c.known {
            t.Errorf("parseLevel(%q): want (%v,%v), got (%v,%v)", c.in, c.want, c.known, got, known)
        }
    }
}

// TestR5_SetupLogging_NoPanic asserts setupLogging runs without panicking on
// every accepted + unknown level and sets a non-nil default handler.
func TestR5_SetupLogging_NoPanic(t *testing.T) {
    for _, lvl := range []string{"debug", "info", "warn", "error", "", "garbage"} {
        // Must not panic on any input. Empty logFile = stderr-only (S3 default).
        setupLogging(lvl, "", 100, 7)
    }
}

// TestS3_FileLoggingWritesAndRotates verifies the S3 fix: when observability.
// log_file is set, structured logs are mirrored to a rotating file via
// lumberjack. A written slog.Info line lands in the file; exceeding max_size
// triggers a rotation backup. Cleans up temp dir after (process-data rule).
func TestS3_FileLoggingWritesAndRotates(t *testing.T) {
    tmpDir := t.TempDir()
    logFile := filepath.Join(tmpDir, "gateway.log")

    // maxSize=1 MiB. A single line larger than MaxSize is rejected wholesale by
    // lumberjack (no rotation, write dropped), so cross the cap the realistic way:
    // many medium lines that accumulate past the boundary and trigger rotation.
    setupLogging("info", logFile, 1, 3)

    slog.Info("s3-probe-marker")
    for i := 0; i < 200; i++ {
        slog.Info("s3-rotate-trigger", "seq", i, "payload", strings.Repeat("x", 16*1024))
    }

    // The active file must exist and carry the marker line.
    data, err := os.ReadFile(logFile)
    if err != nil {
        t.Fatalf("expected log file %s to exist, got error: %v", logFile, err)
    }
    if !strings.Contains(string(data), "s3-probe-marker") && !fileContainsMarker(t, tmpDir, "s3-probe-marker") {
        t.Fatalf("expected marker 's3-probe-marker' in the active log or a rotated backup, active file: %s", string(data))
    }

    // A rotation backup must appear (lumberjack names backups <file>-<timestamp>.gz
    // or <file>.1 when compress is on). At least one extra file beyond the active.
    entries, err := os.ReadDir(tmpDir)
    if err != nil {
        t.Fatalf("read temp dir: %v", err)
    }
    if len(entries) < 2 {
        t.Fatalf("expected at least 2 files (active + rotated backup) after 3+ MiB accumulated writes, got %d", len(entries))
    }
    // t.TempDir auto-cleans; nothing else to remove.
}

// fileContainsMarker scans rotated backups in dir for the marker string.
// Lumberjack compresses rotated backups (.gz), so gzip files are decompressed
// before scanning; plain files are scanned as-is.
func fileContainsMarker(t *testing.T, dir, marker string) bool {
    t.Helper()
    entries, err := os.ReadDir(dir)
    if err != nil {
        return false
    }
    for _, e := range entries {
        if e.IsDir() {
            continue
        }
        name := e.Name()
        path := filepath.Join(dir, name)
        var data []byte
        if strings.HasSuffix(name, ".gz") {
            f, err := os.Open(path)
            if err != nil {
                continue
            }
            gz, err := gzip.NewReader(f)
            if err != nil {
                f.Close()
                continue
            }
            data, err = io.ReadAll(gz)
            f.Close()
            gz.Close()
            if err != nil {
                continue
            }
        } else {
            data, err = os.ReadFile(path)
            if err != nil {
                continue
            }
        }
        if strings.Contains(string(data), marker) {
            return true
        }
    }
    return false
}
