package admin

import (
    "bytes"
    "context"
    "encoding/json"
    "fmt"
    "io"
    "log/slog"
    "net/http"
    "net/http/httptest"
    "os"
    "path/filepath"
    "strings"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/store"
    "github.com/golang-jwt/jwt/v5"
    "gopkg.in/yaml.v3"
)

func init() {
    slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug})))
}

type mockStore struct {
    keys     []*store.APIKeyEntry
    channels []*store.ChannelEntry
    logs     []*store.RequestLog
    quota    map[string]quotaInfo
}

type quotaInfo struct {
    used     float64
    limit    float64
    exceeded bool
}

func newMockStore() *mockStore {
    return &mockStore{
        quota: make(map[string]quotaInfo),
    }
}

func (m *mockStore) AppendLog(log *store.RequestLog) error {
    m.logs = append(m.logs, log)
    return nil
}

func (m *mockStore) QueryLogs(filter store.LogFilter) ([]*store.RequestLog, int, error) {
    return m.logs, len(m.logs), nil
}

func (m *mockStore) GetLog(id string) (*store.RequestLog, error) {
    for _, l := range m.logs {
        if l.ID == id {
            return l, nil
        }
    }
    return nil, fmt.Errorf("log not found")
}

func (m *mockStore) ExportLogs(filter store.LogFilter, format string) ([]byte, error) {
    if format == "csv" {
        var buf bytes.Buffer
        buf.WriteString("id,model,status_code\n")
        for _, l := range m.logs {
            buf.WriteString(fmt.Sprintf("%s,%s,%d\n", l.ID, l.Model, l.StatusCode))
        }
        return buf.Bytes(), nil
    }
    data, _ := json.Marshal(m.logs)
    return data, nil
}

func (m *mockStore) ListKeys() ([]*store.APIKeyEntry, error) {
    return m.keys, nil
}

func (m *mockStore) GetKey(name string) (*store.APIKeyEntry, error) {
    for _, k := range m.keys {
        if k.Name == name {
            return k, nil
        }
    }
    return nil, fmt.Errorf("key %q not found", name)
}

func (m *mockStore) GetKeyByHash(hash string) (*store.APIKeyEntry, error) {
    for _, k := range m.keys {
        if k.KeyHash == hash {
            return k, nil
        }
    }
    return nil, fmt.Errorf("key not found by hash")
}

func (m *mockStore) CreateKey(key *store.APIKeyEntry) error {
    for _, k := range m.keys {
        if k.Name == key.Name {
            return fmt.Errorf("key %q already exists", key.Name)
        }
    }
    key.CreatedAt = time.Now()
    key.UpdatedAt = time.Now()
    m.keys = append(m.keys, key)
    return nil
}

func (m *mockStore) UpdateKey(key *store.APIKeyEntry) error {
    for i, k := range m.keys {
        if k.Name == key.Name {
            key.UpdatedAt = time.Now()
            m.keys[i] = key
            return nil
        }
    }
    return fmt.Errorf("key %q not found", key.Name)
}

func (m *mockStore) DeleteKey(name string) error {
    for i, k := range m.keys {
        if k.Name == name {
            m.keys = append(m.keys[:i], m.keys[i+1:]...)
            return nil
        }
    }
    return fmt.Errorf("key %q not found", name)
}

func (m *mockStore) ListChannels() ([]*store.ChannelEntry, error) {
    return m.channels, nil
}

func (m *mockStore) GetChannel(name string) (*store.ChannelEntry, error) {
    for _, c := range m.channels {
        if c.Name == name {
            return c, nil
        }
    }
    return nil, fmt.Errorf("channel %q not found", name)
}

func (m *mockStore) CreateChannel(ch *store.ChannelEntry) error {
    for _, c := range m.channels {
        if c.Name == ch.Name {
            return fmt.Errorf("channel %q already exists", ch.Name)
        }
    }
    ch.CreatedAt = time.Now()
    ch.UpdatedAt = time.Now()
    m.channels = append(m.channels, ch)
    return nil
}

func (m *mockStore) UpdateChannel(ch *store.ChannelEntry) error {
    for i, c := range m.channels {
        if c.Name == ch.Name {
            ch.UpdatedAt = time.Now()
            m.channels[i] = ch
            return nil
        }
    }
    return fmt.Errorf("channel %q not found", ch.Name)
}

func (m *mockStore) DeleteChannel(name string) error {
    for i, c := range m.channels {
        if c.Name == name {
            m.channels = append(m.channels[:i], m.channels[i+1:]...)
            return nil
        }
    }
    return fmt.Errorf("channel %q not found", name)
}

func (m *mockStore) GetTokenStats(from, to time.Time, groupBy string) ([]*store.TokenStat, error) {
    return []*store.TokenStat{{Time: from.Format("2006-01-02"), InputTokens: 100, OutputTokens: 50, TotalTokens: 150}}, nil
}

func (m *mockStore) GetCostStats(from, to time.Time, groupBy string) ([]*store.CostStat, error) {
    return []*store.CostStat{{Time: from.Format("2006-01-02"), Cost: 1.5, CostUSD: 0.2, Savings: 0.1}}, nil
}

func (m *mockStore) GetModelStats(from, to time.Time) ([]*store.ModelStat, error) {
    return []*store.ModelStat{{Model: "qwen3", RequestCount: 10, InputTokens: 500, OutputTokens: 200, Cost: 0.5}}, nil
}

func (m *mockStore) GetLatencyStats(from, to time.Time) ([]*store.LatencyStat, error) {
    return []*store.LatencyStat{{Channel: "local", P50: 50, P90: 100, P99: 200}}, nil
}

func (m *mockStore) GetErrorStats(from, to time.Time) ([]*store.ErrorStat, error) {
    return []*store.ErrorStat{{Channel: "cloud", Model: "gpt4", ErrorType: "timeout", Count: 3}}, nil
}

func (m *mockStore) GetDashboardOverview() (*store.DashboardOverview, error) {
    return &store.DashboardOverview{TotalRequests: 100, TotalTokens: 5000, TotalCost: 3.5, LocalHitRate: 0.6}, nil
}

func (m *mockStore) GetKeyProfitStats(from, to time.Time) ([]*store.KeyProfitStat, error) {
    return []*store.KeyProfitStat{{KeyName: "test-key", TotalInput: 1000, TotalOutput: 500, Ratio: 0.5, TotalCost: 2.0, RequestCount: 50}}, nil
}

func (m *mockStore) CheckQuota(keyName string) (used, limit float64, exceeded bool, err error) {
    q, ok := m.quota[keyName]
    if !ok {
        return 0, 0, false, fmt.Errorf("key %q not found", keyName)
    }
    return q.used, q.limit, q.exceeded, nil
}

func (m *mockStore) DeductQuota(keyName string, amount float64) error {
    q, ok := m.quota[keyName]
    if !ok {
        return fmt.Errorf("key %q not found", keyName)
    }
    q.used += amount
    m.quota[keyName] = q
    return nil
}

func (m *mockStore) CreateTeam(team *store.Team) error                  { return nil }
func (m *mockStore) GetTeam(id string) (*store.Team, error)            { return nil, nil }
func (m *mockStore) ListTeams() ([]*store.Team, error)                 { return nil, nil }
func (m *mockStore) UpdateTeam(team *store.Team) error                 { return nil }
func (m *mockStore) DeleteTeam(id string) error                        { return nil }
func (m *mockStore) BindKeyToTeam(apiKey, teamID string) error         { return nil }
func (m *mockStore) GetTeamByKey(apiKey string) (*store.Team, error)   { return nil, nil }
func (m *mockStore) AddTeamCost(teamID string, cost float64) error     { return nil }
func (m *mockStore) CheckTeamQuota(teamID string) (limit, used float64, ok bool, err error) {
    return 0, 0, true, nil
}
func (m *mockStore) AddTeamMember(teamID, userID, role string) error  { return nil }
func (m *mockStore) RemoveTeamMember(teamID, userID string) error     { return nil }
func (m *mockStore) CreateOrg(org *store.Organization) error          { return nil }
func (m *mockStore) GetOrg(id string) (*store.Organization, error)    { return nil, nil }
func (m *mockStore) ListOrgs() ([]*store.Organization, error)         { return nil, nil }
func (m *mockStore) DeleteOrg(id string) error                        { return nil }
func (m *mockStore) CreateBatch(requests []store.BatchRequest, endpoint, window string) (*store.Batch, error) {
    return nil, nil
}
func (m *mockStore) GetBatch(id string) (*store.Batch, error)         { return nil, nil }
func (m *mockStore) ListBatches() ([]*store.Batch, error)             { return nil, nil }
func (m *mockStore) CancelBatch(id string) (*store.Batch, error)      { return nil, nil }
func (m *mockStore) UpdateBatch(batch *store.Batch) error             { return nil }
func (m *mockStore) RecordUsage(keyName, backend, model string, promptTokens, completionTokens int, costUSD float64) error {
    return nil
}
func (m *mockStore) GetCostSummary(keyName string) (*store.CostSummary, error) {
    return nil, nil
}
func (m *mockStore) GetCostSummaryAll() (*store.CostSummary, error) {
    return nil, nil
}

func newTestAuth(t *testing.T) *AdminAuth {
    t.Helper()
    auth, err := NewAdminAuth("test-secret-that-is-at-least-32-characters-long", map[string]string{
        "admin": "password123",
    })
    if err != nil {
        t.Fatalf("failed to create test auth: %v", err)
    }
    return auth
}

func newTestHandler(t *testing.T, st store.Store, auth *AdminAuth, configPath string) *Handler {
    t.Helper()
    return NewHandler(st, auth, configPath)
}

func makeAuthenticatedRequest(t *testing.T, auth *AdminAuth, method, path string, body interface{}) *http.Request {
    t.Helper()
    var bodyReader *bytes.Reader
    if body != nil {
        b, err := json.Marshal(body)
        if err != nil {
            t.Fatalf("failed to marshal body: %v", err)
        }
        bodyReader = bytes.NewReader(b)
    } else {
        bodyReader = bytes.NewReader(nil)
    }

    req := httptest.NewRequest(method, path, bodyReader)
    token, err := auth.GenerateToken("admin", "admin")
    if err != nil {
        t.Fatalf("failed to generate token: %v", err)
    }
    req.Header.Set("Authorization", "Bearer "+token)
    return req
}

func decodeResponse(t *testing.T, rec *httptest.ResponseRecorder) map[string]interface{} {
    t.Helper()
    var result map[string]interface{}
    if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
        t.Fatalf("failed to decode response: %v", err)
    }
    return result
}

func decodeResponseArray(t *testing.T, rec *httptest.ResponseRecorder) []interface{} {
    t.Helper()
    var result []interface{}
    if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
        t.Fatalf("failed to decode response array: %v", err)
    }
    return result
}

func loadTestConfig(t *testing.T, yamlContent string) {
    t.Helper()
    tmpDir := t.TempDir()
    configPath := filepath.Join(tmpDir, "test-config.yaml")
    if err := os.WriteFile(configPath, []byte(yamlContent), 0644); err != nil {
        t.Fatalf("failed to write test config: %v", err)
    }
    _, err := config.Load(configPath)
    if err != nil {
        t.Fatalf("failed to load test config: %v", err)
    }
}

func TestExtractBearerToken(t *testing.T) {
    t.Parallel()

    t.Run("bearer_header", func(t *testing.T) {
        t.Parallel()
        req := httptest.NewRequest(http.MethodGet, "/", nil)
        req.Header.Set("Authorization", "Bearer mytoken123")
        token := extractBearerToken(req)
        if token != "mytoken123" {
            t.Fatalf("expected mytoken123, got %q", token)
        }
    })

    t.Run("cookie_token", func(t *testing.T) {
        t.Parallel()
        req := httptest.NewRequest(http.MethodGet, "/", nil)
        req.AddCookie(&http.Cookie{Name: "admin_token", Value: "cookie-token-xyz"})
        token := extractBearerToken(req)
        if token != "cookie-token-xyz" {
            t.Fatalf("expected cookie-token-xyz, got %q", token)
        }
    })

    t.Run("no_token", func(t *testing.T) {
        t.Parallel()
        req := httptest.NewRequest(http.MethodGet, "/", nil)
        token := extractBearerToken(req)
        if token != "" {
            t.Fatalf("expected empty token, got %q", token)
        }
    })

    t.Run("bearer_takes_precedence_over_cookie", func(t *testing.T) {
        t.Parallel()
        req := httptest.NewRequest(http.MethodGet, "/", nil)
        req.Header.Set("Authorization", "Bearer bearer-token")
        req.AddCookie(&http.Cookie{Name: "admin_token", Value: "cookie-token"})
        token := extractBearerToken(req)
        if token != "bearer-token" {
            t.Fatalf("expected bearer-token, got %q", token)
        }
    })

    t.Run("empty_cookie_value_ignored", func(t *testing.T) {
        t.Parallel()
        req := httptest.NewRequest(http.MethodGet, "/", nil)
        req.AddCookie(&http.Cookie{Name: "admin_token", Value: ""})
        token := extractBearerToken(req)
        if token != "" {
            t.Fatalf("expected empty token, got %q", token)
        }
    })

    t.Run("authorization_header_without_bearer_prefix", func(t *testing.T) {
        t.Parallel()
        req := httptest.NewRequest(http.MethodGet, "/", nil)
        req.Header.Set("Authorization", "Basic dXNlcjpwYXNz")
        token := extractBearerToken(req)
        if token != "" {
            t.Fatalf("expected empty token, got %q", token)
        }
    })

    t.Run("bearer_with_empty_value", func(t *testing.T) {
        t.Parallel()
        req := httptest.NewRequest(http.MethodGet, "/", nil)
        req.Header.Set("Authorization", "Bearer ")
        token := extractBearerToken(req)
        if token != "" {
            t.Fatalf("expected empty token, got %q", token)
        }
    })
}

