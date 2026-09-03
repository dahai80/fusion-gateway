package middleware

import (
    "bytes"
    "context"
    "net"
    "net/http"
    "net/http/httptest"
    "strings"
    "sync/atomic"
    "testing"

    "google.golang.org/grpc"
    "google.golang.org/grpc/credentials/insecure"
    "google.golang.org/grpc/test/bufconn"

    "github.com/fusion-gateway/fusion-gateway/internal/adapter"
    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/identity"
    pb "github.com/fusion-gateway/fusion-gateway/internal/identity/pb"
)

// fakeIdentityServer is a minimal IdentityService for middleware tests. It
// is a separate copy from internal/identity's fake (that one is unexported
// and lives in another package); kept small + scoped to these tests.
type fakeIdentityServer struct {
    pb.UnimplementedIdentityServiceServer
    allow     atomic.Bool
    denyCode  atomic.Int32
    leaseID   string
    tenantID  string
    stall     atomic.Bool // stall until ctx deadline → transport failure
    authCalls atomic.Int64
    // #160 cross-tenant guard: when enforceCrossTenant is set, the asserted
    // tenant_id (req.TenantId) must equal realTenant, else refuse with
    // MODEL_UNAUTHORIZED (mimics fusion-identity's P2-3 guard).
    enforceCrossTenant bool
    realTenant         string
    lastAssertedTenant string
}

func (f *fakeIdentityServer) AuthorizeAndAcquire(ctx context.Context, req *pb.AuthorizeAndAcquireRequest) (*pb.AuthorizeAndAcquireResponse, error) {
    f.authCalls.Add(1)
    f.lastAssertedTenant = req.TenantId
    if f.stall.Load() {
        <-ctx.Done()
        return nil, ctx.Err()
    }
    // P2-3 cross-tenant guard: asserted tenant must match the api-key's real tenant.
    if f.enforceCrossTenant && f.realTenant != "" {
        asserted := req.TenantId
        if asserted != "" && asserted != f.realTenant {
            return &pb.AuthorizeAndAcquireResponse{
                IsAllowed:    false,
                ErrorCode:    pb.AuthErrorCode_MODEL_UNAUTHORIZED,
                ErrorMessage: "cross-tenant assertion refused",
            }, nil
        }
    }
    if !f.allow.Load() {
        return &pb.AuthorizeAndAcquireResponse{
            IsAllowed:    false,
            ErrorCode:    pb.AuthErrorCode(f.denyCode.Load()),
            ErrorMessage: "denied",
        }, nil
    }
    return &pb.AuthorizeAndAcquireResponse{
        IsAllowed: true,
        TenantContext: &pb.TenantContext{
            TenantId:   f.tenantID,
            TenantName: f.tenantID,
            Tier:       "pro",
            Priority:   pb.PriorityLevel_PRIORITY_NORMAL,
        },
        LeaseId:          f.leaseID,
        MaxAllowedTokens: 4096,
    }, nil
}

func (f *fakeIdentityServer) ReleaseLease(ctx context.Context, req *pb.ReleaseLeaseRequest) (*pb.ReleaseLeaseResponse, error) {
    return &pb.ReleaseLeaseResponse{Success: true}, nil
}

func (f *fakeIdentityServer) ReportUsage(ctx context.Context, req *pb.ReportUsageRequest) (*pb.ReportUsageResponse, error) {
    return &pb.ReportUsageResponse{Success: true}, nil
}

// newIdentityClient spins a bufconn identity server + real identity.Client.
func newIdentityClient(t *testing.T, f *fakeIdentityServer, fallback bool) (*identity.Client, func()) {
    t.Helper()
    lis := bufconn.Listen(1024 * 1024)
    srv := grpc.NewServer()
    pb.RegisterIdentityServiceServer(srv, f)
    go srv.Serve(lis)
    conn, err := grpc.DialContext(context.Background(), "bufnet",
        grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
            return lis.DialContext(ctx)
        }),
        grpc.WithTransportCredentials(insecure.NewCredentials()),
    )
    if err != nil {
        srv.Stop()
        t.Fatalf("dial bufconn: %v", err)
    }
    c := identity.NewClientForTesting(conn, 200, 5, 1, fallback)
    return c, func() { conn.Close(); srv.Stop() }
}

