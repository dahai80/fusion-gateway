package identity

import (
    "context"
    "net"
    "sync/atomic"
    "testing"
    "time"

    "google.golang.org/grpc"
    "google.golang.org/grpc/credentials/insecure"
    "google.golang.org/grpc/test/bufconn"

    pb "github.com/fusion-gateway/fusion-gateway/internal/identity/pb"
)

// fakeIdentity is a test IdentityService that records calls and returns
// canned responses. dialFail toggles a transport failure for breaker tests.
type fakeIdentity struct {
    pb.UnimplementedIdentityServiceServer
    allow      atomic.Bool
    leaseID    string
    tenantID   string
    dialFail   atomic.Bool
    authCalls  atomic.Int64
    releaseCalls atomic.Int64
    reportCalls  atomic.Int64
}

func (f *fakeIdentity) AuthorizeAndAcquire(ctx context.Context, req *pb.AuthorizeAndAcquireRequest) (*pb.AuthorizeAndAcquireResponse, error) {
    f.authCalls.Add(1)
    if f.dialFail.Load() {
        // simulate unavailable: return via context-cancel by blocking forever
        // — but cheaper to just return a transport-flavored error. Use a
        // closed-channel read to force a grpc UNAVAILABLE-like block.
        <-ctx.Done()
        return nil, ctx.Err()
    }
    if !f.allow.Load() {
        return &pb.AuthorizeAndAcquireResponse{
            IsAllowed:  false,
            ErrorCode:  pb.AuthErrorCode_INVALID_API_KEY,
            ErrorMessage: "bad key",
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

func (f *fakeIdentity) ReleaseLease(ctx context.Context, req *pb.ReleaseLeaseRequest) (*pb.ReleaseLeaseResponse, error) {
    f.releaseCalls.Add(1)
    return &pb.ReleaseLeaseResponse{Success: true}, nil
}

func (f *fakeIdentity) ReportUsage(ctx context.Context, req *pb.ReportUsageRequest) (*pb.ReportUsageResponse, error) {
    f.reportCalls.Add(1)
    return &pb.ReportUsageResponse{Success: true, RemainingDailyQuota: 1000}, nil
}

// newTestClient spins a bufconn gRPC server + Client. breakerThreshold=2 +
// openSec=1 keep breaker tests fast.
func newTestClient(t *testing.T, f *fakeIdentity) (*Client, func()) {
    t.Helper()
    lis := bufconn.Listen(1024 * 1024)
    srv := grpc.NewServer()
    pb.RegisterIdentityServiceServer(srv, f)
    go srv.Serve(lis)
    // bypass NewClient's grpc.NewClient (needs a real dialer); construct the
    // Client fields directly over a bufconn dial.
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
    c := &Client{
        conn:     conn,
        stub:     pb.NewIdentityServiceClient(conn),
        breaker:  newBreaker(2, 1*time.Second),
        deadline: 200 * time.Millisecond,
        fallback: false,
        endpoint: "bufnet",
    }
    return c, func() { conn.Close(); srv.Stop() }
}

func TestClient_AuthorizeAndAcquire_Allowed(t *testing.T) {
    f := &fakeIdentity{}
    f.allow.Store(true)
    f.leaseID = "lease-1"
    f.tenantID = "tenant-a"
    c, cleanup := newTestClient(t, f)
    defer cleanup()
    ar, err := c.AuthorizeAndAcquire(context.Background(), "k", "chat", "m", "rid", "1.2.3.4")
    if err != nil {
        t.Fatalf("unexpected err: %v", err)
    }
    if !ar.Allowed || ar.LeaseID != "lease-1" || ar.TenantID != "tenant-a" {
        t.Fatalf("bad result: %+v", ar)
    }
    if ar.Priority != pb.PriorityLevel_PRIORITY_NORMAL {
        t.Fatalf("priority not propagated: %v", ar.Priority)
    }
}

func TestClient_AuthorizeAndAcquire_DeniedDoesNotTripBreaker(t *testing.T) {
    f := &fakeIdentity{}
    f.allow.Store(false) // denied
    c, cleanup := newTestClient(t, f)
    defer cleanup()
    for i := 0; i < 5; i++ {
        ar, err := c.AuthorizeAndAcquire(context.Background(), "k", "chat", "m", "rid", "")
        if err != nil || ar.Allowed {
            t.Fatalf("call %d: expected denied, got err=%v ar=%+v", i, err, ar)
        }
    }
    // breaker must still be closed (denials are successful RPCs).
    if !c.breaker.allow() {
        t.Fatalf("breaker tripped on auth denials (should only trip on transport failures)")
    }
}

func TestClient_BreakerOpensOnTransportFailure(t *testing.T) {
    f := &fakeIdentity{}
    f.dialFail.Store(true)
    c, cleanup := newTestClient(t, f)
    defer cleanup()
    // threshold=2: two transport failures open the breaker.
    for i := 0; i < 2; i++ {
        if _, err := c.AuthorizeAndAcquire(context.Background(), "k", "chat", "m", "rid", ""); err == nil {
            t.Fatalf("call %d: expected transport err", i)
        }
    }
    // breaker now open → ErrBreakerOpen without hitting the server.
    before := f.authCalls.Load()
    _, err := c.AuthorizeAndAcquire(context.Background(), "k", "chat", "m", "rid", "")
    if err != ErrBreakerOpen {
        t.Fatalf("expected ErrBreakerOpen, got %v", err)
    }
    if f.authCalls.Load() != before {
        t.Fatalf("breaker open should short-circuit (no server call)")
    }
}

func TestClient_ReleaseLease_BestEffort(t *testing.T) {
    f := &fakeIdentity{}
    f.allow.Store(true)
    f.leaseID = "lease-rel"
    f.tenantID = "tenant-rel"
    c, cleanup := newTestClient(t, f)
    defer cleanup()
    ar, err := c.AuthorizeAndAcquire(context.Background(), "k", "chat", "m", "rid", "")
    if err != nil || !ar.Allowed {
        t.Fatalf("acquire failed: %v %v", err, ar)
    }
    c.ReleaseLease(ar.LeaseID, ar.TenantID, "stream-end")
    // async: poll up to 200ms for the server to record the call.
    deadline := time.Now().Add(200 * time.Millisecond)
    for time.Now().Before(deadline) && f.releaseCalls.Load() == 0 {
        time.Sleep(2 * time.Millisecond)
    }
    if f.releaseCalls.Load() != 1 {
        t.Fatalf("ReleaseLease not recorded: %d", f.releaseCalls.Load())
    }
    // empty leaseID is a no-op (no goroutine, no server call).
    c.ReleaseLease("", "t", "x")
}

func TestClient_ReportUsage_BestEffort(t *testing.T) {
    f := &fakeIdentity{}
    f.allow.Store(true)
    f.leaseID = "lease-usage"
    f.tenantID = "tenant-usage"
    c, cleanup := newTestClient(t, f)
    defer cleanup()
    ar, _ := c.AuthorizeAndAcquire(context.Background(), "k", "chat", "m", "rid", "")
    c.ReportUsage(UsageReport{
        LeaseID:          ar.LeaseID,
        TenantID:         ar.TenantID,
        ModelName:        "m",
        PromptTokens:     10,
        CompletionTokens: 20,
        ExecutionTimeMS:  5,
        Status:           pb.InferenceStatus_SUCCESS,
    })
    deadline := time.Now().Add(200 * time.Millisecond)
    for time.Now().Before(deadline) && f.reportCalls.Load() == 0 {
        time.Sleep(2 * time.Millisecond)
    }
    if f.reportCalls.Load() != 1 {
        t.Fatalf("ReportUsage not recorded: %d", f.reportCalls.Load())
    }
    c.ReportUsage(UsageReport{LeaseID: ""}) // no-op
}

func TestNewClient_NilOnEmptyEndpoint(t *testing.T) {
    if c := NewClient("", 10, 30, 5, 30, false); c != nil {
        t.Fatalf("expected nil client for empty endpoint")
    }
}