func TestWithAuth(t *testing.T) {
    t.Parallel()

    auth := newTestAuth(t)
    ms := newMockStore()
    h := newTestHandler(t, ms, auth, "")

    called := false
    next := func(w http.ResponseWriter, r *http.Request) {
        called = true
        claims := GetAdminClaims(r.Context())
        if claims == nil {
            t.Fatal("expected claims in context")
        }
        if claims.Username != "admin" {
            t.Fatalf("expected username admin, got %q", claims.Username)
        }
        w.WriteHeader(http.StatusOK)
    }

    t.Run("no_token_returns_401", func(t *testing.T) {
        called = false
        req := httptest.NewRequest(http.MethodGet, "/admin/api/keys", nil)
        rec := httptest.NewRecorder()
        h.withAuth(next).ServeHTTP(rec, req)
        if rec.Code != http.StatusUnauthorized {
            t.Fatalf("expected 401, got %d", rec.Code)
        }
        if called {
            t.Fatal("next handler should not be called")
        }
    })

    t.Run("invalid_token_returns_401", func(t *testing.T) {
        called = false
        req := httptest.NewRequest(http.MethodGet, "/admin/api/keys", nil)
        req.Header.Set("Authorization", "Bearer invalid-token")
        rec := httptest.NewRecorder()
        h.withAuth(next).ServeHTTP(rec, req)
        if rec.Code != http.StatusUnauthorized {
            t.Fatalf("expected 401, got %d", rec.Code)
        }
        if called {
            t.Fatal("next handler should not be called")
        }
    })

    t.Run("valid_bearer_token_passes", func(t *testing.T) {
        called = false
        req := makeAuthenticatedRequest(t, auth, http.MethodGet, "/admin/api/keys", nil)
        rec := httptest.NewRecorder()
        h.withAuth(next).ServeHTTP(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d", rec.Code)
        }
        if !called {
            t.Fatal("next handler should be called")
        }
    })

    t.Run("cookie_token_passes", func(t *testing.T) {
        called = false
        token, err := auth.GenerateToken("admin", "admin")
        if err != nil {
            t.Fatalf("failed to generate token: %v", err)
        }
        req := httptest.NewRequest(http.MethodGet, "/admin/api/keys", nil)
        req.AddCookie(&http.Cookie{Name: "admin_token", Value: token})
        rec := httptest.NewRecorder()
        h.withAuth(next).ServeHTTP(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d", rec.Code)
        }
        if !called {
            t.Fatal("next handler should be called")
        }
    })
}

func TestAdminAuth_GenerateAndValidate(t *testing.T) {
    t.Parallel()

    t.Run("round_trip", func(t *testing.T) {
        t.Parallel()
        auth := newTestAuth(t)
        token, err := auth.GenerateToken("admin", "admin")
        if err != nil {
            t.Fatalf("GenerateToken failed: %v", err)
        }
        claims, err := auth.ValidateToken(token)
        if err != nil {
            t.Fatalf("ValidateToken failed: %v", err)
        }
        if claims.Username != "admin" {
            t.Fatalf("expected username admin, got %q", claims.Username)
        }
        if claims.Role != "admin" {
            t.Fatalf("expected role admin, got %q", claims.Role)
        }
        if claims.Issuer != "fusion-gateway-admin" {
            t.Fatalf("expected issuer fusion-gateway-admin, got %q", claims.Issuer)
        }
    })

    t.Run("expired_token_fails", func(t *testing.T) {
        t.Parallel()
        auth := newTestAuth(t)
        claims := AdminClaims{
            Username: "admin",
            Role:     "admin",
            RegisteredClaims: jwt.RegisteredClaims{
                ExpiresAt: jwt.NewNumericDate(time.Now().Add(-1 * time.Hour)),
                IssuedAt:  jwt.NewNumericDate(time.Now().Add(-25 * time.Hour)),
                Issuer:    "fusion-gateway-admin",
            },
        }
        token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
        tokenStr, err := token.SignedString(auth.jwtSecret)
        if err != nil {
            t.Fatalf("signing token failed: %v", err)
        }
        _, err = auth.ValidateToken(tokenStr)
        if err == nil {
            t.Fatal("expected validation to fail for expired token")
        }
    })

    t.Run("wrong_signing_method_fails", func(t *testing.T) {
        t.Parallel()
        auth := newTestAuth(t)
        claims := AdminClaims{
            Username: "admin",
            Role:     "admin",
            RegisteredClaims: jwt.RegisteredClaims{
                ExpiresAt: jwt.NewNumericDate(time.Now().Add(24 * time.Hour)),
                IssuedAt:  jwt.NewNumericDate(time.Now()),
                Issuer:    "fusion-gateway-admin",
            },
        }
        token := jwt.NewWithClaims(jwt.SigningMethodNone, claims)
        tokenStr, err := token.SignedString(jwt.UnsafeAllowNoneSignatureType)
        if err != nil {
            t.Fatalf("signing token failed: %v", err)
        }
        _, err = auth.ValidateToken(tokenStr)
        if err == nil {
            t.Fatal("expected validation to fail for none signing method")
        }
    })

    t.Run("empty_secret_generate_fails", func(t *testing.T) {
        t.Parallel()
        auth := &AdminAuth{}
        _, err := auth.GenerateToken("admin", "admin")
        if err == nil {
            t.Fatal("expected error when secret is empty")
        }
    })

    t.Run("empty_secret_validate_fails", func(t *testing.T) {
        t.Parallel()
        auth := &AdminAuth{}
        _, err := auth.ValidateToken("sometoken")
        if err == nil {
            t.Fatal("expected error when secret is empty")
        }
    })

    t.Run("new_admin_auth_short_secret_fails", func(t *testing.T) {
        t.Parallel()
        _, err := NewAdminAuth("short", map[string]string{"admin": "password123"})
        if err == nil {
            t.Fatal("expected error for short secret")
        }
    })

    t.Run("new_admin_auth_empty_secret_returns_disabled", func(t *testing.T) {
        t.Parallel()
        auth, err := NewAdminAuth("", map[string]string{"admin": "password123"})
        if err != nil {
            t.Fatalf("unexpected error: %v", err)
        }
        if auth.Enabled() {
            t.Fatal("expected auth to be disabled with empty secret")
        }
    })

    t.Run("authenticate_success", func(t *testing.T) {
        t.Parallel()
        auth := newTestAuth(t)
        if !auth.Authenticate("admin", "password123") {
            t.Fatal("expected authentication to succeed")
        }
    })

    t.Run("authenticate_wrong_password", func(t *testing.T) {
        t.Parallel()
        auth := newTestAuth(t)
        if auth.Authenticate("admin", "wrongpassword") {
            t.Fatal("expected authentication to fail")
        }
    })

    t.Run("authenticate_unknown_user", func(t *testing.T) {
        t.Parallel()
        auth := newTestAuth(t)
        if auth.Authenticate("unknown", "password123") {
            t.Fatal("expected authentication to fail for unknown user")
        }
    })

    t.Run("authenticate_no_users", func(t *testing.T) {
        t.Parallel()
        auth := &AdminAuth{jwtSecret: []byte("test-secret-that-is-at-least-32-characters-long")}
        if auth.Authenticate("admin", "password") {
            t.Fatal("expected authentication to fail with no users")
        }
    })

    t.Run("enabled_check", func(t *testing.T) {
        t.Parallel()
        auth := newTestAuth(t)
        if !auth.Enabled() {
            t.Fatal("expected auth to be enabled")
        }
    })

    t.Run("is_bcrypt_hash", func(t *testing.T) {
        t.Parallel()
        validHash := "$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy"
        if !isBcryptHash(validHash) {
            t.Fatal("expected $2a$ prefix to be recognized as bcrypt")
        }
        bHash := "$2b$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy"
        if !isBcryptHash(bHash) {
            t.Fatal("expected $2b$ prefix to be recognized as bcrypt")
        }
        yHash := "$2y$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy"
        if !isBcryptHash(yHash) {
            t.Fatal("expected $2y$ prefix to be recognized as bcrypt")
        }
        if isBcryptHash("plaintext") {
            t.Fatal("expected plaintext to not be recognized as bcrypt")
        }
    })

    t.Run("pre_hashed_password_accepted", func(t *testing.T) {
        t.Parallel()
        preHashed := "$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy"
        auth, err := NewAdminAuth("test-secret-that-is-at-least-32-characters-long", map[string]string{
            "admin": preHashed,
        })
        if err != nil {
            t.Fatalf("unexpected error: %v", err)
        }
        if !auth.Enabled() {
            t.Fatal("expected auth to be enabled")
        }
    })
}

func TestContextHelpers(t *testing.T) {
    t.Parallel()

    t.Run("with_and_get_claims", func(t *testing.T) {
        t.Parallel()
        claims := &AdminClaims{Username: "admin", Role: "admin"}
        ctx := WithAdminContext(context.Background(), claims)
        got := GetAdminClaims(ctx)
        if got == nil {
            t.Fatal("expected claims, got nil")
        }
        if got.Username != "admin" {
            t.Fatalf("expected username admin, got %q", got.Username)
        }
    })

    t.Run("get_claims_missing", func(t *testing.T) {
        t.Parallel()
        got := GetAdminClaims(context.Background())
        if got != nil {
            t.Fatal("expected nil claims from empty context")
        }
    })
}

