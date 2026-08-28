package config

import (
    "bytes"
    "io"
    "log/slog"
    "strings"
    "testing"
)

func captureSlog(t *testing.T) *bytes.Buffer {
    t.Helper()
    buf := new(bytes.Buffer)
    slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
    return buf
}

func TestWarnSharedPortSafety_LocalQueueOffFires(t *testing.T) {
    cfg := DefaultConfig()
    cfg.Routing.Mode = "local"
    cfg.Routing.LocalPriority.QueueEnabled = false
    buf := captureSlog(t)
    WarnSharedPortSafety(&cfg)
    out := buf.String()
    if !strings.Contains(out, "local mode active without slot wait-queue") {
        t.Fatalf("B1: expected shared-port WARN in local+queue-off, got: %q", out)
    }
    if !strings.Contains(out, "queue_enabled: true") {
        t.Fatalf("B1: WARN missing opt-in guidance (queue_enabled: true), got: %q", out)
    }
}

func TestWarnSharedPortSafety_LocalQueueOnSilent(t *testing.T) {
    cfg := DefaultConfig()
    cfg.Routing.Mode = "local"
    cfg.Routing.LocalPriority.QueueEnabled = true
    buf := captureSlog(t)
    WarnSharedPortSafety(&cfg)
    if strings.Contains(buf.String(), "local mode active without slot wait-queue") {
        t.Fatalf("B1: WARN must be SILENT when queue enabled (opt-in satisfied), got: %q", buf.String())
    }
}

func TestWarnSharedPortSafety_HybridSilent(t *testing.T) {
    cfg := DefaultConfig()
    cfg.Routing.Mode = "hybrid"
    cfg.Routing.LocalPriority.QueueEnabled = false
    buf := captureSlog(t)
    WarnSharedPortSafety(&cfg)
    if strings.Contains(buf.String(), "local mode active without slot wait-queue") {
        t.Fatalf("B1: WARN must be SILENT in hybrid mode (cloud fallback exists), got: %q", buf.String())
    }
}

func TestWarnSharedPortSafety_CloudSilent(t *testing.T) {
    cfg := DefaultConfig()
    cfg.Routing.Mode = "cloud"
    buf := captureSlog(t)
    WarnSharedPortSafety(&cfg)
    if strings.Contains(buf.String(), "local mode active without slot wait-queue") {
        t.Fatalf("B1: WARN must be SILENT in cloud mode, got: %q", buf.String())
    }
}

func TestWarnSharedPortSafety_DiscardNoPanic(t *testing.T) {
    slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
    cfg := DefaultConfig()
    cfg.Routing.Mode = "local"
    WarnSharedPortSafety(&cfg)
}
