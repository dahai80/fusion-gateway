package middleware

import (
    "context"
    "errors"
    "log/slog"
    "net/http"
    "net/http/httptest"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/store"
)

type mockBudgetStore struct {
    used     float64
    limit    float64
    exceeded bool
    err      error
}

func (m *mockBudgetStore) AppendLog(log *store.RequestLog) error                    { return nil }
func (m *mockBudgetStore) QueryLogs(filter store.LogFilter) ([]*store.RequestLog, int, error) {
    return nil, 0, nil
}
func (m *mockBudgetStore) GetLog(id string) (*store.RequestLog, error)              { return nil, nil }
func (m *mockBudgetStore) ExportLogs(filter store.LogFilter, format string) ([]byte, error) {
    return nil, nil
}
func (m *mockBudgetStore) ListKeys() ([]*store.APIKeyEntry, error)                  { return nil, nil }
func (m *mockBudgetStore) GetKey(name string) (*store.APIKeyEntry, error)           { return nil, nil }
func (m *mockBudgetStore) GetKeyByHash(hash string) (*store.APIKeyEntry, error)     { return nil, nil }
func (m *mockBudgetStore) CreateKey(key *store.APIKeyEntry) error                   { return nil }
func (m *mockBudgetStore) UpdateKey(key *store.APIKeyEntry) error                   { return nil }
func (m *mockBudgetStore) DeleteKey(name string) error                              { return nil }
func (m *mockBudgetStore) ListChannels() ([]*store.ChannelEntry, error)             { return nil, nil }
func (m *mockBudgetStore) GetChannel(name string) (*store.ChannelEntry, error)      { return nil, nil }
func (m *mockBudgetStore) CreateChannel(ch *store.ChannelEntry) error               { return nil }
func (m *mockBudgetStore) UpdateChannel(ch *store.ChannelEntry) error               { return nil }
func (m *mockBudgetStore) DeleteChannel(name string) error                          { return nil }
func (m *mockBudgetStore) GetTokenStats(from, to time.Time, groupBy string) ([]*store.TokenStat, error) {
    return nil, nil
}
func (m *mockBudgetStore) GetCostStats(from, to time.Time, groupBy string) ([]*store.CostStat, error) {
    return nil, nil
}
func (m *mockBudgetStore) GetModelStats(from, to time.Time) ([]*store.ModelStat, error) {
    return nil, nil
}
func (m *mockBudgetStore) GetLatencyStats(from, to time.Time) ([]*store.LatencyStat, error) {
    return nil, nil
}
func (m *mockBudgetStore) GetErrorStats(from, to time.Time) ([]*store.ErrorStat, error) {
    return nil, nil
}
func (m *mockBudgetStore) GetDashboardOverview() (*store.DashboardOverview, error)   { return nil, nil }
func (m *mockBudgetStore) GetKeyProfitStats(from, to time.Time) ([]*store.KeyProfitStat, error) {
    return nil, nil
}
func (m *mockBudgetStore) CheckQuota(keyName string) (used, limit float64, exceeded bool, err error) {
    return m.used, m.limit, m.exceeded, m.err
}
func (m *mockBudgetStore) DeductQuota(keyName string, amount float64) error          { return nil }
func (m *mockBudgetStore) CreateTeam(team *store.Team) error                        { return nil }
func (m *mockBudgetStore) GetTeam(id string) (*store.Team, error)                   { return nil, nil }
func (m *mockBudgetStore) ListTeams() ([]*store.Team, error)                        { return nil, nil }
func (m *mockBudgetStore) UpdateTeam(team *store.Team) error                        { return nil }
func (m *mockBudgetStore) DeleteTeam(id string) error                               { return nil }
func (m *mockBudgetStore) BindKeyToTeam(apiKey, teamID string) error                { return nil }
func (m *mockBudgetStore) GetTeamByKey(apiKey string) (*store.Team, error)          { return nil, nil }
func (m *mockBudgetStore) AddTeamCost(teamID string, cost float64) error            { return nil }
func (m *mockBudgetStore) CheckTeamQuota(teamID string) (limit, used float64, ok bool, err error) {
    return 0, 0, true, nil
}
func (m *mockBudgetStore) AddTeamMember(teamID, userID, role string) error          { return nil }
func (m *mockBudgetStore) RemoveTeamMember(teamID, userID string) error             { return nil }
func (m *mockBudgetStore) CreateOrg(org *store.Organization) error                  { return nil }
func (m *mockBudgetStore) GetOrg(id string) (*store.Organization, error)            { return nil, nil }
func (m *mockBudgetStore) ListOrgs() ([]*store.Organization, error)                 { return nil, nil }
func (m *mockBudgetStore) DeleteOrg(id string) error                                { return nil }
func (m *mockBudgetStore) CreateBatch(requests []store.BatchRequest, endpoint, window string) (*store.Batch, error) {
    return nil, nil
}
func (m *mockBudgetStore) GetBatch(id string) (*store.Batch, error)                 { return nil, nil }
func (m *mockBudgetStore) ListBatches() ([]*store.Batch, error)                     { return nil, nil }
func (m *mockBudgetStore) CancelBatch(id string) (*store.Batch, error)              { return nil, nil }
func (m *mockBudgetStore) UpdateBatch(batch *store.Batch) error                     { return nil }
func (m *mockBudgetStore) RecordUsage(keyName, backend, model string, promptTokens, completionTokens int, costUSD float64) error {
    return nil
}
func (m *mockBudgetStore) GetCostSummary(keyName string) (*store.CostSummary, error) { return nil, nil }
func (m *mockBudgetStore) GetCostSummaryAll() (*store.CostSummary, error)           { return nil, nil }
func (m *mockBudgetStore) Close() error                                             { return nil }