func TestHandleKeys(t *testing.T) {
    t.Parallel()

    auth := newTestAuth(t)
    ms := newMockStore()
    h := newTestHandler(t, ms, auth, "")

    t.Run("list_keys_empty", func(t *testing.T) {
        req := makeAuthenticatedRequest(t, auth, http.MethodGet, "/admin/api/keys", nil)
        rec := httptest.NewRecorder()
        h.handleKeys(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d", rec.Code)
        }
        arr := decodeResponseArray(t, rec)
        if len(arr) != 0 {
            t.Fatalf("expected empty array, got %d items", len(arr))
        }
    })

    t.Run("create_key_success", func(t *testing.T) {
        body := map[string]interface{}{
            "name":   "test-key",
            "status": "active",
            "models": []string{"qwen3"},
            "quota":  100.0,
            "rpm":    60,
            "tpm":    1000,
            "budget": 50.0,
        }
        req := makeAuthenticatedRequest(t, auth, http.MethodPost, "/admin/api/keys", body)
        rec := httptest.NewRecorder()
        h.handleKeys(rec, req)
        if rec.Code != http.StatusCreated {
            t.Fatalf("expected 201, got %d; body: %s", rec.Code, rec.Body.String())
        }
    })

    t.Run("list_keys_after_create", func(t *testing.T) {
        req := makeAuthenticatedRequest(t, auth, http.MethodGet, "/admin/api/keys", nil)
        rec := httptest.NewRecorder()
        h.handleKeys(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d", rec.Code)
        }
        arr := decodeResponseArray(t, rec)
        if len(arr) != 1 {
            t.Fatalf("expected 1 key, got %d", len(arr))
        }
    })

    t.Run("create_key_missing_name", func(t *testing.T) {
        body := map[string]interface{}{
            "status": "active",
        }
        req := makeAuthenticatedRequest(t, auth, http.MethodPost, "/admin/api/keys", body)
        rec := httptest.NewRecorder()
        h.handleKeys(rec, req)
        if rec.Code != http.StatusBadRequest {
            t.Fatalf("expected 400, got %d", rec.Code)
        }
    })

    t.Run("create_key_negative_quota", func(t *testing.T) {
        body := map[string]interface{}{
            "name":  "bad-key",
            "quota": -10.0,
        }
        req := makeAuthenticatedRequest(t, auth, http.MethodPost, "/admin/api/keys", body)
        rec := httptest.NewRecorder()
        h.handleKeys(rec, req)
        if rec.Code != http.StatusBadRequest {
            t.Fatalf("expected 400, got %d", rec.Code)
        }
    })

    t.Run("create_key_negative_rpm", func(t *testing.T) {
        body := map[string]interface{}{
            "name": "bad-key",
            "rpm":  -1,
        }
        req := makeAuthenticatedRequest(t, auth, http.MethodPost, "/admin/api/keys", body)
        rec := httptest.NewRecorder()
        h.handleKeys(rec, req)
        if rec.Code != http.StatusBadRequest {
            t.Fatalf("expected 400, got %d", rec.Code)
        }
    })

    t.Run("create_key_negative_tpm", func(t *testing.T) {
        body := map[string]interface{}{
            "name": "bad-key",
            "tpm":  -1,
        }
        req := makeAuthenticatedRequest(t, auth, http.MethodPost, "/admin/api/keys", body)
        rec := httptest.NewRecorder()
        h.handleKeys(rec, req)
        if rec.Code != http.StatusBadRequest {
            t.Fatalf("expected 400, got %d", rec.Code)
        }
    })

    t.Run("create_key_negative_budget", func(t *testing.T) {
        body := map[string]interface{}{
            "name":   "bad-key",
            "budget": -5.0,
        }
        req := makeAuthenticatedRequest(t, auth, http.MethodPost, "/admin/api/keys", body)
        rec := httptest.NewRecorder()
        h.handleKeys(rec, req)
        if rec.Code != http.StatusBadRequest {
            t.Fatalf("expected 400, got %d", rec.Code)
        }
    })

    t.Run("create_key_duplicate_name", func(t *testing.T) {
        body := map[string]interface{}{
            "name":   "test-key",
            "status": "active",
        }
        req := makeAuthenticatedRequest(t, auth, http.MethodPost, "/admin/api/keys", body)
        rec := httptest.NewRecorder()
        h.handleKeys(rec, req)
        if rec.Code != http.StatusConflict {
            t.Fatalf("expected 409, got %d", rec.Code)
        }
    })

    t.Run("create_key_invalid_json", func(t *testing.T) {
        req := makeAuthenticatedRequest(t, auth, http.MethodPost, "/admin/api/keys", nil)
        req.Body = io.NopCloser(bytes.NewReader([]byte("not-json")))
        req.Header.Set("Content-Type", "application/json")
        rec := httptest.NewRecorder()
        h.handleKeys(rec, req)
        if rec.Code != http.StatusBadRequest {
            t.Fatalf("expected 400, got %d", rec.Code)
        }
    })

    t.Run("method_not_allowed", func(t *testing.T) {
        req := makeAuthenticatedRequest(t, auth, http.MethodPatch, "/admin/api/keys", nil)
        rec := httptest.NewRecorder()
        h.handleKeys(rec, req)
        if rec.Code != http.StatusMethodNotAllowed {
            t.Fatalf("expected 405, got %d", rec.Code)
        }
    })
}

func TestHandleKeyByID(t *testing.T) {
    t.Parallel()

    auth := newTestAuth(t)
    ms := newMockStore()
    _ = ms.CreateKey(&store.APIKeyEntry{Name: "existing-key", Status: "active", AllowedModels: []string{"qwen3"}})
    h := newTestHandler(t, ms, auth, "")

    t.Run("get_key_success", func(t *testing.T) {
        req := makeAuthenticatedRequest(t, auth, http.MethodGet, "/admin/api/keys/existing-key", nil)
        rec := httptest.NewRecorder()
        h.handleKeyByID(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
        }
    })

    t.Run("get_key_not_found", func(t *testing.T) {
        req := makeAuthenticatedRequest(t, auth, http.MethodGet, "/admin/api/keys/nonexistent", nil)
        rec := httptest.NewRecorder()
        h.handleKeyByID(rec, req)
        if rec.Code != http.StatusNotFound {
            t.Fatalf("expected 404, got %d", rec.Code)
        }
    })

    t.Run("update_key_success", func(t *testing.T) {
        body := map[string]interface{}{
            "name":   "existing-key",
            "status": "disabled",
            "models": []string{"gpt4"},
            "quota":  200.0,
        }
        req := makeAuthenticatedRequest(t, auth, http.MethodPut, "/admin/api/keys/existing-key", body)
        rec := httptest.NewRecorder()
        h.handleKeyByID(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
        }
    })

    t.Run("update_key_not_found", func(t *testing.T) {
        body := map[string]interface{}{
            "name":   "nonexistent",
            "status": "active",
        }
        req := makeAuthenticatedRequest(t, auth, http.MethodPut, "/admin/api/keys/nonexistent", body)
        rec := httptest.NewRecorder()
        h.handleKeyByID(rec, req)
        if rec.Code != http.StatusNotFound {
            t.Fatalf("expected 404, got %d", rec.Code)
        }
    })

    t.Run("delete_key_success", func(t *testing.T) {
        ms2 := newMockStore()
        _ = ms2.CreateKey(&store.APIKeyEntry{Name: "to-delete", Status: "active"})
        h2 := newTestHandler(t, ms2, auth, "")
        req := makeAuthenticatedRequest(t, auth, http.MethodDelete, "/admin/api/keys/to-delete", nil)
        rec := httptest.NewRecorder()
        h2.handleKeyByID(rec, req)
        if rec.Code != http.StatusNoContent {
            t.Fatalf("expected 204, got %d", rec.Code)
        }
    })

    t.Run("delete_key_not_found", func(t *testing.T) {
        req := makeAuthenticatedRequest(t, auth, http.MethodDelete, "/admin/api/keys/nonexistent", nil)
        rec := httptest.NewRecorder()
        h.handleKeyByID(rec, req)
        if rec.Code != http.StatusNotFound {
            t.Fatalf("expected 404, got %d", rec.Code)
        }
    })

    t.Run("empty_name_returns_400", func(t *testing.T) {
        req := makeAuthenticatedRequest(t, auth, http.MethodGet, "/admin/api/keys/", nil)
        rec := httptest.NewRecorder()
        h.handleKeyByID(rec, req)
        if rec.Code != http.StatusBadRequest {
            t.Fatalf("expected 400, got %d", rec.Code)
        }
    })

    t.Run("update_key_invalid_json", func(t *testing.T) {
        req := makeAuthenticatedRequest(t, auth, http.MethodPut, "/admin/api/keys/existing-key", nil)
        req.Body = io.NopCloser(bytes.NewReader([]byte("not-json")))
        rec := httptest.NewRecorder()
        h.handleKeyByID(rec, req)
        if rec.Code != http.StatusBadRequest {
            t.Fatalf("expected 400, got %d", rec.Code)
        }
    })

    t.Run("method_not_allowed", func(t *testing.T) {
        req := makeAuthenticatedRequest(t, auth, http.MethodPatch, "/admin/api/keys/existing-key", nil)
        rec := httptest.NewRecorder()
        h.handleKeyByID(rec, req)
        if rec.Code != http.StatusMethodNotAllowed {
            t.Fatalf("expected 405, got %d", rec.Code)
        }
    })
}

func TestHandleChannels(t *testing.T) {
    t.Parallel()

    auth := newTestAuth(t)
    ms := newMockStore()
    h := newTestHandler(t, ms, auth, "")

    t.Run("list_channels_empty", func(t *testing.T) {
        req := makeAuthenticatedRequest(t, auth, http.MethodGet, "/admin/api/channels", nil)
        rec := httptest.NewRecorder()
        h.handleChannels(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d", rec.Code)
        }
        arr := decodeResponseArray(t, rec)
        if len(arr) != 0 {
            t.Fatalf("expected empty array, got %d items", len(arr))
        }
    })

    t.Run("create_channel_success", func(t *testing.T) {
        body := map[string]interface{}{
            "name":     "local-mlx",
            "type":     "local",
            "base_url": "http://localhost:11434",
            "key":      "",
            "models":   []string{"qwen3"},
            "status":   "active",
            "priority": 1,
            "weight":   10,
        }
        req := makeAuthenticatedRequest(t, auth, http.MethodPost, "/admin/api/channels", body)
        rec := httptest.NewRecorder()
        h.handleChannels(rec, req)
        if rec.Code != http.StatusCreated {
            t.Fatalf("expected 201, got %d; body: %s", rec.Code, rec.Body.String())
        }
    })

    t.Run("list_channels_after_create", func(t *testing.T) {
        req := makeAuthenticatedRequest(t, auth, http.MethodGet, "/admin/api/channels", nil)
        rec := httptest.NewRecorder()
        h.handleChannels(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d", rec.Code)
        }
        arr := decodeResponseArray(t, rec)
        if len(arr) != 1 {
            t.Fatalf("expected 1 channel, got %d", len(arr))
        }
    })

    t.Run("create_channel_missing_name", func(t *testing.T) {
        body := map[string]interface{}{
            "type": "local",
        }
        req := makeAuthenticatedRequest(t, auth, http.MethodPost, "/admin/api/channels", body)
        rec := httptest.NewRecorder()
        h.handleChannels(rec, req)
        if rec.Code != http.StatusBadRequest {
            t.Fatalf("expected 400, got %d", rec.Code)
        }
    })

    t.Run("create_channel_duplicate_name", func(t *testing.T) {
        body := map[string]interface{}{
            "name": "local-mlx",
            "type": "local",
        }
        req := makeAuthenticatedRequest(t, auth, http.MethodPost, "/admin/api/channels", body)
        rec := httptest.NewRecorder()
        h.handleChannels(rec, req)
        if rec.Code != http.StatusConflict {
            t.Fatalf("expected 409, got %d", rec.Code)
        }
    })

    t.Run("create_channel_invalid_json", func(t *testing.T) {
        req := makeAuthenticatedRequest(t, auth, http.MethodPost, "/admin/api/channels", nil)
        req.Body = io.NopCloser(bytes.NewReader([]byte("bad-json")))
        rec := httptest.NewRecorder()
        h.handleChannels(rec, req)
        if rec.Code != http.StatusBadRequest {
            t.Fatalf("expected 400, got %d", rec.Code)
        }
    })

    t.Run("method_not_allowed", func(t *testing.T) {
        req := makeAuthenticatedRequest(t, auth, http.MethodPatch, "/admin/api/channels", nil)
        rec := httptest.NewRecorder()
        h.handleChannels(rec, req)
        if rec.Code != http.StatusMethodNotAllowed {
            t.Fatalf("expected 405, got %d", rec.Code)
        }
    })
}

func TestHandleChannelByID(t *testing.T) {
    t.Parallel()

    auth := newTestAuth(t)
    ms := newMockStore()
    _ = ms.CreateChannel(&store.ChannelEntry{Name: "local-mlx", Type: "local", Status: "active"})
    h := newTestHandler(t, ms, auth, "")

    t.Run("get_channel_success", func(t *testing.T) {
        req := makeAuthenticatedRequest(t, auth, http.MethodGet, "/admin/api/channels/local-mlx", nil)
        rec := httptest.NewRecorder()
        h.handleChannelByID(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
        }
    })

    t.Run("get_channel_not_found", func(t *testing.T) {
        req := makeAuthenticatedRequest(t, auth, http.MethodGet, "/admin/api/channels/nonexistent", nil)
        rec := httptest.NewRecorder()
        h.handleChannelByID(rec, req)
        if rec.Code != http.StatusNotFound {
            t.Fatalf("expected 404, got %d", rec.Code)
        }
    })

    t.Run("update_channel_success", func(t *testing.T) {
        body := map[string]interface{}{
            "name":     "local-mlx",
            "type":     "local",
            "base_url": "http://localhost:11444",
            "status":   "disabled",
            "priority": 2,
            "weight":   5,
        }
        req := makeAuthenticatedRequest(t, auth, http.MethodPut, "/admin/api/channels/local-mlx", body)
        rec := httptest.NewRecorder()
        h.handleChannelByID(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
        }
    })

    t.Run("update_channel_not_found", func(t *testing.T) {
        body := map[string]interface{}{
            "name": "nonexistent",
            "type": "local",
        }
        req := makeAuthenticatedRequest(t, auth, http.MethodPut, "/admin/api/channels/nonexistent", body)
        rec := httptest.NewRecorder()
        h.handleChannelByID(rec, req)
        if rec.Code != http.StatusNotFound {
            t.Fatalf("expected 404, got %d", rec.Code)
        }
    })

    t.Run("delete_channel_success", func(t *testing.T) {
        ms2 := newMockStore()
        _ = ms2.CreateChannel(&store.ChannelEntry{Name: "to-delete", Type: "local", Status: "active"})
        h2 := newTestHandler(t, ms2, auth, "")
        req := makeAuthenticatedRequest(t, auth, http.MethodDelete, "/admin/api/channels/to-delete", nil)
        rec := httptest.NewRecorder()
        h2.handleChannelByID(rec, req)
        if rec.Code != http.StatusNoContent {
            t.Fatalf("expected 204, got %d", rec.Code)
        }
    })

    t.Run("delete_channel_not_found", func(t *testing.T) {
        req := makeAuthenticatedRequest(t, auth, http.MethodDelete, "/admin/api/channels/nonexistent", nil)
        rec := httptest.NewRecorder()
        h.handleChannelByID(rec, req)
        if rec.Code != http.StatusNotFound {
            t.Fatalf("expected 404, got %d", rec.Code)
        }
    })

    t.Run("empty_name_returns_400", func(t *testing.T) {
        req := makeAuthenticatedRequest(t, auth, http.MethodGet, "/admin/api/channels/", nil)
        rec := httptest.NewRecorder()
        h.handleChannelByID(rec, req)
        if rec.Code != http.StatusBadRequest {
            t.Fatalf("expected 400, got %d", rec.Code)
        }
    })

    t.Run("update_channel_invalid_json", func(t *testing.T) {
        req := makeAuthenticatedRequest(t, auth, http.MethodPut, "/admin/api/channels/local-mlx", nil)
        req.Body = io.NopCloser(bytes.NewReader([]byte("not-json")))
        rec := httptest.NewRecorder()
        h.handleChannelByID(rec, req)
        if rec.Code != http.StatusBadRequest {
            t.Fatalf("expected 400, got %d", rec.Code)
        }
    })

    t.Run("method_not_allowed", func(t *testing.T) {
        req := makeAuthenticatedRequest(t, auth, http.MethodPatch, "/admin/api/channels/local-mlx", nil)
        rec := httptest.NewRecorder()
        h.handleChannelByID(rec, req)
        if rec.Code != http.StatusMethodNotAllowed {
            t.Fatalf("expected 405, got %d", rec.Code)
        }
    })
}