// principalRequest builds a request with a Principal carrying an api key,
// matching what APIKeyAuthWithStore leaves upstream of IdentityAuth.
func principalRequest(apiKey string) *http.Request {
    r := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBufferString(`{"model":"gpt-x","messages":[]}`))
    ctx := ContextWithPrincipal(r.Context(), &Principal{
        AuthMethod: "api-key",
        KeyConfig:  &config.AuthKeyConfig{Key: apiKey},
        Role:       RoleInference,
    })
    return r.WithContext(ctx)
}

// principalRequestWithTenant is like principalRequest but pre-binds the key
// to a team (p.Team.ID), mimicking APIKeyAuthWithStore's GetTeamByKey
// resolution (#150). IdentityAuth sends p.Team.ID as the asserted tenant.
func principalRequestWithTenant(apiKey, tenantID string) *http.Request {
    r := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBufferString(`{"model":"gpt-x","messages":[]}`))
    ctx := ContextWithPrincipal(r.Context(), &Principal{
        AuthMethod: "api-key",
        KeyConfig:  &config.AuthKeyConfig{Key: apiKey},
        Role:       RoleInference,
        Team:       &TeamInfo{ID: tenantID, Role: RoleInference},
    })
    return r.WithContext(ctx)
}

// TestIdentityAuth_CrossTenantRefused (#160): the gateway sends the
// credential-resolved tenant (p.Team.ID) on AuthorizeAndAcquire. When it
// matches the api-key's real tenant the request is allowed; when the
// identity servicer sees a mismatch it refuses. This proves P2-3 is active
// end-to-end (gateway populates TenantId, identity enforces the guard).
func TestIdentityAuth_CrossTenantRefused(t *testing.T) {
    f := &fakeIdentityServer{
        leaseID:            "lease-7",
        tenantID:           "tenant-real",
        enforceCrossTenant: true,
        realTenant:         "tenant-real",
    }
    f.allow.Store(true)
    c, cleanup := newIdentityClient(t, f, false)
    defer cleanup()
    cfg := &config.IdentityConfig{Enabled: true}

    // Matching tenant → allowed.
    rec := httptest.NewRecorder()
    h := IdentityAuth(c, cfg)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
    h.ServeHTTP(rec, principalRequestWithTenant("k", "tenant-real"))
    if rec.Code != http.StatusOK {
        t.Fatalf("matching tenant should be allowed, got %d", rec.Code)
    }
    if f.lastAssertedTenant != "tenant-real" {
        t.Fatalf("gateway did not send resolved tenant_id; got %q", f.lastAssertedTenant)
    }

    // Mismatched asserted tenant → refused (MODEL_UNAUTHORIZED → 403).
    rec2 := httptest.NewRecorder()
    h2 := IdentityAuth(c, cfg)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        t.Fatalf("mismatched tenant must not reach handler")
    }))
    h2.ServeHTTP(rec2, principalRequestWithTenant("k", "tenant-spoof"))
    if rec2.Code != http.StatusForbidden {
        t.Fatalf("cross-tenant assertion should be refused 403, got %d", rec2.Code)
    }
}

func TestIdentityAuth_NilClientPassthrough(t *testing.T) {
    cfg := &config.IdentityConfig{Enabled: true}
    called := false
    h := IdentityAuth(nil, cfg)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        called = true
    }))
    h.ServeHTTP(httptest.NewRecorder(), principalRequest("k"))
    if !called {
        t.Fatalf("nil client should pass through")
    }
}

func TestIdentityAuth_DisabledPassthrough(t *testing.T) {
    f := &fakeIdentityServer{}
    f.allow.Store(true)
    c, cleanup := newIdentityClient(t, f, false)
    defer cleanup()
    cfg := &config.IdentityConfig{Enabled: false}
    called := false
    h := IdentityAuth(c, cfg)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        called = true
    }))
    h.ServeHTTP(httptest.NewRecorder(), principalRequest("k"))
    if !called {
        t.Fatalf("disabled should pass through")
    }
    if f.authCalls.Load() != 0 {
        t.Fatalf("disabled should not call identity")
    }
}

