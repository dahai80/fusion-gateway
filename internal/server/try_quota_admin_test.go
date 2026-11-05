package server

// #514: direct handler tests for the try-quota admin endpoints (the
// withAdminOnly wrapper needs a fully wired server; the guards themselves are
// shared with every other admin surface and covered there).

import (
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "strings"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/tryquota"
)

func TestTryQuotaUsageHandlerNoMeter(t *testing.T) {
    s := &Server{}
    rec := httptest.NewRecorder()
    s.handleTryQuotaUsage(rec, httptest.NewRequest(http.MethodGet, "/admin/api/try-quota/usage", nil))
    if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "[]" {
        t.Fatalf("no meter: code=%d body=%q want 200 []", rec.Code, rec.Body.String())
    }
}

func TestTryQuotaUsageHandlerRows(t *testing.T) {
    meter := tryquota.NewStore("")
    meter.Record("u1", 42, tryquota.CallDetail{Model: "glm-5"}, time.Now())
    s := &Server{tryQuotaMeter: meter}
    rec := httptest.NewRecorder()
    s.handleTryQuotaUsage(rec, httptest.NewRequest(http.MethodGet, "/admin/api/try-quota/usage", nil))
    if rec.Code != http.StatusOK {
        t.Fatalf("code = %d", rec.Code)
    }
    var rows []tryquota.UsageRow
    if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
        t.Fatalf("unmarshal: %v (%q)", err, rec.Body.String())
    }
    if len(rows) != 1 || rows[0].Sub != "u1" || rows[0].Used != 42 {
        t.Fatalf("rows = %+v", rows)
    }
}

func TestTryQuotaUsageHandlerMethodGuard(t *testing.T) {
    s := &Server{tryQuotaMeter: tryquota.NewStore("")}
    rec := httptest.NewRecorder()
    s.handleTryQuotaUsage(rec, httptest.NewRequest(http.MethodPost, "/admin/api/try-quota/usage", nil))
    if rec.Code != http.StatusMethodNotAllowed {
        t.Fatalf("code = %d, want 405", rec.Code)
    }
}

func TestTryQuotaBudgetHandlerSetAndClear(t *testing.T) {
    meter := tryquota.NewStore("")
    s := &Server{tryQuotaMeter: meter}

    body := `{"sub":"u1","daily_budget":500}`
    rec := httptest.NewRecorder()
    s.handleTryQuotaBudget(rec, httptest.NewRequest(http.MethodPost, "/admin/api/try-quota/budget", strings.NewReader(body)))
    if rec.Code != http.StatusOK {
        t.Fatalf("set: code=%d body=%q", rec.Code, rec.Body.String())
    }
    if b, ok := meter.BudgetOverride("u1"); !ok || b != 500 {
        t.Fatalf("override = %d,%v want 500,true", b, ok)
    }

    body = `{"sub":"u1","daily_budget":0}`
    rec = httptest.NewRecorder()
    s.handleTryQuotaBudget(rec, httptest.NewRequest(http.MethodPost, "/admin/api/try-quota/budget", strings.NewReader(body)))
    if rec.Code != http.StatusOK {
        t.Fatalf("clear: code=%d", rec.Code)
    }
    if _, ok := meter.BudgetOverride("u1"); ok {
        t.Fatalf("override not cleared")
    }
}

func TestTryQuotaBudgetHandlerValidation(t *testing.T) {
    s := &Server{tryQuotaMeter: tryquota.NewStore("")}
    // Empty sub rejected.
    rec := httptest.NewRecorder()
    s.handleTryQuotaBudget(rec, httptest.NewRequest(http.MethodPost, "/admin/api/try-quota/budget", strings.NewReader(`{"daily_budget":5}`)))
    if rec.Code != http.StatusBadRequest {
        t.Fatalf("empty sub: code=%d want 400", rec.Code)
    }
    // Broken body rejected.
    rec = httptest.NewRecorder()
    s.handleTryQuotaBudget(rec, httptest.NewRequest(http.MethodPost, "/admin/api/try-quota/budget", strings.NewReader("nope")))
    if rec.Code != http.StatusBadRequest {
        t.Fatalf("bad body: code=%d want 400", rec.Code)
    }
}

func TestTryQuotaBudgetHandlerNoMeter503(t *testing.T) {
    s := &Server{}
    rec := httptest.NewRecorder()
    s.handleTryQuotaBudget(rec, httptest.NewRequest(http.MethodPost, "/admin/api/try-quota/budget", strings.NewReader(`{"sub":"u1","daily_budget":5}`)))
    if rec.Code != http.StatusServiceUnavailable {
        t.Fatalf("code = %d, want 503", rec.Code)
    }
}