func TestHandleLogs(t *testing.T) {
    t.Parallel()

    auth := newTestAuth(t)
    ms := newMockStore()
    _ = ms.AppendLog(&store.RequestLog{ID: "1", Model: "qwen3", StatusCode: 200, InputTokens: 10, OutputTokens: 20, TotalTokens: 30, Cost: 0.01, Latency: 50})
    h := newTestHandler(t, ms, auth, "")

    t.Run("get_logs_success", func(t *testing.T) {
        req := makeAuthenticatedRequest(t, auth, http.MethodGet, "/admin/api/logs", nil)
        rec := httptest.NewRecorder()
        h.handleLogs(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d", rec.Code)
        }
        result := decodeResponse(t, rec)
        if result["total"] == nil {
            t.Fatal("expected total field in response")
        }
    })

    t.Run("method_not_allowed", func(t *testing.T) {
        req := makeAuthenticatedRequest(t, auth, http.MethodPost, "/admin/api/logs", nil)
        rec := httptest.NewRecorder()
        h.handleLogs(rec, req)
        if rec.Code != http.StatusMethodNotAllowed {
            t.Fatalf("expected 405, got %d", rec.Code)
        }
    })
}

func TestHandleLogsExport(t *testing.T) {
    t.Parallel()

    auth := newTestAuth(t)
    ms := newMockStore()
    _ = ms.AppendLog(&store.RequestLog{ID: "1", Model: "qwen3", StatusCode: 200})
    h := newTestHandler(t, ms, auth, "")

    t.Run("export_json_default", func(t *testing.T) {
        req := makeAuthenticatedRequest(t, auth, http.MethodGet, "/admin/api/logs/export", nil)
        rec := httptest.NewRecorder()
        h.handleLogsExport(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d", rec.Code)
        }
        ct := rec.Header().Get("Content-Type")
        if ct != "application/json" {
            t.Fatalf("expected application/json, got %q", ct)
        }
    })

    t.Run("export_csv", func(t *testing.T) {
        req := makeAuthenticatedRequest(t, auth, http.MethodGet, "/admin/api/logs/export?format=csv", nil)
        rec := httptest.NewRecorder()
        h.handleLogsExport(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d", rec.Code)
        }
        ct := rec.Header().Get("Content-Type")
        if ct != "text/csv" {
            t.Fatalf("expected text/csv, got %q", ct)
        }
    })

    t.Run("export_invalid_format", func(t *testing.T) {
        req := makeAuthenticatedRequest(t, auth, http.MethodGet, "/admin/api/logs/export?format=xml", nil)
        rec := httptest.NewRecorder()
        h.handleLogsExport(rec, req)
        if rec.Code != http.StatusBadRequest {
            t.Fatalf("expected 400, got %d", rec.Code)
        }
    })

    t.Run("export_json_explicit", func(t *testing.T) {
        req := makeAuthenticatedRequest(t, auth, http.MethodGet, "/admin/api/logs/export?format=json", nil)
        rec := httptest.NewRecorder()
        h.handleLogsExport(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d", rec.Code)
        }
        disp := rec.Header().Get("Content-Disposition")
        if !strings.Contains(disp, "logs.json") {
            t.Fatalf("expected logs.json in Content-Disposition, got %q", disp)
        }
    })

    t.Run("method_not_allowed", func(t *testing.T) {
        req := makeAuthenticatedRequest(t, auth, http.MethodPost, "/admin/api/logs/export", nil)
        rec := httptest.NewRecorder()
        h.handleLogsExport(rec, req)
        if rec.Code != http.StatusMethodNotAllowed {
            t.Fatalf("expected 405, got %d", rec.Code)
        }
    })
}

func TestHandleAnalyticsOverview(t *testing.T) {
    t.Parallel()

    auth := newTestAuth(t)
    ms := newMockStore()
    _ = ms.AppendLog(&store.RequestLog{
        ID: "1", Model: "qwen3", StatusCode: 200,
        InputTokens: 100, OutputTokens: 50, TotalTokens: 150,
        Cost: 0.05, Latency: 80, ChannelType: "local",
    })
    _ = ms.AppendLog(&store.RequestLog{
        ID: "2", Model: "gpt4", StatusCode: 500,
        InputTokens: 200, OutputTokens: 100, TotalTokens: 300,
        Cost: 0.1, Latency: 200, ChannelType: "cloud",
    })
    h := newTestHandler(t, ms, auth, "")

    req := makeAuthenticatedRequest(t, auth, http.MethodGet, "/admin/api/analytics", nil)
    rec := httptest.NewRecorder()
    h.handleAnalyticsOverview(rec, req)
    if rec.Code != http.StatusOK {
        t.Fatalf("expected 200, got %d", rec.Code)
    }
    result := decodeResponse(t, rec)
    if result["token"] == nil {
        t.Fatal("expected token section")
    }
    if result["cost"] == nil {
        t.Fatal("expected cost section")
    }
    if result["model"] == nil {
        t.Fatal("expected model section")
    }
    if result["latency"] == nil {
        t.Fatal("expected latency section")
    }
    if result["error"] == nil {
        t.Fatal("expected error section")
    }
}

func TestAnalyticsOverview_EmptyLogs(t *testing.T) {
    t.Parallel()

    auth := newTestAuth(t)
    ms := newMockStore()
    h := newTestHandler(t, ms, auth, "")

    req := makeAuthenticatedRequest(t, auth, http.MethodGet, "/admin/api/analytics", nil)
    rec := httptest.NewRecorder()
    h.handleAnalyticsOverview(rec, req)
    if rec.Code != http.StatusOK {
        t.Fatalf("expected 200, got %d", rec.Code)
    }
}

func TestAnalyticsOverview_WithErrors(t *testing.T) {
    t.Parallel()

    auth := newTestAuth(t)
    ms := newMockStore()
    _ = ms.AppendLog(&store.RequestLog{
        ID: "1", Model: "qwen3", StatusCode: 500,
        InputTokens: 10, OutputTokens: 5, TotalTokens: 15,
        Cost: 0.01, Latency: 100, ChannelType: "cloud",
    })
    _ = ms.AppendLog(&store.RequestLog{
        ID: "2", Model: "gpt4", StatusCode: 429,
        InputTokens: 20, OutputTokens: 10, TotalTokens: 30,
        Cost: 0.02, Latency: 200, ChannelType: "cloud",
    })
    h := newTestHandler(t, ms, auth, "")

    req := makeAuthenticatedRequest(t, auth, http.MethodGet, "/admin/api/analytics", nil)
    rec := httptest.NewRecorder()
    h.handleAnalyticsOverview(rec, req)
    if rec.Code != http.StatusOK {
        t.Fatalf("expected 200, got %d", rec.Code)
    }
}

func TestAnalyticsOverview_TimeRangeParams(t *testing.T) {
    t.Parallel()

    auth := newTestAuth(t)
    ms := newMockStore()
    _ = ms.AppendLog(&store.RequestLog{ID: "1", Model: "qwen3", StatusCode: 200, InputTokens: 10, OutputTokens: 5, TotalTokens: 15, Cost: 0.01, Latency: 50, ChannelType: "local"})
    h := newTestHandler(t, ms, auth, "")

    from := time.Now().AddDate(0, 0, -1).UTC().Format(time.RFC3339)
    to := time.Now().UTC().Format(time.RFC3339)
    url := fmt.Sprintf("/admin/api/analytics?from=%s&to=%s", from, to)
    req := makeAuthenticatedRequest(t, auth, http.MethodGet, url, nil)
    rec := httptest.NewRecorder()
    h.handleAnalyticsOverview(rec, req)
    if rec.Code != http.StatusOK {
        t.Fatalf("expected 200, got %d", rec.Code)
    }
}

func TestHandleTokenStats(t *testing.T) {
    t.Parallel()

    auth := newTestAuth(t)
    ms := newMockStore()
    h := newTestHandler(t, ms, auth, "")

    t.Run("default_group_by", func(t *testing.T) {
        req := makeAuthenticatedRequest(t, auth, http.MethodGet, "/admin/api/analytics/tokens", nil)
        rec := httptest.NewRecorder()
        h.handleTokenStats(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d", rec.Code)
        }
    })

    t.Run("valid_group_by", func(t *testing.T) {
        req := makeAuthenticatedRequest(t, auth, http.MethodGet, "/admin/api/analytics/tokens?group_by=model", nil)
        rec := httptest.NewRecorder()
        h.handleTokenStats(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d", rec.Code)
        }
    })

    t.Run("invalid_group_by", func(t *testing.T) {
        req := makeAuthenticatedRequest(t, auth, http.MethodGet, "/admin/api/analytics/tokens?group_by=invalid", nil)
        rec := httptest.NewRecorder()
        h.handleTokenStats(rec, req)
        if rec.Code != http.StatusBadRequest {
            t.Fatalf("expected 400, got %d", rec.Code)
        }
    })
}

func TestHandleCostStats(t *testing.T) {
    t.Parallel()

    auth := newTestAuth(t)
    ms := newMockStore()
    h := newTestHandler(t, ms, auth, "")

    t.Run("default_group_by", func(t *testing.T) {
        req := makeAuthenticatedRequest(t, auth, http.MethodGet, "/admin/api/analytics/cost", nil)
        rec := httptest.NewRecorder()
        h.handleCostStats(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d", rec.Code)
        }
    })

    t.Run("invalid_group_by", func(t *testing.T) {
        req := makeAuthenticatedRequest(t, auth, http.MethodGet, "/admin/api/analytics/cost?group_by=bogus", nil)
        rec := httptest.NewRecorder()
        h.handleCostStats(rec, req)
        if rec.Code != http.StatusBadRequest {
            t.Fatalf("expected 400, got %d", rec.Code)
        }
    })
}

func TestHandleModelStats(t *testing.T) {
    t.Parallel()

    auth := newTestAuth(t)
    ms := newMockStore()
    h := newTestHandler(t, ms, auth, "")

    req := makeAuthenticatedRequest(t, auth, http.MethodGet, "/admin/api/analytics/models", nil)
    rec := httptest.NewRecorder()
    h.handleModelStats(rec, req)
    if rec.Code != http.StatusOK {
        t.Fatalf("expected 200, got %d", rec.Code)
    }
}

func TestHandleLatencyStats(t *testing.T) {
    t.Parallel()

    auth := newTestAuth(t)
    ms := newMockStore()
    h := newTestHandler(t, ms, auth, "")

    req := makeAuthenticatedRequest(t, auth, http.MethodGet, "/admin/api/analytics/latency", nil)
    rec := httptest.NewRecorder()
    h.handleLatencyStats(rec, req)
    if rec.Code != http.StatusOK {
        t.Fatalf("expected 200, got %d", rec.Code)
    }
}

func TestHandleErrorStats(t *testing.T) {
    t.Parallel()

    auth := newTestAuth(t)
    ms := newMockStore()
    h := newTestHandler(t, ms, auth, "")

    req := makeAuthenticatedRequest(t, auth, http.MethodGet, "/admin/api/analytics/errors", nil)
    rec := httptest.NewRecorder()
    h.handleErrorStats(rec, req)
    if rec.Code != http.StatusOK {
        t.Fatalf("expected 200, got %d", rec.Code)
    }
}

