package middleware

import (
    "bufio"
    "context"
    "errors"
    "log/slog"
    "net"
    "net/http"
    "net/http/httptest"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/store"
)

type logMockStore struct {
    logged bool
    err    error
}

func (m *logMockStore) AppendLog(log *store.RequestLog) error {
    m.logged = true
    return m.err
}
func (m *logMockStore) QueryLogs(filter store.LogFilter) ([]*store.RequestLog, int, error) {
    return nil, 0, nil
}
func (m *logMockStore) GetLog(id string) (*store.RequestLog, error)              { return nil, nil }
func (m *logMockStore) ExportLogs(filter store.LogFilter, format string) ([]byte, error) {
    return nil, nil
}
func (m *logMockStore) DistinctLogFilters() (*store.LogFilters, error)            { return &store.LogFilters{}, nil }
func (m *logMockStore) ListKeys() ([]*store.APIKeyEntry, error)                  { return nil, nil }
func (m *logMockStore) GetKey(name string) (*store.APIKeyEntry, error)           { return nil, nil }
func (m *logMockStore) GetKeyByHash(hash string) (*store.APIKeyEntry, error)     { return nil, nil }
func (m *logMockStore) CreateKey(key *store.APIKeyEntry) error                   { return nil }
func (m *logMockStore) UpdateKey(key *store.APIKeyEntry) error                   { return nil }
func (m *logMockStore) DeleteKey(name string) error                              { return nil }
func (m *logMockStore) ListChannels() ([]*store.ChannelEntry, error)             { return nil, nil }
func (m *logMockStore) GetChannel(name string) (*store.ChannelEntry, error)      { return nil, nil }
func (m *logMockStore) CreateChannel(ch *store.ChannelEntry) error               { return nil }
func (m *logMockStore) UpdateChannel(ch *store.ChannelEntry) error               { return nil }
func (m *logMockStore) DeleteChannel(name string) error                          { return nil }
func (m *logMockStore) GetTokenStats(from, to time.Time, groupBy string) ([]*store.TokenStat, error) {
    return nil, nil
}
func (m *logMockStore) GetCostStats(from, to time.Time, groupBy string) ([]*store.CostStat, error) {
    return nil, nil
}
func (m *logMockStore) GetModelStats(from, to time.Time) ([]*store.ModelStat, error) {
    return nil, nil
}
func (m *logMockStore) GetLatencyStats(from, to time.Time) ([]*store.LatencyStat, error) {
    return nil, nil
}
func (m *logMockStore) GetErrorStats(from, to time.Time) ([]*store.ErrorStat, error) {
    return nil, nil
}
func (m *logMockStore) GetDashboardOverview() (*store.DashboardOverview, error)   { return nil, nil }
func (m *logMockStore) GetKeyProfitStats(from, to time.Time) ([]*store.KeyProfitStat, error) {
    return nil, nil
}
func (m *logMockStore) CheckQuota(keyName string) (used, limit float64, exceeded bool, err error) {
    return 0, 0, false, nil
}
func (m *logMockStore) DeductQuota(keyName string, amount float64) error          { return nil }
func (m *logMockStore) CreateTeam(team *store.Team) error                        { return nil }
func (m *logMockStore) GetTeam(id string) (*store.Team, error)                   { return nil, nil }
func (m *logMockStore) ListTeams() ([]*store.Team, error)                        { return nil, nil }
func (m *logMockStore) UpdateTeam(team *store.Team) error                        { return nil }
func (m *logMockStore) DeleteTeam(id string) error                               { return nil }
func (m *logMockStore) BindKeyToTeam(apiKey, teamID string) error                { return nil }
func (m *logMockStore) GetTeamByKey(apiKey string) (*store.Team, error)          { return nil, nil }
func (m *logMockStore) AddTeamCost(teamID string, cost float64) error            { return nil }
func (m *logMockStore) CheckTeamQuota(teamID string) (limit, used float64, ok bool, err error) {
    return 0, 0, true, nil
}
func (m *logMockStore) AddTeamMember(teamID, userID, role string) error          { return nil }
func (m *logMockStore) RemoveTeamMember(teamID, userID string) error             { return nil }
func (m *logMockStore) CreateOrg(org *store.Organization) error                  { return nil }
func (m *logMockStore) GetOrg(id string) (*store.Organization, error)            { return nil, nil }
func (m *logMockStore) ListOrgs() ([]*store.Organization, error)                 { return nil, nil }
func (m *logMockStore) DeleteOrg(id string) error                                { return nil }
func (m *logMockStore) CreateBatch(requests []store.BatchRequest, endpoint, window string) (*store.Batch, error) {
    return nil, nil
}
func (m *logMockStore) GetBatch(id string) (*store.Batch, error)                 { return nil, nil }
func (m *logMockStore) ListBatches() ([]*store.Batch, error)                     { return nil, nil }
func (m *logMockStore) CancelBatch(id string) (*store.Batch, error)              { return nil, nil }
func (m *logMockStore) UpdateBatch(batch *store.Batch) error                     { return nil }
func (m *logMockStore) RecordUsage(keyName, backend, model string, promptTokens, completionTokens int, costUSD float64) error {
    return nil
}
func (m *logMockStore) GetCostSummary(keyName string) (*store.CostSummary, error) { return nil, nil }
func (m *logMockStore) GetCostSummaryAll() (*store.CostSummary, error)           { return nil, nil }
func (m *logMockStore) Close() error                                             { return nil }

