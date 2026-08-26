package observability

import (
    "net/http"
    "net/http/httptest"
    "strings"
    "sync"
    "testing"
)

// TestEI6_TokenizerDeviationMetricRemoved guards that the dead
// tokenizer_calibration_deviation gauge is no longer declared. On the BUG
// (gauge registered but never written) the metric name appears in /metrics
// exposing a permanent 0 — a fake-healthy signal. On the FIX the name is
// absent. The guard direction: FAIL on the bug (metric present), PASS on the
// fix (metric absent).
func TestEI6_TokenizerDeviationMetricRemoved(t *testing.T) {
    req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
    rec := httptest.NewRecorder()
    Handler().ServeHTTP(rec, req)
    body := rec.Body.String()
    if strings.Contains(body, "fusion_gateway_tokenizer_calibration_deviation") {
        t.Fatal("EI6: dead metric tokenizer_calibration_deviation must not be registered — it had no write path")
    }
}

// TestEI6_CloudInFlightWired guards that the in_flight_requests cloud label is
// a live signal driven by IncrCloudInFlight/DecrCloudInFlight. On the BUG the
// cloud label was never written so CloudInFlight() stays 0 regardless of inc.
// On the FIX IncrCloudInFlight raises the counter and DecrCloudInFlight lowers
// it (floored at 0).
func TestEI6_CloudInFlightWired(t *testing.T) {
    cloudInFlight.Store(0)
    if got := CloudInFlight(); got != 0 {
        t.Fatalf("EI6: expected cloud in-flight 0 at baseline, got %d", got)
    }
    IncrCloudInFlight()
    IncrCloudInFlight()
    if got := CloudInFlight(); got != 2 {
        t.Fatalf("EI6: expected cloud in-flight 2 after 2 inc, got %d", got)
    }
    DecrCloudInFlight()
    if got := CloudInFlight(); got != 1 {
        t.Fatalf("EI6: expected cloud in-flight 1 after 1 dec, got %d", got)
    }
    DecrCloudInFlight()
    if got := CloudInFlight(); got != 0 {
        t.Fatalf("EI6: expected cloud in-flight 0 after balanced dec, got %d", got)
    }
}

// TestEI6_CloudInFlightFloor guards DecrCloudInFlight never goes negative — a
// stray extra dec (e.g. drain goroutine racing an error-path dec) must floor
// at 0, not underflow, so /metrics never reports a negative in-flight count.
func TestEI6_CloudInFlightFloor(t *testing.T) {
    cloudInFlight.Store(0)
    DecrCloudInFlight()
    DecrCloudInFlight()
    if got := CloudInFlight(); got != 0 {
        t.Fatalf("EI6: dec below zero must floor at 0, got %d", got)
    }
    cloudInFlight.Store(0)
}

// TestEI6_CloudInFlightConcurrent guards the atomic counter is race-free under
// concurrent inc/dec — the whole reason it is atomic.Int64 not a bare int.
func TestEI6_CloudInFlightConcurrent(t *testing.T) {
    cloudInFlight.Store(0)
    var wg sync.WaitGroup
    for i := 0; i < 50; i++ {
        wg.Add(2)
        go func() {
            defer wg.Done()
            IncrCloudInFlight()
        }()
        go func() {
            defer wg.Done()
            DecrCloudInFlight()
        }()
    }
    wg.Wait()
    if got := CloudInFlight(); got < 0 {
        t.Fatalf("EI6: concurrent inc/dec produced negative in-flight: %d", got)
    }
    cloudInFlight.Store(0)
}

// TestEI6_CloudLabelPresentInMetrics guards the cloud label series still
// EXISTS in /metrics (registered) — EI6 wires it live, it does not remove it.
// A regression that unregisters the cloud label would make this fail.
func TestEI6_CloudLabelPresentInMetrics(t *testing.T) {
    cloudInFlight.Store(0)
    IncrCloudInFlight()
    defer func() { cloudInFlight.Store(0) }()
    req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
    rec := httptest.NewRecorder()
    Handler().ServeHTTP(rec, req)
    body := rec.Body.String()
    if !strings.Contains(body, `fusion_gateway_in_flight_requests{backend="cloud"}`) {
        t.Fatal("EI6: in_flight_requests cloud label must be present and live in /metrics")
    }
}