func TestHandleProfitStats(t *testing.T) {
    t.Parallel()

    auth := newTestAuth(t)
    ms := newMockStore()
    h := newTestHandler(t, ms, auth, "")

    req := makeAuthenticatedRequest(t, auth, http.MethodGet, "/admin/api/analytics/profit", nil)
    rec := httptest.NewRecorder()
    h.handleProfitStats(rec, req)
    if rec.Code != http.StatusOK {
        t.Fatalf("expected 200, got %d", rec.Code)
    }
    result := decodeResponse(t, rec)
    if result["summary"] == nil {
        t.Fatal("expected summary field")
    }
}

func TestProfitStats_WithZeroRatios(t *testing.T) {
    t.Parallel()

    auth := newTestAuth(t)
    ms := newMockStore()
    h := newTestHandler(t, ms, auth, "")

    req := makeAuthenticatedRequest(t, auth, http.MethodGet, "/admin/api/analytics/profit", nil)
    rec := httptest.NewRecorder()
    h.handleProfitStats(rec, req)
    if rec.Code != http.StatusOK {
        t.Fatalf("expected 200, got %d", rec.Code)
    }
}

func TestHandleDashboard(t *testing.T) {
    t.Parallel()

    auth := newTestAuth(t)
    ms := newMockStore()
    h := newTestHandler(t, ms, auth, "")

    t.Run("get_dashboard", func(t *testing.T) {
        req := makeAuthenticatedRequest(t, auth, http.MethodGet, "/admin/api/dashboard", nil)
        rec := httptest.NewRecorder()
        h.handleDashboard(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d", rec.Code)
        }
    })

    t.Run("method_not_allowed", func(t *testing.T) {
        req := makeAuthenticatedRequest(t, auth, http.MethodPost, "/admin/api/dashboard", nil)
        rec := httptest.NewRecorder()
        h.handleDashboard(rec, req)
        if rec.Code != http.StatusMethodNotAllowed {
            t.Fatalf("expected 405, got %d", rec.Code)
        }
    })
}

func TestHandleQuota(t *testing.T) {
    t.Parallel()

    auth := newTestAuth(t)
    ms := newMockStore()
    ms.quota["test-key"] = quotaInfo{used: 10, limit: 100, exceeded: false}
    h := newTestHandler(t, ms, auth, "")

    t.Run("check_quota_success", func(t *testing.T) {
        req := makeAuthenticatedRequest(t, auth, http.MethodGet, "/admin/api/quota/test-key", nil)
        rec := httptest.NewRecorder()
        h.handleQuota(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
        }
        result := decodeResponse(t, rec)
        if result["key_name"] != "test-key" {
            t.Fatalf("expected key_name test-key, got %v", result["key_name"])
        }
    })

    t.Run("check_quota_not_found", func(t *testing.T) {
        req := makeAuthenticatedRequest(t, auth, http.MethodGet, "/admin/api/quota/nonexistent", nil)
        rec := httptest.NewRecorder()
        h.handleQuota(rec, req)
        if rec.Code != http.StatusNotFound {
            t.Fatalf("expected 404, got %d", rec.Code)
        }
    })

    t.Run("deduct_quota_success", func(t *testing.T) {
        body := map[string]interface{}{"amount": 5.0}
        req := makeAuthenticatedRequest(t, auth, http.MethodPost, "/admin/api/quota/test-key", body)
        rec := httptest.NewRecorder()
        h.handleQuota(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
        }
    })

    t.Run("deduct_quota_invalid_json", func(t *testing.T) {
        req := makeAuthenticatedRequest(t, auth, http.MethodPost, "/admin/api/quota/test-key", nil)
        req.Body = io.NopCloser(bytes.NewReader([]byte("not-json")))
        rec := httptest.NewRecorder()
        h.handleQuota(rec, req)
        if rec.Code != http.StatusBadRequest {
            t.Fatalf("expected 400, got %d", rec.Code)
        }
    })

    t.Run("deduct_quota_not_found", func(t *testing.T) {
        body := map[string]interface{}{"amount": 5.0}
        req := makeAuthenticatedRequest(t, auth, http.MethodPost, "/admin/api/quota/nonexistent", body)
        rec := httptest.NewRecorder()
        h.handleQuota(rec, req)
        if rec.Code != http.StatusInternalServerError {
            t.Fatalf("expected 500, got %d", rec.Code)
        }
    })

    t.Run("empty_key_name_returns_400", func(t *testing.T) {
        req := makeAuthenticatedRequest(t, auth, http.MethodGet, "/admin/api/quota/", nil)
        rec := httptest.NewRecorder()
        h.handleQuota(rec, req)
        if rec.Code != http.StatusBadRequest {
            t.Fatalf("expected 400, got %d", rec.Code)
        }
    })

    t.Run("method_not_allowed", func(t *testing.T) {
        req := makeAuthenticatedRequest(t, auth, http.MethodPut, "/admin/api/quota/test-key", nil)
        rec := httptest.NewRecorder()
        h.handleQuota(rec, req)
        if rec.Code != http.StatusMethodNotAllowed {
            t.Fatalf("expected 405, got %d", rec.Code)
        }
    })
}

func TestHandleLogin(t *testing.T) {
    t.Parallel()

    auth := newTestAuth(t)

    t.Run("successful_login", func(t *testing.T) {
        body := map[string]string{"username": "admin", "password": "password123"}
        b, _ := json.Marshal(body)
        req := httptest.NewRequest(http.MethodPost, "/admin/api/login", bytes.NewReader(b))
        rec := httptest.NewRecorder()
        auth.HandleLogin(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
        }
        result := decodeResponse(t, rec)
        if result["token"] == nil {
            t.Fatal("expected token in response")
        }
        if result["username"] != "admin" {
            t.Fatalf("expected username admin, got %v", result["username"])
        }
        cookies := rec.Result().Cookies()
        found := false
        for _, c := range cookies {
            if c.Name == "admin_token" {
                found = true
                if c.Value == "" {
                    t.Fatal("expected non-empty admin_token cookie")
                }
            }
        }
        if !found {
            t.Fatal("expected admin_token cookie to be set")
        }
    })

    t.Run("wrong_password", func(t *testing.T) {
        body := map[string]string{"username": "admin", "password": "wrong"}
        b, _ := json.Marshal(body)
        req := httptest.NewRequest(http.MethodPost, "/admin/api/login", bytes.NewReader(b))
        rec := httptest.NewRecorder()
        auth.HandleLogin(rec, req)
        if rec.Code != http.StatusUnauthorized {
            t.Fatalf("expected 401, got %d", rec.Code)
        }
    })

    t.Run("invalid_json", func(t *testing.T) {
        req := httptest.NewRequest(http.MethodPost, "/admin/api/login", bytes.NewReader([]byte("not-json")))
        rec := httptest.NewRecorder()
        auth.HandleLogin(rec, req)
        if rec.Code != http.StatusBadRequest {
            t.Fatalf("expected 400, got %d", rec.Code)
        }
    })

    t.Run("method_not_allowed", func(t *testing.T) {
        req := httptest.NewRequest(http.MethodGet, "/admin/api/login", nil)
        rec := httptest.NewRecorder()
        auth.HandleLogin(rec, req)
        if rec.Code != http.StatusMethodNotAllowed {
            t.Fatalf("expected 405, got %d", rec.Code)
        }
    })

    t.Run("disabled_admin_module", func(t *testing.T) {
        disabledAuth := &AdminAuth{}
        req := httptest.NewRequest(http.MethodPost, "/admin/api/login", bytes.NewReader([]byte(`{"username":"a","password":"b"}`)))
        rec := httptest.NewRecorder()
        disabledAuth.HandleLogin(rec, req)
        if rec.Code != http.StatusServiceUnavailable {
            t.Fatalf("expected 503, got %d", rec.Code)
        }
    })
}

func TestHandleLogout(t *testing.T) {
    t.Parallel()

    auth := newTestAuth(t)

    t.Run("clears_cookie", func(t *testing.T) {
        req := httptest.NewRequest(http.MethodPost, "/admin/api/logout", nil)
        rec := httptest.NewRecorder()
        auth.HandleLogout(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
        }
        cookies := rec.Result().Cookies()
        found := false
        for _, c := range cookies {
            if c.Name == "admin_token" {
                found = true
                if c.MaxAge != -1 {
                    t.Fatalf("expected MaxAge -1 to expire cookie, got %d", c.MaxAge)
                }
                if !c.HttpOnly {
                    t.Fatal("expected HttpOnly cookie")
                }
                if !c.Secure {
                    t.Fatal("expected Secure cookie to match login handler")
                }
            }
        }
        if !found {
            t.Fatal("expected admin_token cookie to be expired/cleared")
        }
    })

    t.Run("method_not_allowed", func(t *testing.T) {
        req := httptest.NewRequest(http.MethodGet, "/admin/api/logout", nil)
        rec := httptest.NewRecorder()
        auth.HandleLogout(rec, req)
        if rec.Code != http.StatusMethodNotAllowed {
            t.Fatalf("expected 405, got %d", rec.Code)
        }
    })
}

func TestNormalizeStatus(t *testing.T) {
    t.Parallel()

    tests := []struct {
        input    interface{}
        expected string
    }{
        {float64(1), "active"},
        {float64(0), "disabled"},
        {float64(2), "disabled"},
        {"active", "active"},
        {"disabled", "disabled"},
        {nil, "active"},
        {true, "active"},
    }

    for i, tt := range tests {
        result := normalizeStatus(tt.input)
        if result != tt.expected {
            t.Errorf("test %d: normalizeStatus(%v) = %q, want %q", i, tt.input, result, tt.expected)
        }
    }
}

func TestStatusToNumber(t *testing.T) {
    t.Parallel()

    if statusToNumber("active") != 1 {
        t.Fatal("expected 1 for active")
    }
    if statusToNumber("enabled") != 1 {
        t.Fatal("expected 1 for enabled")
    }
    if statusToNumber("disabled") != 0 {
        t.Fatal("expected 0 for disabled")
    }
    if statusToNumber("anything") != 0 {
        t.Fatal("expected 0 for anything else")
    }
}

func TestIsValidGroupBy(t *testing.T) {
    t.Parallel()

    valid := []string{"hour", "day", "week", "month", "model", "backend", "key"}
    for _, v := range valid {
        if !isValidGroupBy(v) {
            t.Fatalf("expected %q to be valid", v)
        }
    }

    invalid := []string{"", "year", "invalid", "DAY"}
    for _, v := range invalid {
        if isValidGroupBy(v) {
            t.Fatalf("expected %q to be invalid", v)
        }
    }
}

func TestParseLogFilter(t *testing.T) {
    t.Parallel()

    t.Run("default_values", func(t *testing.T) {
        req := httptest.NewRequest(http.MethodGet, "/admin/api/logs", nil)
        filter := parseLogFilter(req)
        if filter.Page != 1 {
            t.Fatalf("expected page 1, got %d", filter.Page)
        }
        if filter.PageSize != 50 {
            t.Fatalf("expected page_size 50, got %d", filter.PageSize)
        }
    })

    t.Run("custom_pagination", func(t *testing.T) {
        req := httptest.NewRequest(http.MethodGet, "/admin/api/logs?page=3&limit=100", nil)
        filter := parseLogFilter(req)
        if filter.Page != 3 {
            t.Fatalf("expected page 3, got %d", filter.Page)
        }
        if filter.PageSize != 100 {
            t.Fatalf("expected page_size 100, got %d", filter.PageSize)
        }
    })

    t.Run("limit_capped_at_500", func(t *testing.T) {
        req := httptest.NewRequest(http.MethodGet, "/admin/api/logs?limit=1000", nil)
        filter := parseLogFilter(req)
        if filter.PageSize != 50 {
            t.Fatalf("expected page_size 50 (default), got %d", filter.PageSize)
        }
    })

    t.Run("filter_params", func(t *testing.T) {
        req := httptest.NewRequest(http.MethodGet, "/admin/api/logs?key_name=mykey&model=qwen3&channel=local&status=success&min_tokens=100&min_cost=0.01", nil)
        filter := parseLogFilter(req)
        if filter.KeyName != "mykey" {
            t.Fatalf("expected key_name mykey, got %q", filter.KeyName)
        }
        if filter.Model != "qwen3" {
            t.Fatalf("expected model qwen3, got %q", filter.Model)
        }
        if filter.Channel != "local" {
            t.Fatalf("expected channel local, got %q", filter.Channel)
        }
        if filter.Status != "success" {
            t.Fatalf("expected status success, got %q", filter.Status)
        }
        if filter.MinTokens != 100 {
            t.Fatalf("expected min_tokens 100, got %d", filter.MinTokens)
        }
        if filter.MinCost != 0.01 {
            t.Fatalf("expected min_cost 0.01, got %f", filter.MinCost)
        }
    })

    t.Run("time_range", func(t *testing.T) {
        from := time.Now().AddDate(0, 0, -1).UTC().Format(time.RFC3339)
        to := time.Now().UTC().Format(time.RFC3339)
        req := httptest.NewRequest(http.MethodGet, "/admin/api/logs?from="+from+"&to="+to, nil)
        filter := parseLogFilter(req)
        if filter.StartTime == nil {
            t.Fatal("expected start_time to be set")
        }
        if filter.EndTime == nil {
            t.Fatal("expected end_time to be set")
        }
    })

    t.Run("invalid_time_ignored", func(t *testing.T) {
        req := httptest.NewRequest(http.MethodGet, "/admin/api/logs?from=not-a-time&to=also-bad", nil)
        filter := parseLogFilter(req)
        if filter.StartTime != nil {
            t.Fatal("expected start_time to be nil for invalid time")
        }
        if filter.EndTime != nil {
            t.Fatal("expected end_time to be nil for invalid time")
        }
    })

    t.Run("invalid_page_uses_default", func(t *testing.T) {
        req := httptest.NewRequest(http.MethodGet, "/admin/api/logs?page=0&limit=-1", nil)
        filter := parseLogFilter(req)
        if filter.Page != 1 {
            t.Fatalf("expected page 1, got %d", filter.Page)
        }
        if filter.PageSize != 50 {
            t.Fatalf("expected page_size 50, got %d", filter.PageSize)
        }
    })
}

