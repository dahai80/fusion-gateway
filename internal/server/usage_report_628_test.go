package server

// #628: /v1/usage/report per-call ledger endpoint + /v1/cost access scoping.
// A non-privileged key must not read another key's usage (?key=<other> →
// 403); without ?key= it gets its OWN summary, not the global one. Only the
// master-key principal sees arbitrary keys (admin-plane check: the site BFF
// uses a dedicated low-privilege key and must see only what it owns).

import (
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "net/url"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/cost"
    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/middleware"
)

func srv628(t *testing.T) *Server {
    t.Helper()
    s := newTestServer()
    s.costTracker = cost.NewTracker(100)
    s.costTracker.RecordAndReturn("key-a", "local", "m1", 100, 50)
    s.costTracker.RecordAndReturn("key-b", "local", "m1", 10, 5)
    return s
}

func call628(s *Server, path string, p *middleware.Principal) *httptest.ResponseRecorder {
    req := httptest.NewRequest(http.MethodGet, path, nil)
    if p != nil {
        req = req.WithContext(middleware.ContextWithPrincipal(req.Context(), p))
    }
    rec := httptest.NewRecorder()
    s.handleCost(rec, req)
    return rec
}

func keyPrincipal628(name string) *middleware.Principal {
    return &middleware.Principal{KeyConfig: &config.AuthKeyConfig{Name: name}}
}

func Test628_Cost_NonPrivilegedKey_CannotReadOtherKey(t *testing.T) {
    s := srv628(t)
    rec := call628(s, "/v1/cost?key=key-b", keyPrincipal628("key-a"))
    if rec.Code != http.StatusForbidden {
        t.Fatalf("cross-key ?key= = %d body=%s, want 403", rec.Code, rec.Body.String())
    }
}

func Test628_Cost_OwnKey_SummaryScoped(t *testing.T) {
    s := srv628(t)
    rec := call628(s, "/v1/cost?key=key-a", keyPrincipal628("key-a"))
    if rec.Code != http.StatusOK {
        t.Fatalf("own-key ?key= = %d, want 200", rec.Code)
    }
    var sum cost.CostSummary
    _ = json.Unmarshal(rec.Body.Bytes(), &sum)
    if sum.TotalRequests != 1 {
        t.Fatalf("own summary requests = %d, want 1 (only key-a)", sum.TotalRequests)
    }
}

func Test628_Cost_NoParam_NonPrivileged_SeesOwnKeyOnly(t *testing.T) {
    s := srv628(t)
    rec := call628(s, "/v1/cost", keyPrincipal628("key-a"))
    if rec.Code != http.StatusOK {
        t.Fatalf("no-param = %d, want 200", rec.Code)
    }
    var sum cost.CostSummary
    _ = json.Unmarshal(rec.Body.Bytes(), &sum)
    if sum.TotalRequests != 1 {
        t.Fatalf("no-param non-privileged saw %d requests, want 1 (own key only)", sum.TotalRequests)
    }
}

func Test628_Cost_MasterKey_ReadsAnyKey(t *testing.T) {
    s := srv628(t)
    rec := call628(s, "/v1/cost?key=key-b", &middleware.Principal{IsMaster: true})
    if rec.Code != http.StatusOK {
        t.Fatalf("master cross-key = %d, want 200", rec.Code)
    }
    var sum cost.CostSummary
    _ = json.Unmarshal(rec.Body.Bytes(), &sum)
    if sum.TotalRequests != 1 {
        t.Fatalf("master key-b summary = %d requests, want 1", sum.TotalRequests)
    }
    rec = call628(s, "/v1/cost", &middleware.Principal{IsMaster: true})
    if rec.Code != http.StatusOK {
        t.Fatalf("master no-param = %d, want 200", rec.Code)
    }
    var full cost.CostSummary
    _ = json.Unmarshal(rec.Body.Bytes(), &full)
    if full.TotalRequests != 2 {
        t.Fatalf("master global summary = %d requests, want 2 (all keys)", full.TotalRequests)
    }
}

func Test628_UsageReport_LedgerAndWindow(t *testing.T) {
    s := srv628(t)
    since := url.QueryEscape(time.Now().Add(-time.Minute).Format(time.RFC3339))
    until := url.QueryEscape(time.Now().Add(time.Minute).Format(time.RFC3339))
    req := httptest.NewRequest(http.MethodGet, "/v1/usage/report?key=key-a&since="+since+"&until="+until+"&limit=10", nil)
    req = req.WithContext(middleware.ContextWithPrincipal(req.Context(), keyPrincipal628("key-a")))
    rec := httptest.NewRecorder()
    s.handleUsageReport(rec, req)

    if rec.Code != http.StatusOK {
        t.Fatalf("report = %d body=%s, want 200", rec.Code, rec.Body.String())
    }
    var resp struct {
        Records []cost.UsageRecord `json:"records"`
        Total   int                `json:"total"`
        Source  string             `json:"source"`
        Summary *cost.CostSummary  `json:"summary"`
    }
    if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
        t.Fatalf("bad json: %v body=%s", err, rec.Body.String())
    }
    if resp.Total != 1 || len(resp.Records) != 1 {
        t.Fatalf("key-a ledger = %d records / total %d, want 1/1", len(resp.Records), resp.Total)
    }
    if resp.Records[0].KeyName != "key-a" {
        t.Fatalf("record key = %s, want key-a", resp.Records[0].KeyName)
    }
    if resp.Source != "ring" {
        t.Fatalf("source = %s, want ring", resp.Source)
    }
    if resp.Summary == nil || resp.Summary.TotalRequests != 1 {
        t.Fatalf("window summary must reconcile with ledger, got %+v", resp.Summary)
    }
}

func Test628_UsageReport_NonPrivileged_DefaultsToOwnKey(t *testing.T) {
    s := srv628(t)
    req := httptest.NewRequest(http.MethodGet, "/v1/usage/report", nil)
    req = req.WithContext(middleware.ContextWithPrincipal(req.Context(), keyPrincipal628("key-a")))
    rec := httptest.NewRecorder()
    s.handleUsageReport(rec, req)
    if rec.Code != http.StatusOK {
        t.Fatalf("report = %d, want 200", rec.Code)
    }
    var resp struct {
        Total int `json:"total"`
    }
    _ = json.Unmarshal(rec.Body.Bytes(), &resp)
    if resp.Total != 1 {
        t.Fatalf("default-scope total = %d, want 1 (own key only)", resp.Total)
    }
}

func Test628_UsageReport_CrossKey_403(t *testing.T) {
    s := srv628(t)
    req := httptest.NewRequest(http.MethodGet, "/v1/usage/report?key=key-b", nil)
    req = req.WithContext(middleware.ContextWithPrincipal(req.Context(), keyPrincipal628("key-a")))
    rec := httptest.NewRecorder()
    s.handleUsageReport(rec, req)
    if rec.Code != http.StatusForbidden {
        t.Fatalf("cross-key report = %d, want 403", rec.Code)
    }
}
