package server

// health_cycle_test.go — #381 follow-up: the hub's /api/v1/system/health
// subcalls the gateway's mlx_url (which defaults to the gateway itself), so a
// gateway /health probe of the hub re-enters the gateway /health surface —
// an unbounded probe cycle truncated only by timeouts (hub health took
// 13-20s live). Requests carrying X-Fusion-Source: model-hub (the hub's own
// probe signature) must skip model-hub backend probes and break the cycle.

import (
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "sync/atomic"
    "testing"

    "github.com/fusion-gateway/fusion-gateway/internal/adapter"
    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

func healthCycleTestServer(t *testing.T) (*Server, *http.ServeMux, *int32) {
    t.Helper()
    var upstreamHits int32
    ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        atomic.AddInt32(&upstreamHits, 1)
        w.WriteHeader(http.StatusOK)
        _, _ = w.Write([]byte(`{"status":"healthy"}`))
    }))
    t.Cleanup(ts.Close)

    s := newTestServer()
    s.pool.Register("fusion-model-hub", adapter.NewFusionModelHubProvider(
        "fusion-model-hub",
        config.BackendConfig{Type: "fusion-model-hub", BaseURL: ts.URL, Enabled: true},
    ), config.BackendConfig{Type: "fusion-model-hub", BaseURL: ts.URL, Enabled: true})

    mux := http.NewServeMux()
    mux.HandleFunc("/health", s.handleHealth)
    return s, mux, &upstreamHits
}

// TestHealth_ModelHubProbeHitsUpstream: a plain (non-reentrant) /health
// probes the hub backend as before.
func TestHealth_ModelHubProbeHitsUpstream(t *testing.T) {
    _, mux, hits := healthCycleTestServer(t)
    rec := httptest.NewRecorder()
    mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
    if rec.Code != http.StatusOK {
        t.Fatalf("status = %d, want 200", rec.Code)
    }
    if atomic.LoadInt32(hits) == 0 {
        t.Fatal("plain /health should probe the model-hub backend")
    }
}

// TestHealth_ReentrantModelHubProbeSkipped: when the request is the hub's own
// probe (X-Fusion-Source: model-hub), the hub backend probe is skipped — the
// cycle breaks at depth 1 and the hub entry reports the skip instead of a
// hang or a timeout error.
func TestHealth_ReentrantModelHubProbeSkipped(t *testing.T) {
    _, mux, hits := healthCycleTestServer(t)

    // Warm: one plain probe so the counter is non-zero.
    mux.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/health", nil))
    before := atomic.LoadInt32(hits)

    req := httptest.NewRequest(http.MethodGet, "/health", nil)
    req.Header.Set("X-Fusion-Source", "model-hub")
    rec := httptest.NewRecorder()
    mux.ServeHTTP(rec, req)

    if rec.Code != http.StatusOK {
        t.Fatalf("status = %d, want 200", rec.Code)
    }
    if got := atomic.LoadInt32(hits); got != before {
        t.Fatalf("reentrant /health probed the hub upstream %d extra time(s), want 0", got-before)
    }

    var body struct {
        Backends map[string]map[string]interface{} `json:"backends"`
    }
    if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
        t.Fatalf("decode /health body: %v (%s)", err, rec.Body.String())
    }
    entry, ok := body.Backends["fusion-model-hub"]
    if !ok {
        t.Fatal("skipped hub backend must still appear in /health backends")
    }
    if entry["healthy"] != true {
        t.Fatalf("skipped hub entry healthy = %v, want true (skip is not a failure)", entry["healthy"])
    }
    if entry["skipped"] != "reentrant_probe" {
        t.Fatalf("skipped hub entry skipped = %v, want reentrant_probe", entry["skipped"])
    }
}

// TestStatus_ReentrantModelHubProbeSkipped: /v1/status (buildBackendStatus)
// breaks the same cycle when the caller is the hub's health probe.
func TestStatus_ReentrantModelHubProbeSkipped(t *testing.T) {
    s, mux, hits := healthCycleTestServer(t)
    mux.HandleFunc("/v1/status", func(w http.ResponseWriter, r *http.Request) {
        writeJSON(w, http.StatusOK, map[string]interface{}{
            "backends": s.buildBackendStatus(r.Context(), r.Header.Get("X-Fusion-Source") == "model-hub"),
        })
    })

    mux.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v1/status", nil))
    before := atomic.LoadInt32(hits)

    req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
    req.Header.Set("X-Fusion-Source", "model-hub")
    rec := httptest.NewRecorder()
    mux.ServeHTTP(rec, req)

    if got := atomic.LoadInt32(hits); got != before {
        t.Fatalf("reentrant /v1/status probed the hub upstream %d extra time(s), want 0", got-before)
    }
    var body struct {
        Backends map[string]map[string]interface{} `json:"backends"`
    }
    if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
        t.Fatalf("decode body: %v (%s)", err, rec.Body.String())
    }
    if entry, ok := body.Backends["fusion-model-hub"]; !ok || entry["skipped"] != "reentrant_probe" {
        t.Fatalf("hub entry = %v, want skipped=reentrant_probe", entry)
    }
}