func TestParseTimeRange(t *testing.T) {
    t.Parallel()

    t.Run("default_range", func(t *testing.T) {
        req := httptest.NewRequest(http.MethodGet, "/", nil)
        from, to := parseTimeRange(req)
        if from.After(to) {
            t.Fatal("from should be before to")
        }
    })

    t.Run("custom_range", func(t *testing.T) {
        now := time.Now()
        fromStr := now.AddDate(0, 0, -3).UTC().Format(time.RFC3339)
        toStr := now.UTC().Format(time.RFC3339)
        req := httptest.NewRequest(http.MethodGet, "/?from="+fromStr+"&to="+toStr, nil)
        from, to := parseTimeRange(req)
        if from.After(to) {
            t.Fatal("from should be before to")
        }
    })

    t.Run("invalid_time_uses_default", func(t *testing.T) {
        req := httptest.NewRequest(http.MethodGet, "/?from=invalid&to=also-invalid", nil)
        from, _ := parseTimeRange(req)
        if from.IsZero() {
            t.Fatal("from should have default value even with invalid input")
        }
    })
}

func TestWriteJSON(t *testing.T) {
    t.Parallel()

    rec := httptest.NewRecorder()
    writeJSON(rec, http.StatusOK, map[string]string{"hello": "world"})
    if rec.Code != http.StatusOK {
        t.Fatalf("expected 200, got %d", rec.Code)
    }
    ct := rec.Header().Get("Content-Type")
    if ct != "application/json" {
        t.Fatalf("expected application/json, got %q", ct)
    }
}

func TestWriteJSON_NilData(t *testing.T) {
    t.Parallel()

    rec := httptest.NewRecorder()
    writeJSON(rec, http.StatusNoContent, nil)
    if rec.Code != http.StatusNoContent {
        t.Fatalf("expected 204, got %d", rec.Code)
    }
}

func TestWriteError(t *testing.T) {
    t.Parallel()

    rec := httptest.NewRecorder()
    writeError(rec, http.StatusBadRequest, "test error")
    if rec.Code != http.StatusBadRequest {
        t.Fatalf("expected 400, got %d", rec.Code)
    }
    result := decodeResponse(t, rec)
    errObj, ok := result["error"].(map[string]interface{})
    if !ok {
        t.Fatal("expected error object in response")
    }
    if errObj["message"] != "test error" {
        t.Fatalf("expected message 'test error', got %v", errObj["message"])
    }
}

func TestMaskAPIKey(t *testing.T) {
    t.Parallel()

    if maskAPIKey("") != "" {
        t.Fatal("expected empty string for empty key")
    }
    if maskAPIKey("abc") != "****" {
        t.Fatal("expected **** for short key")
    }
    if maskAPIKey("sk-1234567890abcdef") != "****cdef" {
        t.Fatalf("expected ****cdef, got %q", maskAPIKey("sk-1234567890abcdef"))
    }
}

func TestHandleRoutingConfig(t *testing.T) {
    auth := newTestAuth(t)
    ms := newMockStore()

    defaultYAML := `
server:
  host: "0.0.0.0"
  port: 8100
routing:
  token_threshold: 8000
  output_input_ratio_threshold: 0.6
  local_priority:
    enabled: true
    max_system_memory_ratio: 0.9
    max_mlx_memory_ratio: 0.7
    max_concurrent: 8
  circuit_breaker:
    failure_threshold: 5
  fallback:
    enabled: false
    cloud_default: "openai"
  ratio_tiers:
    enabled: false
    rules: []
  token_tiers:
    enabled: false
    metric: "total"
    rules: []
`
    loadTestConfig(t, defaultYAML)

    t.Run("get_routing_config", func(t *testing.T) {
        h := newTestHandler(t, ms, auth, "")
        req := makeAuthenticatedRequest(t, auth, http.MethodGet, "/admin/api/config/routing", nil)
        rec := httptest.NewRecorder()
        h.handleRoutingConfig(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
        }
    })

    t.Run("method_not_allowed", func(t *testing.T) {
        h := newTestHandler(t, ms, auth, "")
        req := makeAuthenticatedRequest(t, auth, http.MethodDelete, "/admin/api/config/routing", nil)
        rec := httptest.NewRecorder()
        h.handleRoutingConfig(rec, req)
        if rec.Code != http.StatusMethodNotAllowed {
            t.Fatalf("expected 405, got %d", rec.Code)
        }
    })

    t.Run("put_routing_config_no_config_path", func(t *testing.T) {
        h := newTestHandler(t, ms, auth, "")
        body := map[string]interface{}{"token_threshold": 5000}
        req := makeAuthenticatedRequest(t, auth, http.MethodPut, "/admin/api/config/routing", body)
        rec := httptest.NewRecorder()
        h.handleRoutingConfig(rec, req)
        if rec.Code != http.StatusInternalServerError {
            t.Fatalf("expected 500, got %d", rec.Code)
        }
    })

    t.Run("update_token_threshold", func(t *testing.T) {
        tmpDir := t.TempDir()
        configPath := filepath.Join(tmpDir, "config.yaml")
        if err := os.WriteFile(configPath, []byte(defaultYAML), 0644); err != nil {
            t.Fatalf("failed to write config file: %v", err)
        }
        loadTestConfig(t, defaultYAML)

        h := newTestHandler(t, ms, auth, configPath)
        body := map[string]interface{}{"token_threshold": 4000}
        req := makeAuthenticatedRequest(t, auth, http.MethodPut, "/admin/api/config/routing", body)
        rec := httptest.NewRecorder()
        h.handleRoutingConfig(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
        }
    })

    t.Run("update_ratio_threshold", func(t *testing.T) {
        tmpDir := t.TempDir()
        configPath := filepath.Join(tmpDir, "config.yaml")
        if err := os.WriteFile(configPath, []byte(defaultYAML), 0644); err != nil {
            t.Fatalf("failed to write config file: %v", err)
        }
        loadTestConfig(t, defaultYAML)

        h := newTestHandler(t, ms, auth, configPath)
        body := map[string]interface{}{"output_input_ratio_threshold": 0.8}
        req := makeAuthenticatedRequest(t, auth, http.MethodPut, "/admin/api/config/routing", body)
        rec := httptest.NewRecorder()
        h.handleRoutingConfig(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
        }
    })

    t.Run("update_default_model", func(t *testing.T) {
        tmpDir := t.TempDir()
        configPath := filepath.Join(tmpDir, "config.yaml")
        if err := os.WriteFile(configPath, []byte(defaultYAML), 0644); err != nil {
            t.Fatalf("failed to write config file: %v", err)
        }
        loadTestConfig(t, defaultYAML)

        h := newTestHandler(t, ms, auth, configPath)
        body := map[string]interface{}{"default_model": "qwen2.5-7b"}
        req := makeAuthenticatedRequest(t, auth, http.MethodPut, "/admin/api/config/routing", body)
        rec := httptest.NewRecorder()
        h.handleRoutingConfig(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
        }

        // Confirm the value persisted to the config file on disk.
        written, err := os.ReadFile(configPath)
        if err != nil {
            t.Fatalf("failed to read config file: %v", err)
        }
        var doc map[string]interface{}
        if err := yaml.Unmarshal(written, &doc); err != nil {
            t.Fatalf("failed to parse written yaml: %v", err)
        }
        routing, _ := doc["routing"].(map[string]interface{})
        if routing == nil || routing["default_model"] != "qwen2.5-7b" {
            t.Fatalf("expected routing.default_model=qwen2.5-7b in file, got %v", routing)
        }
    })

    t.Run("update_local_priority", func(t *testing.T) {
        tmpDir := t.TempDir()
        configPath := filepath.Join(tmpDir, "config.yaml")
        if err := os.WriteFile(configPath, []byte(defaultYAML), 0644); err != nil {
            t.Fatalf("failed to write config file: %v", err)
        }
        loadTestConfig(t, defaultYAML)

        h := newTestHandler(t, ms, auth, configPath)
        body := map[string]interface{}{
            "local_priority_enabled":  false,
            "max_system_memory_ratio": 0.5,
            "max_mlx_memory_ratio":    0.4,
            "max_concurrent":          4,
        }
        req := makeAuthenticatedRequest(t, auth, http.MethodPut, "/admin/api/config/routing", body)
        rec := httptest.NewRecorder()
        h.handleRoutingConfig(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
        }
    })

    t.Run("update_circuit_breaker_enable", func(t *testing.T) {
        tmpDir := t.TempDir()
        configPath := filepath.Join(tmpDir, "config.yaml")
        if err := os.WriteFile(configPath, []byte(defaultYAML), 0644); err != nil {
            t.Fatalf("failed to write config file: %v", err)
        }
        loadTestConfig(t, defaultYAML)

        h := newTestHandler(t, ms, auth, configPath)
        body := map[string]interface{}{"circuit_breaker_enabled": true}
        req := makeAuthenticatedRequest(t, auth, http.MethodPut, "/admin/api/config/routing", body)
        rec := httptest.NewRecorder()
        h.handleRoutingConfig(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
        }
    })

    t.Run("update_circuit_breaker_disable", func(t *testing.T) {
        tmpDir := t.TempDir()
        configPath := filepath.Join(tmpDir, "config.yaml")
        if err := os.WriteFile(configPath, []byte(defaultYAML), 0644); err != nil {
            t.Fatalf("failed to write config file: %v", err)
        }
        loadTestConfig(t, defaultYAML)

        h := newTestHandler(t, ms, auth, configPath)
        body := map[string]interface{}{"circuit_breaker_enabled": false}
        req := makeAuthenticatedRequest(t, auth, http.MethodPut, "/admin/api/config/routing", body)
        rec := httptest.NewRecorder()
        h.handleRoutingConfig(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
        }
    })

    t.Run("update_fallback", func(t *testing.T) {
        tmpDir := t.TempDir()
        configPath := filepath.Join(tmpDir, "config.yaml")
        if err := os.WriteFile(configPath, []byte(defaultYAML), 0644); err != nil {
            t.Fatalf("failed to write config file: %v", err)
        }
        loadTestConfig(t, defaultYAML)

        h := newTestHandler(t, ms, auth, configPath)
        body := map[string]interface{}{
            "fallback_enabled":       true,
            "fallback_cloud_default": "volcengine",
        }
        req := makeAuthenticatedRequest(t, auth, http.MethodPut, "/admin/api/config/routing", body)
        rec := httptest.NewRecorder()
        h.handleRoutingConfig(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
        }
    })

    t.Run("update_ratio_tiers", func(t *testing.T) {
        tmpDir := t.TempDir()
        configPath := filepath.Join(tmpDir, "config.yaml")
        if err := os.WriteFile(configPath, []byte(defaultYAML), 0644); err != nil {
            t.Fatalf("failed to write config file: %v", err)
        }
        loadTestConfig(t, defaultYAML)

        h := newTestHandler(t, ms, auth, configPath)
        body := map[string]interface{}{
            "ratio_tiers_enabled": true,
            "ratio_tiers_rules": []map[string]interface{}{
                {"max_ratio": 0.5, "backend": "local"},
                {"max_ratio": 2.0, "backend": "cloud"},
            },
        }
        req := makeAuthenticatedRequest(t, auth, http.MethodPut, "/admin/api/config/routing", body)
        rec := httptest.NewRecorder()
        h.handleRoutingConfig(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
        }
    })

    t.Run("update_token_tiers", func(t *testing.T) {
        tmpDir := t.TempDir()
        configPath := filepath.Join(tmpDir, "config.yaml")
        if err := os.WriteFile(configPath, []byte(defaultYAML), 0644); err != nil {
            t.Fatalf("failed to write config file: %v", err)
        }
        loadTestConfig(t, defaultYAML)

        h := newTestHandler(t, ms, auth, configPath)
        body := map[string]interface{}{
            "token_tiers_enabled": true,
            "token_tiers_metric":  "input",
            "token_tiers_rules": []map[string]interface{}{
                {"max_tokens": 500, "backend": "local"},
                {"max_tokens": 8000, "backend": "cloud"},
            },
        }
        req := makeAuthenticatedRequest(t, auth, http.MethodPut, "/admin/api/config/routing", body)
        rec := httptest.NewRecorder()
        h.handleRoutingConfig(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
        }
    })

    t.Run("update_invalid_json", func(t *testing.T) {
        h := newTestHandler(t, ms, auth, "/dummy/path")
        req := makeAuthenticatedRequest(t, auth, http.MethodPut, "/admin/api/config/routing", nil)
        req.Body = io.NopCloser(bytes.NewReader([]byte("not-json")))
        rec := httptest.NewRecorder()
        h.handleRoutingConfig(rec, req)
        if rec.Code != http.StatusBadRequest {
            t.Fatalf("expected 400, got %d", rec.Code)
        }
    })

    t.Run("update_invalid_config_file", func(t *testing.T) {
        tmpDir := t.TempDir()
        configPath := filepath.Join(tmpDir, "bad-config.yaml")
        if err := os.WriteFile(configPath, []byte("not: [valid: yaml"), 0644); err != nil {
            t.Fatalf("failed to write config: %v", err)
        }
        loadTestConfig(t, defaultYAML)

        h := newTestHandler(t, ms, auth, configPath)
        body := map[string]interface{}{"token_threshold": 5000}
        req := makeAuthenticatedRequest(t, auth, http.MethodPut, "/admin/api/config/routing", body)
        rec := httptest.NewRecorder()
        h.handleRoutingConfig(rec, req)
        if rec.Code != http.StatusInternalServerError {
            t.Fatalf("expected 500, got %d", rec.Code)
        }
    })

    t.Run("update_nonexistent_config_file", func(t *testing.T) {
        loadTestConfig(t, defaultYAML)
        h := newTestHandler(t, ms, auth, "/nonexistent/path/config.yaml")
        body := map[string]interface{}{"token_threshold": 5000}
        req := makeAuthenticatedRequest(t, auth, http.MethodPut, "/admin/api/config/routing", body)
        rec := httptest.NewRecorder()
        h.handleRoutingConfig(rec, req)
        if rec.Code != http.StatusInternalServerError {
            t.Fatalf("expected 500, got %d", rec.Code)
        }
    })

    t.Run("update_no_routing_section_in_file", func(t *testing.T) {
        tmpDir := t.TempDir()
        configPath := filepath.Join(tmpDir, "config.yaml")
        if err := os.WriteFile(configPath, []byte("server:\n  port: 8100\n"), 0644); err != nil {
            t.Fatalf("failed to write config: %v", err)
        }
        loadTestConfig(t, defaultYAML)

        h := newTestHandler(t, ms, auth, configPath)
        body := map[string]interface{}{"token_threshold": 5000}
        req := makeAuthenticatedRequest(t, auth, http.MethodPut, "/admin/api/config/routing", body)
        rec := httptest.NewRecorder()
        h.handleRoutingConfig(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
        }
    })
}