func TestNewResponseRecorder(t *testing.T) {
    slog.Info("test NewResponseRecorder")
    rec := httptest.NewRecorder()
    rr := NewResponseRecorder(rec)
    if rr.StatusCode != http.StatusOK {
        t.Errorf("expected default 200, got %d", rr.StatusCode)
    }
    if rr.Size != 0 {
        t.Errorf("expected size 0, got %d", rr.Size)
    }
}

func TestResponseRecorder_WriteHeader(t *testing.T) {
    slog.Info("test ResponseRecorder_WriteHeader")
    rec := httptest.NewRecorder()
    rr := NewResponseRecorder(rec)
    rr.WriteHeader(http.StatusNotFound)
    if rr.StatusCode != http.StatusNotFound {
        t.Errorf("expected 404, got %d", rr.StatusCode)
    }
    if rec.Code != http.StatusNotFound {
        t.Errorf("underlying recorder not updated")
    }
}

func TestResponseRecorder_Write(t *testing.T) {
    slog.Info("test ResponseRecorder_Write")
    rec := httptest.NewRecorder()
    rr := NewResponseRecorder(rec)
    n, err := rr.Write([]byte("hello"))
    if err != nil {
        t.Fatal(err)
    }
    if n != 5 {
        t.Errorf("expected 5 bytes, got %d", n)
    }
    if rr.Size != 5 {
        t.Errorf("expected size 5, got %d", rr.Size)
    }
    if rec.Body.String() != "hello" {
        t.Errorf("body mismatch: %s", rec.Body.String())
    }
}

func TestResponseRecorder_Write_Multiple(t *testing.T) {
    slog.Info("test ResponseRecorder_Write_Multiple")
    rec := httptest.NewRecorder()
    rr := NewResponseRecorder(rec)
    _, _ = rr.Write([]byte("hello"))
    _, _ = rr.Write([]byte(" world"))
    if rr.Size != 11 {
        t.Errorf("expected size 11, got %d", rr.Size)
    }
}

func TestResponseRecorder_Hijack(t *testing.T) {
    slog.Info("test ResponseRecorder_Hijack")
    rec := httptest.NewRecorder()
    rr := NewResponseRecorder(rec)
    _, _, err := rr.Hijack()
    if err == nil {
        t.Error("expected error for non-hijackable response writer")
    }
}

func TestResponseRecorder_Flush(t *testing.T) {
    slog.Info("test ResponseRecorder_Flush")
    rec := httptest.NewRecorder()
    rr := NewResponseRecorder(rec)
    rr.Flush()
}

func TestInitRequestLog(t *testing.T) {
    slog.Info("test InitRequestLog")
    req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions?stream=true", nil)
    req.Header.Set("X-Request-ID", "req-123")
    req.Header.Set("X-Fusion-Project-Id", "proj-1")
    req.Header.Set("X-Fusion-Chat-Id", "chat-1")
    req.Header.Set("X-Space-Id", "space-1")
    ctx := context.WithValue(req.Context(), RequestIDKey, "req-123")
    req = req.WithContext(ctx)

    entry := InitRequestLog(req)
    if entry.RequestID != "req-123" {
        t.Errorf("expected req-123, got %s", entry.RequestID)
    }
    if entry.RequestType != "POST /v1/chat/completions" {
        t.Errorf("unexpected request type: %s", entry.RequestType)
    }
    if !entry.IsStream {
        t.Error("expected stream=true")
    }
    if entry.ProjectID != "proj-1" {
        t.Errorf("expected proj-1, got %s", entry.ProjectID)
    }
    if entry.ChatID != "chat-1" {
        t.Errorf("expected chat-1, got %s", entry.ChatID)
    }
    if entry.SpaceID != "space-1" {
        t.Errorf("expected space-1, got %s", entry.SpaceID)
    }
    if !entry.IsSuccess {
        t.Error("expected default success=true")
    }
}

