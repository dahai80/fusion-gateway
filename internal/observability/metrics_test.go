package observability

import (
    "net/http"
    "net/http/httptest"
    "strings"
    "testing"
)

func TestHandler_ReturnsMetrics(t *testing.T) {
    req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
    rec := httptest.NewRecorder()
    Handler().ServeHTTP(rec, req)
    if rec.Code != http.StatusOK {
        t.Fatalf("expected 200, got %d", rec.Code)
    }
    t.Logf("metrics body length: %d", rec.Body.Len())
}

func TestRecordRequest_Local(t *testing.T) {
    beforeTotal, beforeLocal, _ := Stats()
    RecordRequest("local", "test-model", "success")
    total, local, _ := Stats()
    if total != beforeTotal+1 {
        t.Fatalf("expected total=%d, got %d", beforeTotal+1, total)
    }
    if local != beforeLocal+1 {
        t.Fatalf("expected local=%d, got %d", beforeLocal+1, local)
    }
}

func TestRecordRequest_Cloud(t *testing.T) {
    RecordRequest("cloud", "gpt-4", "error")
    total, _, cloud := Stats()
    if total < 1 {
        t.Fatalf("expected total>=1, got %d", total)
    }
    if cloud < 1 {
        t.Fatalf("expected cloud>=1, got %d", cloud)
    }
}

func TestSuccessRate_Local(t *testing.T) {
    rate := SuccessRate("local")
    if rate < 0 || rate > 1 {
        t.Fatalf("success rate out of range: %f", rate)
    }
    t.Logf("local success rate: %.4f", rate)
}

func TestSuccessRate_Cloud(t *testing.T) {
    rate := SuccessRate("cloud")
    if rate < 0 || rate > 1 {
        t.Fatalf("success rate out of range: %f", rate)
    }
    t.Logf("cloud success rate: %.4f", rate)
}

func TestSuccessRate_NoRequests(t *testing.T) {
    rate := SuccessRate("local")
    if rate < 0 || rate > 1 {
        t.Fatalf("success rate out of range: %f", rate)
    }
    t.Logf("local success rate: %.4f", rate)
}

func TestRecordDuration(t *testing.T) {
    RecordDuration("local", "test-model", 0.5)
    req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
    rec := httptest.NewRecorder()
    Handler().ServeHTTP(rec, req)
    body := rec.Body.String()
    if !strings.Contains(body, "fusion_gateway_request_duration_seconds") {
        t.Fatal("expected duration metric in output")
    }
}

func TestRecordTokens(t *testing.T) {
    RecordTokens("input", "local", 100)
    RecordTokens("output", "cloud", 50)
}

func TestRecordRouteDecision(t *testing.T) {
    RecordRouteDecision("local", "token_budget")
    RecordRouteDecision("cloud", "fallback")
}

func TestRecordCircuitBreakerTrip(t *testing.T) {
    RecordCircuitBreakerTrip("local", "memory_overload")
}

func TestUpdateHardwareMetrics(t *testing.T) {
    UpdateHardwareMetrics(
        0.75,
        1024, 10, 5,
        0.5, 0.3, 0.2,
        2048, 4096,
        8192,
        2, 3,
    )
}

func TestRecordCollectionError(t *testing.T) {
    RecordCollectionError("iokit")
    RecordCollectionError("gopsutil")
}

func TestUpdateConfigVersion(t *testing.T) {
    UpdateConfigVersion(42)
}

func TestUpdateInFlight(t *testing.T) {
    UpdateInFlight("local", 5)
    UpdateInFlight("cloud", 10)
}