func TestHandleBackendsConfig(t *testing.T) {
    auth := newTestAuth(t)
    ms := newMockStore()

    backendsYAML := `
server:
  host: "0.0.0.0"
  port: 8100
routing:
  token_threshold: 8000
  output_input_ratio_threshold: 0.6
  local_priority:
    enabled: true
    max_system_memory_ratio: 0.9
    max_mlx_memory_ratio: 0.7
    max_concurrent: 8
  circuit_breaker:
    failure_threshold: 5
  fallback:
    enabled: false
    cloud_default: "openai"
  ratio_tiers:
    enabled: false
    rules: []
  token_tiers:
    enabled: false
    metric: "total"
    rules: []
backends:
  local:
    type: local
    base_url: "http://localhost:11434"
    api_key: ""
    timeout: 30s
    enabled: true
  openai:
    type: openai
    base_url: "https://api.openai.com"
    api_key: "sk-test-api-key-1234567890"
    timeout: 60s
    enabled: true
`
    loadTestConfig(t, backendsYAML)

    t.Run("get_backends", func(t *testing.T) {
        h := newTestHandler(t, ms, auth, "")
        req := makeAuthenticatedRequest(t, auth, http.MethodGet, "/admin/api/config/backends", nil)
        rec := httptest.NewRecorder()
        h.handleBackendsConfig(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
        }
        arr := decodeResponseArray(t, rec)
        if len(arr) != 2 {
            t.Fatalf("expected 2 backends, got %d", len(arr))
        }
    })

    t.Run("method_not_allowed", func(t *testing.T) {
        h := newTestHandler(t, ms, auth, "")
        req := makeAuthenticatedRequest(t, auth, http.MethodPost, "/admin/api/config/backends", nil)
        rec := httptest.NewRecorder()
        h.handleBackendsConfig(rec, req)
        if rec.Code != http.StatusMethodNotAllowed {
            t.Fatalf("expected 405, got %d", rec.Code)
        }
    })
}

func TestHandleBackendByName(t *testing.T) {
    auth := newTestAuth(t)
    ms := newMockStore()

    backendsYAML := `
server:
  host: "0.0.0.0"
  port: 8100
routing:
  token_threshold: 8000
  output_input_ratio_threshold: 0.6
  local_priority:
    enabled: true
    max_system_memory_ratio: 0.9
    max_mlx_memory_ratio: 0.7
    max_concurrent: 8
  circuit_breaker:
    failure_threshold: 5
  fallback:
    enabled: false
    cloud_default: "openai"
  ratio_tiers:
    enabled: false
    rules: []
  token_tiers:
    enabled: false
    metric: "total"
    rules: []
backends:
  local:
    type: local
    base_url: "http://localhost:11434"
    api_key: "sk-long-api-key-for-testing"
    timeout: 30s
    enabled: true
`
    loadTestConfig(t, backendsYAML)

    t.Run("get_backend_success", func(t *testing.T) {
        h := newTestHandler(t, ms, auth, "")
        req := makeAuthenticatedRequest(t, auth, http.MethodGet, "/admin/api/config/backends/local", nil)
        rec := httptest.NewRecorder()
        h.handleBackendByName(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
        }
        result := decodeResponse(t, rec)
        if result["name"] != "local" {
            t.Fatalf("expected name local, got %v", result["name"])
        }
        if result["api_key"] != "****ting" {
            t.Fatalf("expected masked api key, got %v", result["api_key"])
        }
    })

    t.Run("get_backend_not_found", func(t *testing.T) {
        h := newTestHandler(t, ms, auth, "")
        req := makeAuthenticatedRequest(t, auth, http.MethodGet, "/admin/api/config/backends/nonexistent", nil)
        rec := httptest.NewRecorder()
        h.handleBackendByName(rec, req)
        if rec.Code != http.StatusNotFound {
            t.Fatalf("expected 404, got %d", rec.Code)
        }
    })

    t.Run("empty_backend_name", func(t *testing.T) {
        h := newTestHandler(t, ms, auth, "")
        req := makeAuthenticatedRequest(t, auth, http.MethodGet, "/admin/api/config/backends/", nil)
        rec := httptest.NewRecorder()
        h.handleBackendByName(rec, req)
        if rec.Code != http.StatusBadRequest {
            t.Fatalf("expected 400, got %d", rec.Code)
        }
    })

    t.Run("put_backend_no_config_path", func(t *testing.T) {
        h := newTestHandler(t, ms, auth, "")
        body := map[string]interface{}{"base_url": "http://new-url"}
        req := makeAuthenticatedRequest(t, auth, http.MethodPut, "/admin/api/config/backends/local", body)
        rec := httptest.NewRecorder()
        h.handleBackendByName(rec, req)
        if rec.Code != http.StatusInternalServerError {
            t.Fatalf("expected 500, got %d", rec.Code)
        }
    })

    t.Run("put_backend_not_found_in_snapshot", func(t *testing.T) {
        h := newTestHandler(t, ms, auth, "/dummy/path")
        body := map[string]interface{}{"base_url": "http://new-url"}
        req := makeAuthenticatedRequest(t, auth, http.MethodPut, "/admin/api/config/backends/nonexistent", body)
        rec := httptest.NewRecorder()
        h.handleBackendByName(rec, req)
        if rec.Code != http.StatusNotFound {
            t.Fatalf("expected 404, got %d", rec.Code)
        }
    })

    t.Run("put_backend_invalid_json", func(t *testing.T) {
        h := newTestHandler(t, ms, auth, "/dummy/path")
        req := makeAuthenticatedRequest(t, auth, http.MethodPut, "/admin/api/config/backends/local", nil)
        req.Body = io.NopCloser(bytes.NewReader([]byte("not-json")))
        rec := httptest.NewRecorder()
        h.handleBackendByName(rec, req)
        if rec.Code != http.StatusBadRequest {
            t.Fatalf("expected 400, got %d", rec.Code)
        }
    })

    t.Run("method_not_allowed", func(t *testing.T) {
        h := newTestHandler(t, ms, auth, "")
        req := makeAuthenticatedRequest(t, auth, http.MethodDelete, "/admin/api/config/backends/local", nil)
        rec := httptest.NewRecorder()
        h.handleBackendByName(rec, req)
        if rec.Code != http.StatusMethodNotAllowed {
            t.Fatalf("expected 405, got %d", rec.Code)
        }
    })

    t.Run("update_backend_base_url", func(t *testing.T) {
        tmpDir := t.TempDir()
        configPath := filepath.Join(tmpDir, "config.yaml")
        if err := os.WriteFile(configPath, []byte(backendsYAML), 0644); err != nil {
            t.Fatalf("failed to write config: %v", err)
        }
        loadTestConfig(t, backendsYAML)

        h := newTestHandler(t, ms, auth, configPath)
        body := map[string]interface{}{"base_url": "http://localhost:11444"}
        req := makeAuthenticatedRequest(t, auth, http.MethodPut, "/admin/api/config/backends/local", body)
        rec := httptest.NewRecorder()
        h.handleBackendByName(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
        }
    })

    t.Run("update_backend_enabled", func(t *testing.T) {
        tmpDir := t.TempDir()
        configPath := filepath.Join(tmpDir, "config.yaml")
        if err := os.WriteFile(configPath, []byte(backendsYAML), 0644); err != nil {
            t.Fatalf("failed to write config: %v", err)
        }
        loadTestConfig(t, backendsYAML)

        h := newTestHandler(t, ms, auth, configPath)
        body := map[string]interface{}{"enabled": false}
        req := makeAuthenticatedRequest(t, auth, http.MethodPut, "/admin/api/config/backends/local", body)
        rec := httptest.NewRecorder()
        h.handleBackendByName(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
        }
    })

    t.Run("update_backend_api_key", func(t *testing.T) {
        tmpDir := t.TempDir()
        configPath := filepath.Join(tmpDir, "config.yaml")
        if err := os.WriteFile(configPath, []byte(backendsYAML), 0644); err != nil {
            t.Fatalf("failed to write config: %v", err)
        }
        loadTestConfig(t, backendsYAML)

        h := newTestHandler(t, ms, auth, configPath)
        body := map[string]interface{}{"api_key": "sk-new-key-1234567890"}
        req := makeAuthenticatedRequest(t, auth, http.MethodPut, "/admin/api/config/backends/local", body)
        rec := httptest.NewRecorder()
        h.handleBackendByName(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
        }
    })

    t.Run("update_backend_timeout", func(t *testing.T) {
        tmpDir := t.TempDir()
        configPath := filepath.Join(tmpDir, "config.yaml")
        if err := os.WriteFile(configPath, []byte(backendsYAML), 0644); err != nil {
            t.Fatalf("failed to write config: %v", err)
        }
        loadTestConfig(t, backendsYAML)

        h := newTestHandler(t, ms, auth, configPath)
        body := map[string]interface{}{"timeout": "120s"}
        req := makeAuthenticatedRequest(t, auth, http.MethodPut, "/admin/api/config/backends/local", body)
        rec := httptest.NewRecorder()
        h.handleBackendByName(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
        }
    })

    t.Run("update_backend_invalid_config_file", func(t *testing.T) {
        tmpDir := t.TempDir()
        configPath := filepath.Join(tmpDir, "bad.yaml")
        if err := os.WriteFile(configPath, []byte("invalid: [yaml"), 0644); err != nil {
            t.Fatalf("failed to write config: %v", err)
        }
        loadTestConfig(t, backendsYAML)

        h := newTestHandler(t, ms, auth, configPath)
        body := map[string]interface{}{"base_url": "http://new"}
        req := makeAuthenticatedRequest(t, auth, http.MethodPut, "/admin/api/config/backends/local", body)
        rec := httptest.NewRecorder()
        h.handleBackendByName(rec, req)
        if rec.Code != http.StatusInternalServerError {
            t.Fatalf("expected 500, got %d", rec.Code)
        }
    })

    t.Run("update_backend_no_backends_section", func(t *testing.T) {
        tmpDir := t.TempDir()
        configPath := filepath.Join(tmpDir, "config.yaml")
        if err := os.WriteFile(configPath, []byte("server:\n  port: 8100\n"), 0644); err != nil {
            t.Fatalf("failed to write config: %v", err)
        }
        loadTestConfig(t, backendsYAML)

        h := newTestHandler(t, ms, auth, configPath)
        body := map[string]interface{}{"base_url": "http://new"}
        req := makeAuthenticatedRequest(t, auth, http.MethodPut, "/admin/api/config/backends/local", body)
        rec := httptest.NewRecorder()
        h.handleBackendByName(rec, req)
        if rec.Code != http.StatusInternalServerError {
            t.Fatalf("expected 500, got %d; body: %s", rec.Code, rec.Body.String())
        }
    })

    t.Run("update_backend_not_in_file", func(t *testing.T) {
        tmpDir := t.TempDir()
        configPath := filepath.Join(tmpDir, "config.yaml")
        yamlContent := "server:\n  port: 8100\nbackends:\n  other:\n    type: cloud\n"
        if err := os.WriteFile(configPath, []byte(yamlContent), 0644); err != nil {
            t.Fatalf("failed to write config: %v", err)
        }
        loadTestConfig(t, backendsYAML)

        h := newTestHandler(t, ms, auth, configPath)
        body := map[string]interface{}{"base_url": "http://new"}
        req := makeAuthenticatedRequest(t, auth, http.MethodPut, "/admin/api/config/backends/local", body)
        rec := httptest.NewRecorder()
        h.handleBackendByName(rec, req)
        if rec.Code != http.StatusNotFound {
            t.Fatalf("expected 404, got %d; body: %s", rec.Code, rec.Body.String())
        }
    })
}