func TestIdentityAuth_AllowedStampsTenantAndLease(t *testing.T) {
    f := &fakeIdentityServer{}
    f.allow.Store(true)
    f.leaseID = "lease-42"
    f.tenantID = "tenant-z"
    c, cleanup := newIdentityClient(t, f, false)
    defer cleanup()
    cfg := &config.IdentityConfig{Enabled: true}
    var gotLease *IdentityLease
    var gotTenant string
    var gotSigCtx context.Context
    h := IdentityAuth(c, cfg)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        gotLease = IdentityLeaseFromContext(r.Context())
        gotTenant = tenantFromRequest(r)
        gotSigCtx = r.Context()
    }))
    h.ServeHTTP(httptest.NewRecorder(), principalRequest("k"))
    if gotLease == nil || gotLease.LeaseID != "lease-42" {
        t.Fatalf("lease not stamped: %+v", gotLease)
    }
    if gotTenant != "tenant-z" {
        t.Fatalf("tenant not stamped: %q", gotTenant)
    }
    // #157 items 2+3: lease scheduling signals threaded onto ctx for the
    // outbound adapter to stamp X-Fusion-Priority/Max-Tokens.
    sig := adapter.LeaseSignalsFromContext(gotSigCtx)
    if sig.Priority != int32(pb.PriorityLevel_PRIORITY_NORMAL) {
        t.Fatalf("lease signals priority: got %d, want %d", sig.Priority, pb.PriorityLevel_PRIORITY_NORMAL)
    }
    if sig.MaxAllowedTokens != 4096 {
        t.Fatalf("lease signals max_tokens: got %d, want 4096", sig.MaxAllowedTokens)
    }
}

func TestIdentityAuth_DeniedMapsErrorCode(t *testing.T) {
    f := &fakeIdentityServer{}
    f.allow.Store(false)
    f.denyCode.Store(int32(pb.AuthErrorCode_INVALID_API_KEY))
    c, cleanup := newIdentityClient(t, f, false)
    defer cleanup()
    cfg := &config.IdentityConfig{Enabled: true}
    rec := httptest.NewRecorder()
    h := IdentityAuth(c, cfg)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        t.Fatalf("denied request must not reach handler")
    }))
    h.ServeHTTP(rec, principalRequest("k"))
    if rec.Code != http.StatusUnauthorized {
        t.Fatalf("INVALID_API_KEY → 401, got %d", rec.Code)
    }
    if !strings.Contains(rec.Body.String(), "identity_error") {
        t.Fatalf("error body missing type: %s", rec.Body.String())
    }
}

func TestIdentityAuth_QuotaExceededMaps402(t *testing.T) {
    f := &fakeIdentityServer{}
    f.allow.Store(false)
    f.denyCode.Store(int32(pb.AuthErrorCode_DAILY_QUOTA_EXCEEDED))
    c, cleanup := newIdentityClient(t, f, false)
    defer cleanup()
    cfg := &config.IdentityConfig{Enabled: true}
    rec := httptest.NewRecorder()
    h := IdentityAuth(c, cfg)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
    h.ServeHTTP(rec, principalRequest("k"))
    if rec.Code != http.StatusPaymentRequired {
        t.Fatalf("DAILY_QUOTA_EXCEEDED → 402, got %d", rec.Code)
    }
}

func TestIdentityAuth_TransportFailureFallback(t *testing.T) {
    f := &fakeIdentityServer{}
    f.stall.Store(true) // every call times out → transport failure
    c, cleanup := newIdentityClient(t, f, true) // fallback_to_local=true
    defer cleanup()
    cfg := &config.IdentityConfig{Enabled: true}
    reached := false
    h := IdentityAuth(c, cfg)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        reached = true
    }))
    h.ServeHTTP(httptest.NewRecorder(), principalRequest("k"))
    if !reached {
        t.Fatalf("fallback_to_local should proceed on transport failure")
    }
}

func TestIdentityAuth_TransportFailureStrict503(t *testing.T) {
    f := &fakeIdentityServer{}
    f.stall.Store(true)
    c, cleanup := newIdentityClient(t, f, false) // strict
    defer cleanup()
    cfg := &config.IdentityConfig{Enabled: true}
    rec := httptest.NewRecorder()
    h := IdentityAuth(c, cfg)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        t.Fatalf("strict mode should not reach handler")
    }))
    h.ServeHTTP(rec, principalRequest("k"))
    if rec.Code != http.StatusServiceUnavailable {
        t.Fatalf("strict mode transport failure → 503, got %d", rec.Code)
    }
}

