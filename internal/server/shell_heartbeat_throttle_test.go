package server

import (
    "bytes"
    "encoding/json"
    "log/slog"
    "net/http"
    "net/http/httptest"
    "strconv"
    "strings"
    "testing"

    "github.com/fusion-gateway/fusion-gateway/internal/observability"
    "github.com/fusion-gateway/fusion-gateway/internal/shell"
)

// TestShellHeartbeatDenial_LogThrottledMetricNot (#281): a module hammering
// the heartbeat endpoint with a bad credential must produce ONE WARN per
// 5-minute window (not one per 15s, which flooded the log at 1944/day),
// while fusion_gateway_shell_heartbeat_denied_total still counts every
// denial so the condition stays observable in /metrics.
func TestShellHeartbeatDenial_LogThrottledMetricNot(t *testing.T) {
    s := newShellTestServer(t, false)
    s.cfg.Config.Auth.MasterKey = "mk"
    s.cfg.Config.Shell.AdminKey = "ak"

    var buf bytes.Buffer
    prev := slog.Default()
    slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
    defer slog.SetDefault(prev)

    const moduleID = "fusion-flood-281"
    beat := func(token string) int {
        r := httptest.NewRequest(http.MethodPost, "/api/v1/shell/manifests/"+moduleID+"/heartbeat", nil)
        r.SetPathValue("id", moduleID)
        if token != "" {
            r.Header.Set("Authorization", "Bearer "+token)
        }
        ww := httptest.NewRecorder()
        s.withShellHeartbeatAuth(s.handleShellManifestHeartbeat)(ww, r)
        return ww.Code
    }

    // Snapshot the counter baseline, then fire 12 denials (a 3-minute flood
    // at the observed 15s cadence).
    metricsBefore := scrapeHeartbeatDeniedMetrics(t)
    for i := 0; i < 6; i++ {
        if code := beat("fm-" + moduleID + "-wrongkeywrongkeywrongkeyw"); code != http.StatusForbidden {
            t.Fatalf("invalid-credential beat %d code = %d, want 403", i, code)
        }
        if code := beat(""); code != http.StatusUnauthorized {
            t.Fatalf("missing-credential beat %d code = %d, want 401", i, code)
        }
    }

    logged := strings.Count(buf.String(), "shell heartbeat denied")
    if logged != 2 {
        t.Fatalf("expected exactly 2 throttled WARN lines (1 invalid + 1 missing), got %d", logged)
    }

    metricsAfter := scrapeHeartbeatDeniedMetrics(t)
    const invalidKey = `module_id="` + moduleID + `",reason="invalid"`
    const missingKey = `module_id="` + moduleID + `",reason="missing"`
    if d := metricsAfter[invalidKey] - metricsBefore[invalidKey]; d != 6 {
        t.Fatalf("invalid-credential metric delta = %v, want 6 (metric must NOT be throttled)", d)
    }
    if d := metricsAfter[missingKey] - metricsBefore[missingKey]; d != 6 {
        t.Fatalf("missing-credential metric delta = %v, want 6 (metric must NOT be throttled)", d)
    }
}

// TestShellHeartbeatDenial_MissingCredentialEnvelope: the throttled log does
// not change the wire behavior — missing credential stays 401 with the
// heartbeat_unauthorized envelope.
func TestShellHeartbeatDenial_MissingCredentialEnvelope(t *testing.T) {
    s := newShellTestServer(t, false)
    s.cfg.Config.Auth.MasterKey = "mk"
    r := httptest.NewRequest(http.MethodPost, "/api/v1/shell/manifests/fusion-x/heartbeat", nil)
    r.SetPathValue("id", "fusion-x")
    ww := httptest.NewRecorder()
    s.withShellHeartbeatAuth(s.handleShellManifestHeartbeat)(ww, r)
    if ww.Code != http.StatusUnauthorized {
        t.Fatalf("code = %d, want 401", ww.Code)
    }
    var env shell.Envelope
    if err := json.Unmarshal(ww.Body.Bytes(), &env); err != nil {
        t.Fatalf("envelope decode: %v body=%s", err, ww.Body.String())
    }
    if env.Code != "heartbeat_unauthorized" {
        t.Fatalf("envelope code = %q, want heartbeat_unauthorized", env.Code)
    }
}

// scrapeHeartbeatDeniedMetrics scrapes /metrics and returns a map from the
// heartbeat-denied sample's label block (module_id+reason) to its value, so
// tests can diff counters across calls without depending on other samples.
func scrapeHeartbeatDeniedMetrics(t *testing.T) map[string]float64 {
    t.Helper()
    req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
    rec := httptest.NewRecorder()
    observability.Handler().ServeHTTP(rec, req)
    out := make(map[string]float64)
    for _, line := range strings.Split(rec.Body.String(), "\n") {
        if !strings.HasPrefix(line, "fusion_gateway_shell_heartbeat_denied_total{") {
            continue
        }
        space := strings.LastIndexByte(line, ' ')
        if space < 0 {
            continue
        }
        value, err := strconv.ParseFloat(line[space+1:], 64)
        if err != nil {
            continue
        }
        labels := strings.TrimSuffix(strings.TrimPrefix(line[:space], "fusion_gateway_shell_heartbeat_denied_total{"), "}")
        out[labels] = value
    }
    return out
}