func TestRegisterRoutes(t *testing.T) {
    auth := newTestAuth(t)
    ms := newMockStore()
    h := newTestHandler(t, ms, auth, "")

    mux := http.NewServeMux()
    h.RegisterRoutes(mux)

    req := httptest.NewRequest(http.MethodGet, "/admin/api/keys", nil)
    token, _ := auth.GenerateToken("admin", "admin")
    req.Header.Set("Authorization", "Bearer "+token)
    rec := httptest.NewRecorder()
    mux.ServeHTTP(rec, req)
    if rec.Code != http.StatusOK {
        t.Fatalf("expected 200, got %d", rec.Code)
    }
}

func TestCreateKeyWithNumericStatus(t *testing.T) {
    t.Parallel()

    auth := newTestAuth(t)
    ms := newMockStore()
    h := newTestHandler(t, ms, auth, "")

    body := map[string]interface{}{
        "name":   "numeric-status-key",
        "status": 1,
        "models": []string{"qwen3"},
    }
    req := makeAuthenticatedRequest(t, auth, http.MethodPost, "/admin/api/keys", body)
    rec := httptest.NewRecorder()
    h.handleKeys(rec, req)
    if rec.Code != http.StatusCreated {
        t.Fatalf("expected 201, got %d; body: %s", rec.Code, rec.Body.String())
    }
}

func TestCreateChannelWithNumericStatus(t *testing.T) {
    t.Parallel()

    auth := newTestAuth(t)
    ms := newMockStore()
    h := newTestHandler(t, ms, auth, "")

    body := map[string]interface{}{
        "name":     "numeric-status-ch",
        "type":     "local",
        "base_url": "http://localhost:11434",
        "status":   1,
    }
    req := makeAuthenticatedRequest(t, auth, http.MethodPost, "/admin/api/channels", body)
    rec := httptest.NewRecorder()
    h.handleChannels(rec, req)
    if rec.Code != http.StatusCreated {
        t.Fatalf("expected 201, got %d; body: %s", rec.Code, rec.Body.String())
    }
}

func TestListKeysWithEntries(t *testing.T) {
    t.Parallel()

    auth := newTestAuth(t)
    ms := newMockStore()
    _ = ms.CreateKey(&store.APIKeyEntry{Name: "key1", Status: "active", KeyPrefix: "sk-abc", AllowedModels: []string{"qwen3"}, QuotaLimit: 100, QuotaUsed: 10, CreatedAt: time.Now()})
    _ = ms.CreateKey(&store.APIKeyEntry{Name: "key2", Status: "disabled", KeyPrefix: "sk-def", AllowedModels: []string{"gpt4"}, QuotaLimit: 200, QuotaUsed: 50, CreatedAt: time.Now()})
    h := newTestHandler(t, ms, auth, "")

    req := makeAuthenticatedRequest(t, auth, http.MethodGet, "/admin/api/keys", nil)
    rec := httptest.NewRecorder()
    h.handleKeys(rec, req)
    if rec.Code != http.StatusOK {
        t.Fatalf("expected 200, got %d", rec.Code)
    }
    arr := decodeResponseArray(t, rec)
    if len(arr) != 2 {
        t.Fatalf("expected 2 keys, got %d", len(arr))
    }
}

func TestListChannelsWithEntries(t *testing.T) {
    t.Parallel()

    auth := newTestAuth(t)
    ms := newMockStore()
    _ = ms.CreateChannel(&store.ChannelEntry{Name: "ch1", Type: "local", Status: "active", BaseURL: "http://localhost:11434", Models: []string{"qwen3"}, Priority: 1, Weight: 10, CreatedAt: time.Now()})
    _ = ms.CreateChannel(&store.ChannelEntry{Name: "ch2", Type: "cloud", Status: "disabled", BaseURL: "https://api.openai.com", Models: []string{"gpt4"}, Priority: 2, Weight: 5, CreatedAt: time.Now()})
    h := newTestHandler(t, ms, auth, "")

    req := makeAuthenticatedRequest(t, auth, http.MethodGet, "/admin/api/channels", nil)
    rec := httptest.NewRecorder()
    h.handleChannels(rec, req)
    if rec.Code != http.StatusOK {
        t.Fatalf("expected 200, got %d", rec.Code)
    }
    arr := decodeResponseArray(t, rec)
    if len(arr) != 2 {
        t.Fatalf("expected 2 channels, got %d", len(arr))
    }
}

type errStore struct {
    *mockStore
}

func (e *errStore) ListKeys() ([]*store.APIKeyEntry, error) {
    return nil, fmt.Errorf("store error")
}

func (e *errStore) ListChannels() ([]*store.ChannelEntry, error) {
    return nil, fmt.Errorf("store error")
}

func (e *errStore) QueryLogs(filter store.LogFilter) ([]*store.RequestLog, int, error) {
    return nil, 0, fmt.Errorf("store error")
}

func (e *errStore) GetTokenStats(from, to time.Time, groupBy string) ([]*store.TokenStat, error) {
    return nil, fmt.Errorf("store error")
}

func (e *errStore) GetCostStats(from, to time.Time, groupBy string) ([]*store.CostStat, error) {
    return nil, fmt.Errorf("store error")
}

func (e *errStore) GetModelStats(from, to time.Time) ([]*store.ModelStat, error) {
    return nil, fmt.Errorf("store error")
}

func (e *errStore) GetLatencyStats(from, to time.Time) ([]*store.LatencyStat, error) {
    return nil, fmt.Errorf("store error")
}

func (e *errStore) GetErrorStats(from, to time.Time) ([]*store.ErrorStat, error) {
    return nil, fmt.Errorf("store error")
}

func (e *errStore) GetDashboardOverview() (*store.DashboardOverview, error) {
    return nil, fmt.Errorf("store error")
}

func (e *errStore) GetKeyProfitStats(from, to time.Time) ([]*store.KeyProfitStat, error) {
    return nil, fmt.Errorf("store error")
}

func (e *errStore) ExportLogs(filter store.LogFilter, format string) ([]byte, error) {
    return nil, fmt.Errorf("store error")
}

func TestStoreErrors(t *testing.T) {
    auth := newTestAuth(t)
    base := newMockStore()
    es := &errStore{mockStore: base}
    h := newTestHandler(t, es, auth, "")

    t.Run("list_keys_error", func(t *testing.T) {
        req := makeAuthenticatedRequest(t, auth, http.MethodGet, "/admin/api/keys", nil)
        rec := httptest.NewRecorder()
        h.handleKeys(rec, req)
        if rec.Code != http.StatusInternalServerError {
            t.Fatalf("expected 500, got %d", rec.Code)
        }
    })

    t.Run("list_channels_error", func(t *testing.T) {
        req := makeAuthenticatedRequest(t, auth, http.MethodGet, "/admin/api/channels", nil)
        rec := httptest.NewRecorder()
        h.handleChannels(rec, req)
        if rec.Code != http.StatusInternalServerError {
            t.Fatalf("expected 500, got %d", rec.Code)
        }
    })

    t.Run("query_logs_error", func(t *testing.T) {
        req := makeAuthenticatedRequest(t, auth, http.MethodGet, "/admin/api/logs", nil)
        rec := httptest.NewRecorder()
        h.handleLogs(rec, req)
        if rec.Code != http.StatusInternalServerError {
            t.Fatalf("expected 500, got %d", rec.Code)
        }
    })

    t.Run("analytics_overview_error", func(t *testing.T) {
        req := makeAuthenticatedRequest(t, auth, http.MethodGet, "/admin/api/analytics", nil)
        rec := httptest.NewRecorder()
        h.handleAnalyticsOverview(rec, req)
        if rec.Code != http.StatusInternalServerError {
            t.Fatalf("expected 500, got %d", rec.Code)
        }
    })

    t.Run("token_stats_error", func(t *testing.T) {
        req := makeAuthenticatedRequest(t, auth, http.MethodGet, "/admin/api/analytics/tokens", nil)
        rec := httptest.NewRecorder()
        h.handleTokenStats(rec, req)
        if rec.Code != http.StatusInternalServerError {
            t.Fatalf("expected 500, got %d", rec.Code)
        }
    })

    t.Run("cost_stats_error", func(t *testing.T) {
        req := makeAuthenticatedRequest(t, auth, http.MethodGet, "/admin/api/analytics/cost", nil)
        rec := httptest.NewRecorder()
        h.handleCostStats(rec, req)
        if rec.Code != http.StatusInternalServerError {
            t.Fatalf("expected 500, got %d", rec.Code)
        }
    })

    t.Run("model_stats_error", func(t *testing.T) {
        req := makeAuthenticatedRequest(t, auth, http.MethodGet, "/admin/api/analytics/models", nil)
        rec := httptest.NewRecorder()
        h.handleModelStats(rec, req)
        if rec.Code != http.StatusInternalServerError {
            t.Fatalf("expected 500, got %d", rec.Code)
        }
    })

    t.Run("latency_stats_error", func(t *testing.T) {
        req := makeAuthenticatedRequest(t, auth, http.MethodGet, "/admin/api/analytics/latency", nil)
        rec := httptest.NewRecorder()
        h.handleLatencyStats(rec, req)
        if rec.Code != http.StatusInternalServerError {
            t.Fatalf("expected 500, got %d", rec.Code)
        }
    })

    t.Run("error_stats_error", func(t *testing.T) {
        req := makeAuthenticatedRequest(t, auth, http.MethodGet, "/admin/api/analytics/errors", nil)
        rec := httptest.NewRecorder()
        h.handleErrorStats(rec, req)
        if rec.Code != http.StatusInternalServerError {
            t.Fatalf("expected 500, got %d", rec.Code)
        }
    })

    t.Run("dashboard_error", func(t *testing.T) {
        req := makeAuthenticatedRequest(t, auth, http.MethodGet, "/admin/api/dashboard", nil)
        rec := httptest.NewRecorder()
        h.handleDashboard(rec, req)
        if rec.Code != http.StatusInternalServerError {
            t.Fatalf("expected 500, got %d", rec.Code)
        }
    })

    t.Run("profit_stats_error", func(t *testing.T) {
        req := makeAuthenticatedRequest(t, auth, http.MethodGet, "/admin/api/analytics/profit", nil)
        rec := httptest.NewRecorder()
        h.handleProfitStats(rec, req)
        if rec.Code != http.StatusInternalServerError {
            t.Fatalf("expected 500, got %d", rec.Code)
        }
    })

    t.Run("export_logs_error", func(t *testing.T) {
        req := makeAuthenticatedRequest(t, auth, http.MethodGet, "/admin/api/logs/export", nil)
        rec := httptest.NewRecorder()
        h.handleLogsExport(rec, req)
        if rec.Code != http.StatusInternalServerError {
            t.Fatalf("expected 500, got %d", rec.Code)
        }
    })
}
