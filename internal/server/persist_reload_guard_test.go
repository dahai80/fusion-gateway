package server

import (
    "bytes"
    "log/slog"
    "strings"
    "testing"
)

// #344: hot-reload can flip persistence.enabled on, but the sink is built
// once at startup — the request path must log ONE loud ERROR (not per
// request, not silently skip).
func TestWarnPersistNoSinkOnce(t *testing.T) {
    var buf bytes.Buffer
    oldDefault := slog.Default()
    slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
    defer slog.SetDefault(oldDefault)

    s := newTestServer()
    if s.persistSink != nil {
        t.Fatalf("test server should have no persist sink")
    }
    s.warnPersistNoSinkOnce()
    s.warnPersistNoSinkOnce()

    out := buf.String()
    if !strings.Contains(out, "no persist sink") {
        t.Errorf("expected loud ERROR about missing persist sink, got: %s", out)
    }
    if got := strings.Count(out, "no persist sink"); got != 1 {
        t.Errorf("expected exactly 1 warn per process, got %d", got)
    }
    if !strings.Contains(out, "restart the gateway") {
        t.Errorf("warn should tell the operator to restart, got: %s", out)
    }
}