func TestInitRequestLog_NoHeaders(t *testing.T) {
    slog.Info("test InitRequestLog_NoHeaders")
    req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
    entry := InitRequestLog(req)
    if entry.RequestType != "GET /v1/models" {
        t.Errorf("unexpected request type: %s", entry.RequestType)
    }
    if entry.IsStream {
        t.Error("expected stream=false")
    }
}

func TestWithRequestLogContext(t *testing.T) {
    slog.Info("test WithRequestLogContext")
    entry := &store.RequestLog{RequestID: "test-123"}
    req := httptest.NewRequest(http.MethodGet, "/test", nil)
    req = WithRequestLogContext(req, entry)
    got := GetRequestLog(req.Context())
    if got == nil || got.RequestID != "test-123" {
        t.Error("request log not stored in context")
    }
}

func TestGetRequestLog_Nil(t *testing.T) {
    slog.Info("test GetRequestLog_Nil")
    req := httptest.NewRequest(http.MethodGet, "/test", nil)
    got := GetRequestLog(req.Context())
    if got != nil {
        t.Error("expected nil")
    }
}

func TestFinalizeAndAppendLog(t *testing.T) {
    slog.Info("test FinalizeAndAppendLog")
    st := &logMockStore{}
    entry := &store.RequestLog{StatusCode: 200}
    start := time.Now().Add(-1 * time.Second)
    FinalizeAndAppendLog(entry, st, start, "test-key")
    if !st.logged {
        t.Error("expected log to be appended")
    }
    if entry.APIKeyName != "test-key" {
        t.Errorf("expected test-key, got %s", entry.APIKeyName)
    }
    if entry.Latency <= 0 {
        t.Error("expected positive latency")
    }
    if !entry.IsSuccess {
        t.Error("200 should be success")
    }
}

func TestFinalizeAndAppendLog_Failure(t *testing.T) {
    slog.Info("test FinalizeAndAppendLog_Failure")
    st := &logMockStore{}
    entry := &store.RequestLog{StatusCode: 500}
    start := time.Now()
    FinalizeAndAppendLog(entry, st, start, "test-key")
    if entry.IsSuccess {
        t.Error("500 should not be success")
    }
}

func TestFinalizeAndAppendLog_NilStore(t *testing.T) {
    slog.Info("test FinalizeAndAppendLog_NilStore")
    entry := &store.RequestLog{StatusCode: 200}
    start := time.Now()
    FinalizeAndAppendLog(entry, nil, start, "test-key")
    if entry.APIKeyName != "test-key" {
        t.Errorf("expected test-key, got %s", entry.APIKeyName)
    }
}

func TestFinalizeAndAppendLog_StoreError(t *testing.T) {
    slog.Info("test FinalizeAndAppendLog_StoreError")
    st := &logMockStore{err: errors.New("db error")}
    entry := &store.RequestLog{StatusCode: 200}
    start := time.Now()
    FinalizeAndAppendLog(entry, st, start, "test-key")
}

func TestFinalizeAndAppendLog_BoundaryStatusCodes(t *testing.T) {
    slog.Info("test FinalizeAndAppendLog_BoundaryStatusCodes")
    cases := []struct {
        code    int
        success bool
    }{
        {200, true},
        {201, true},
        {299, true},
        {300, true},
        {399, true},
        {400, false},
        {404, false},
        {500, false},
    }
    for _, tc := range cases {
        st := &logMockStore{}
        entry := &store.RequestLog{StatusCode: tc.code}
        FinalizeAndAppendLog(entry, st, time.Now(), "key")
        if entry.IsSuccess != tc.success {
            t.Errorf("code %d: expected success=%v, got %v", tc.code, tc.success, entry.IsSuccess)
        }
    }
}

type hijackableResponseWriter struct {
    http.ResponseWriter
    hijacked bool
}

func (h *hijackableResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
    h.hijacked = true
    return nil, nil, nil
}

func TestResponseRecorder_Hijack_Success(t *testing.T) {
    slog.Info("test ResponseRecorder_Hijack_Success")
    inner := &hijackableResponseWriter{ResponseWriter: httptest.NewRecorder()}
    rr := NewResponseRecorder(inner)
    _, _, err := rr.Hijack()
    if err != nil {
        t.Fatalf("expected no error for hijackable writer, got %v", err)
    }
    if !inner.hijacked {
        t.Error("inner Hijack should have been called")
    }
}