func TestBudgetBlock_NoKeyConfig(t *testing.T) {
    slog.Info("test BudgetBlock_NoKeyConfig")
    st := &mockBudgetStore{}
    handler := BudgetBlock(st)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
    }))
    req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
    rec := httptest.NewRecorder()
    handler.ServeHTTP(rec, req)
    if rec.Code != http.StatusOK {
        t.Fatalf("expected 200, got %d", rec.Code)
    }
}

func TestBudgetBlock_NoKeyName(t *testing.T) {
    slog.Info("test BudgetBlock_NoKeyName")
    st := &mockBudgetStore{}
    p := &Principal{KeyConfig: &config.AuthKeyConfig{Name: ""}}
    ctx := ContextWithPrincipal(context.Background(), p)
    handler := BudgetBlock(st)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
    }))
    req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
    req = req.WithContext(ctx)
    rec := httptest.NewRecorder()
    handler.ServeHTTP(rec, req)
    if rec.Code != http.StatusOK {
        t.Fatalf("expected 200, got %d", rec.Code)
    }
}

func TestBudgetBlock_ZeroBudgetLimit(t *testing.T) {
    slog.Info("test BudgetBlock_ZeroBudgetLimit")
    st := &mockBudgetStore{exceeded: true}
    p := &Principal{KeyConfig: &config.AuthKeyConfig{Name: "test", BudgetLimit: 0}}
    ctx := ContextWithPrincipal(context.Background(), p)
    handler := BudgetBlock(st)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
    }))
    req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
    req = req.WithContext(ctx)
    rec := httptest.NewRecorder()
    handler.ServeHTTP(rec, req)
    if rec.Code != http.StatusOK {
        t.Fatalf("expected 200 with zero budget limit, got %d", rec.Code)
    }
}

func TestBudgetBlock_NilStore(t *testing.T) {
    slog.Info("test BudgetBlock_NilStore")
    p := &Principal{KeyConfig: &config.AuthKeyConfig{Name: "test", BudgetLimit: 100}}
    ctx := ContextWithPrincipal(context.Background(), p)
    handler := BudgetBlock(nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
    }))
    req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
    req = req.WithContext(ctx)
    rec := httptest.NewRecorder()
    handler.ServeHTTP(rec, req)
    if rec.Code != http.StatusOK {
        t.Fatalf("expected 200 with nil store, got %d", rec.Code)
    }
}

func TestBudgetBlock_UnderQuota(t *testing.T) {
    slog.Info("test BudgetBlock_UnderQuota")
    st := &mockBudgetStore{used: 50, limit: 100, exceeded: false}
    p := &Principal{KeyConfig: &config.AuthKeyConfig{Name: "test", BudgetLimit: 100}}
    ctx := ContextWithPrincipal(context.Background(), p)
    handler := BudgetBlock(st)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
    }))
    req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
    req = req.WithContext(ctx)
    rec := httptest.NewRecorder()
    handler.ServeHTTP(rec, req)
    if rec.Code != http.StatusOK {
        t.Fatalf("expected 200, got %d", rec.Code)
    }
}

func TestBudgetBlock_Exceeded(t *testing.T) {
    slog.Info("test BudgetBlock_Exceeded")
    st := &mockBudgetStore{used: 100, limit: 100, exceeded: true}
    p := &Principal{KeyConfig: &config.AuthKeyConfig{Name: "test", BudgetLimit: 100}}
    ctx := ContextWithPrincipal(context.Background(), p)
    handler := BudgetBlock(st)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        t.Fatal("should not reach next handler")
    }))
    req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
    req = req.WithContext(ctx)
    rec := httptest.NewRecorder()
    handler.ServeHTTP(rec, req)
    if rec.Code != http.StatusForbidden {
        t.Fatalf("expected 403, got %d", rec.Code)
    }
}

func TestBudgetBlock_StoreError(t *testing.T) {
    slog.Info("test BudgetBlock_StoreError")
    st := &mockBudgetStore{err: errors.New("db down")}
    p := &Principal{KeyConfig: &config.AuthKeyConfig{Name: "test", BudgetLimit: 100}}
    ctx := ContextWithPrincipal(context.Background(), p)
    handler := BudgetBlock(st)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
    }))
    req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
    req = req.WithContext(ctx)
    rec := httptest.NewRecorder()
    handler.ServeHTTP(rec, req)
    if rec.Code != http.StatusOK {
        t.Fatalf("expected 200 on store error (fail open), got %d", rec.Code)
    }
}