func TestIdentityAuth_AdminJwtSkipped(t *testing.T) {
    f := &fakeIdentityServer{}
    f.allow.Store(false) // would deny if called
    c, cleanup := newIdentityClient(t, f, false)
    defer cleanup()
    cfg := &config.IdentityConfig{Enabled: true}
    r := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBufferString(`{"model":"m"}`))
    ctx := ContextWithPrincipal(r.Context(), &Principal{AuthMethod: "admin-jwt", Role: RoleAdmin})
    reached := false
    h := IdentityAuth(c, cfg)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        reached = true
    }))
    h.ServeHTTP(httptest.NewRecorder(), r.WithContext(ctx))
    if !reached {
        t.Fatalf("admin-jwt should bypass identity")
    }
    if f.authCalls.Load() != 0 {
        t.Fatalf("admin-jwt should not call identity")
    }
}

func TestIdentityAuth_PeeksModelWithoutConsumingBody(t *testing.T) {
    f := &fakeIdentityServer{}
    f.allow.Store(true)
    c, cleanup := newIdentityClient(t, f, false)
    defer cleanup()
    cfg := &config.IdentityConfig{Enabled: true}
    var bodySeen string
    h := IdentityAuth(c, cfg)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        buf := make([]byte, 64)
        n, _ := r.Body.Read(buf)
        bodySeen = string(buf[:n])
    }))
    r := principalRequest("k")
    h.ServeHTTP(httptest.NewRecorder(), r)
    if !strings.Contains(bodySeen, `"model":"gpt-x"`) {
        t.Fatalf("downstream body consumed; got %q", bodySeen)
    }
}

func TestInferenceModule(t *testing.T) {
    cases := map[string]string{
        "/v1/chat/completions":  "chat",
        "/v1/messages":          "chat",
        "/v1/completions":       "chat",
        "/v1/embeddings":        "rag",
        "/v1/rerank":            "rag",
        "/v1/images/generations": "design",
        "/gateway/v1/connector/x": "agent",
        "/health":               "",
    }
    for path, want := range cases {
        r := httptest.NewRequest("POST", path, nil)
        if got := inferenceModule(r); got != want {
            t.Errorf("inferenceModule(%q) = %q, want %q", path, got, want)
        }
    }
}

func TestMapAuthErrorCode(t *testing.T) {
    cases := []struct {
        code pb.AuthErrorCode
        want int
    }{
        {pb.AuthErrorCode_INVALID_API_KEY, http.StatusUnauthorized},
        {pb.AuthErrorCode_TENANT_DISABLED, http.StatusForbidden},
        {pb.AuthErrorCode_MODULE_UNAUTHORIZED, http.StatusForbidden},
        {pb.AuthErrorCode_MODEL_UNAUTHORIZED, http.StatusForbidden},
        {pb.AuthErrorCode_CONCURRENCY_LIMIT_EXCEEDED, http.StatusTooManyRequests},
        {pb.AuthErrorCode_DAILY_QUOTA_EXCEEDED, http.StatusPaymentRequired},
        {pb.AuthErrorCode_RATE_LIMIT_EXCEEDED, http.StatusTooManyRequests},
    }
    for _, tc := range cases {
        status, _ := mapAuthErrorCode(tc.code, "")
        if status != tc.want {
            t.Errorf("code %v → %d, want %d", tc.code, status, tc.want)
        }
    }
}

// tenantFromRequest reads the adapter tenant stamped into ctx. Uses the
// adapter package's TenantFromContext via a thin local import-free check:
// the middleware stamps adapter.WithTenant, whose value is read back by
// adapter.TenantFromContext. To avoid an import cycle in the test helper,
// we read the header that would be set on an outbound request — but here
// we just re-extract from the Principal.Team that IdentityAuth stamped.
func tenantFromRequest(r *http.Request) string {
    p := PrincipalFromContext(r.Context())
    if p == nil || p.Team == nil {
        return ""
    }
    return p.Team.ID
}
