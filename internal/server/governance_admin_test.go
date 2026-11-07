package server

// Governance report handler tests (LiteLLM-gap dashboard wave): the handler
// must degrade to empty PG sections when the insights read layer is absent
// and surface try-quota budget rows (incl. soft_alerted) when the meter is
// on. Handler-level tests — withAdminOnly auth is covered by the shared
// middleware suite.

import (
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/tryquota"
)

func TestGovernanceReportNoMeterNoInsights(t *testing.T) {
    s := newTestServer()
    req := httptest.NewRequest(http.MethodGet, "/admin/api/reports/governance", nil)
    rec := httptest.NewRecorder()
    s.handleGovernanceReport(rec, req)
    if rec.Code != http.StatusOK {
        t.Fatalf("empty system must still report, got %d", rec.Code)
    }
    var body struct {
        Budgets      []tryquota.UsageRow `json:"budgets"`
        PIIDaily     []map[string]any    `json:"pii_daily"`
        RejectsByKey []map[string]any    `json:"rejects_by_key"`
        Events       []map[string]any    `json:"events"`
    }
    if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
        t.Fatalf("decode: %v", err)
    }
    if len(body.Budgets) != 0 {
        t.Fatalf("no meter means no budget rows, got %d", len(body.Budgets))
    }
}

func TestGovernanceReportBudgetRows(t *testing.T) {
    s := newTestServer()
    s.tryQuotaMeter = tryquota.NewStore("")
    now := time.Now()
    s.tryQuotaMeter.Record("gov-user", 700, tryquota.CallDetail{}, now)
    s.tryQuotaMeter.SetBudgetOverride("gov-user", 1000)
    s.tryQuotaMeter.MarkSoftAlerted("gov-user", now)

    req := httptest.NewRequest(http.MethodGet, "/admin/api/reports/governance", nil)
    rec := httptest.NewRecorder()
    s.handleGovernanceReport(rec, req)
    if rec.Code != http.StatusOK {
        t.Fatalf("got %d", rec.Code)
    }
    var body struct {
        Budgets []struct {
            Sub         string `json:"sub"`
            Used        int    `json:"used"`
            Budget      int    `json:"budget"`
            SoftAlerted bool   `json:"soft_alerted"`
        } `json:"budgets"`
        GeneratedAt string `json:"generated_at"`
    }
    if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
        t.Fatalf("decode: %v", err)
    }
    if body.GeneratedAt == "" {
        t.Fatal("generated_at required")
    }
    if len(body.Budgets) != 1 {
        t.Fatalf("one budget row expected, got %d", len(body.Budgets))
    }
    row := body.Budgets[0]
    if row.Sub != "gov-user" || row.Used != 700 || row.Budget != 1000 {
        t.Fatalf("unexpected row %+v", row)
    }
    if !row.SoftAlerted {
        t.Fatal("soft_alerted must surface in the report")
    }
}

func TestGovernanceReportMethodGuard(t *testing.T) {
    s := newTestServer()
    req := httptest.NewRequest(http.MethodPost, "/admin/api/reports/governance", nil)
    rec := httptest.NewRecorder()
    s.handleGovernanceReport(rec, req)
    if rec.Code != http.StatusMethodNotAllowed {
        t.Fatalf("POST must 405, got %d", rec.Code)
    }
}
