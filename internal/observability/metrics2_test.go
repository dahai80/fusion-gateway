package observability

import (
    "net/http"
    "net/http/httptest"
    "strings"
    "testing"
)

func TestRecordRequest_LocalError(t *testing.T) {
    RecordRequest("local", "model-err", "error")
    rate := SuccessRate("local")
    t.Logf("local success rate after error: %.4f", rate)
    if rate < 0 || rate > 1 {
        t.Fatalf("success rate out of range: %f", rate)
    }
}

func TestRecordRequest_CloudSuccess(t *testing.T) {
    RecordRequest("cloud", "gpt-4", "success")
    rate := SuccessRate("cloud")
    t.Logf("cloud success rate after success: %.4f", rate)
    if rate < 0 || rate > 1 {
        t.Fatalf("success rate out of range: %f", rate)
    }
}

func TestRecordRequest_OtherStatus(t *testing.T) {
    RecordRequest("local", "model-other", "timeout")
    RecordRequest("cloud", "model-other", "timeout")
}

func TestRecordRequest_CloudError(t *testing.T) {
    RecordRequest("cloud", "model-err", "error")
    rate := SuccessRate("cloud")
    t.Logf("cloud success rate after error: %.4f", rate)
    if rate < 0 || rate > 1 {
        t.Fatalf("success rate out of range: %f", rate)
    }
}

func TestSuccessRate_CloudZeroRequests(t *testing.T) {
    savedCloudS := cloudSuccesses.Load()
    savedCloudF := cloudFailures.Load()
    cloudSuccesses.Store(0)
    cloudFailures.Store(0)
    defer func() {
        cloudSuccesses.Store(savedCloudS)
        cloudFailures.Store(savedCloudF)
    }()
    rate := SuccessRate("cloud")
    if rate != 1.0 {
        t.Fatalf("expected 1.0 for zero cloud requests, got %f", rate)
    }
}

func TestStats_ReturnsValues(t *testing.T) {
    total, local, cloud := Stats()
    t.Logf("stats: total=%d local=%d cloud=%d", total, local, cloud)
    if total < 0 || local < 0 || cloud < 0 {
        t.Fatal("stats should not be negative")
    }
}

func TestRecordTokens_VerifyMetric(t *testing.T) {
    RecordTokens("input", "local", 42)
    req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
    rec := httptest.NewRecorder()
    Handler().ServeHTTP(rec, req)
    body := rec.Body.String()
    if !strings.Contains(body, "fusion_gateway_request_tokens_total") {
        t.Fatal("expected tokens metric in output")
    }
}

func TestRecordRouteDecision_VerifyMetric(t *testing.T) {
    RecordRouteDecision("cloud", "circuit_breaker")
    req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
    rec := httptest.NewRecorder()
    Handler().ServeHTTP(rec, req)
    body := rec.Body.String()
    if !strings.Contains(body, "fusion_gateway_route_decisions_total") {
        t.Fatal("expected route decisions metric in output")
    }
}

func TestUpdateHardwareMetrics_AllFields(t *testing.T) {
    UpdateHardwareMetrics(
        0.9,
        2048, 20, 10,
        0.6, 0.4, 0.3,
        4096, 8192,
        16384,
        3, 5,
    )
    req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
    rec := httptest.NewRecorder()
    Handler().ServeHTTP(rec, req)
    body := rec.Body.String()
    if !strings.Contains(body, "fusion_gateway_hw_memory_used_ratio") {
        t.Fatal("expected hw memory metric")
    }
    if !strings.Contains(body, "fusion_gateway_hw_swap_used_bytes") {
        t.Fatal("expected hw swap metric")
    }
    if !strings.Contains(body, "fusion_gateway_hw_gpu_device_utilization") {
        t.Fatal("expected hw gpu metric")
    }
    if !strings.Contains(body, "fusion_gateway_hw_mlx_active_memory_bytes") {
        t.Fatal("expected hw mlx metric")
    }
}

func TestRecordCircuitBreakerTrip_VerifyMetric(t *testing.T) {
    RecordCircuitBreakerTrip("cloud", "timeout")
    req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
    rec := httptest.NewRecorder()
    Handler().ServeHTTP(rec, req)
    body := rec.Body.String()
    if !strings.Contains(body, "fusion_gateway_circuit_breaker_trips_total") {
        t.Fatal("expected circuit breaker trips metric")
    }
}

func TestUpdateInFlight_VerifyMetric(t *testing.T) {
    UpdateInFlight("local", 7)
    UpdateInFlight("cloud", 3)
    req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
    rec := httptest.NewRecorder()
    Handler().ServeHTTP(rec, req)
    body := rec.Body.String()
    if !strings.Contains(body, "fusion_gateway_in_flight_requests") {
        t.Fatal("expected in-flight metric")
    }
}
