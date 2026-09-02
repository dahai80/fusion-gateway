package identity

import (
    "context"
    "log/slog"
    "sync"
    "sync/atomic"
    "time"

    "google.golang.org/grpc"
    "google.golang.org/grpc/backoff"
    "google.golang.org/grpc/credentials/insecure"
    "google.golang.org/grpc/keepalive"

    pb "github.com/fusion-gateway/fusion-gateway/internal/identity/pb"
    "github.com/fusion-gateway/fusion-gateway/internal/safego"
)

// Client is the long-lived fusion-identity gRPC control-plane client (#157).
// It owns one gRPC channel (keepalive, reconnect) + a circuit breaker. The
// inference hot path calls AuthorizeAndAcquire before reaching fusion-mlx;
// ReleaseLease/ReportUsage run on stream end / completion.
//
// Breaker semantics (#157 acceptance): identity outage → NEW requests 503
// (or fallback-to-local when configured), active streams keep running. The
// breaker counts consecutive failures; one half-open probe after OpenSec.
type Client struct {
    conn   *grpc.ClientConn
    stub   pb.IdentityServiceClient
    breaker *breaker

    deadline     time.Duration
    fallback     bool
    endpoint     string
}

// NewClient dials the identity gRPC server. Returns nil if endpoint empty
// (caller treats nil = identity disabled, same as cfg.Identity.Enabled=false).
func NewClient(endpoint string, deadlineMS, keepAliveSec, breakerThreshold, breakerOpenSec int, fallback bool) *Client {
    if endpoint == "" {
        return nil
    }
    deadline := time.Duration(deadlineMS) * time.Millisecond
    ka := time.Duration(keepAliveSec) * time.Second
    conn, err := grpc.NewClient(endpoint,
        grpc.WithTransportCredentials(insecure.NewCredentials()),
        grpc.WithConnectParams(grpc.ConnectParams{
            MinConnectTimeout: 2 * time.Second,
            Backoff: backoff.Config{
                BaseDelay:  100 * time.Millisecond,
                MaxDelay:   3 * time.Second,
                Multiplier: 1.6,
                Jitter:     0.2,
            },
        }),
        grpc.WithKeepaliveParams(keepalive.ClientParameters{
            Time:                ka,
            Timeout:             ka,
            PermitWithoutStream: true,
        }),
    )
    if err != nil {
        // grpc.NewClient is lazy — it does not dial immediately, so an error
        // here is a config/option error, not a connection error. Log + nil so
        // the caller falls back to the local auth path rather than crashing.
        slog.Error("identity gRPC client construction failed (falling back to local auth)",
            "endpoint", endpoint, "error", err)
        return nil
    }
    c := &Client{
        conn:     conn,
        stub:     pb.NewIdentityServiceClient(conn),
        breaker:  newBreaker(breakerThreshold, time.Duration(breakerOpenSec)*time.Second),
        deadline: deadline,
        fallback: fallback,
        endpoint: endpoint,
    }
    slog.Info("identity gRPC client connected",
        "endpoint", endpoint, "deadline", deadline, "fallback_to_local", fallback)
    return c
}

// AuthResult is the gateway-side projection of AuthorizeAndAcquireResponse.
// LeaseID is "" when not allowed; it threads through the request so the
// stream-end path can call ReleaseLease.
type AuthResult struct {
    Allowed          bool
    ErrorCode        pb.AuthErrorCode
    ErrorMessage     string
    TenantID         string
    TenantName       string
    Tier             string
    Priority         pb.PriorityLevel
    LeaseID          string
    MaxAllowedTokens int32
}

// AuthorizeAndAcquire calls the identity hot-path RPC. Returns (result, err):
// err != nil = transport/breaker failure (caller decides 503 vs fallback);
// err == nil + !Allowed = identity denied (caller maps ErrorCode → HTTP).
func (c *Client) AuthorizeAndAcquire(ctx context.Context, apiKey, module, model, requestID, clientIP string) (*AuthResult, error) {
    if !c.breaker.allow() {
        return nil, ErrBreakerOpen
    }
    rctx, cancel := context.WithTimeout(ctx, c.deadline)
    defer cancel()
    resp, err := c.stub.AuthorizeAndAcquire(rctx, &pb.AuthorizeAndAcquireRequest{
        ApiKey:       apiKey,
        TargetModule: module,
        TargetModel:  model,
        RequestId:    requestID,
        ClientIp:     clientIP,
    })
    if err != nil {
        c.breaker.recordFailure()
        return nil, err
    }
    // A denied response is a SUCCESSFUL RPC (identity answered); do NOT trip
    // the breaker on auth denials — only transport failures open it.
    c.breaker.recordSuccess()
    ar := &AuthResult{
        Allowed:          resp.IsAllowed,
        ErrorCode:        resp.ErrorCode,
        ErrorMessage:     resp.ErrorMessage,
        LeaseID:          resp.LeaseId,
        MaxAllowedTokens: resp.MaxAllowedTokens,
    }
    if tc := resp.TenantContext; tc != nil {
        ar.TenantID = tc.TenantId
        ar.TenantName = tc.TenantName
        ar.Tier = tc.Tier
        ar.Priority = tc.Priority
    }
    return ar, nil
}

// ReleaseLease releases a concurrency lease. Best-effort + async on the
// stream-end path: a failure here must not break an already-completed
// inference response. Runs under a fresh context (the request ctx may be
// canceled at stream end).
func (c *Client) ReleaseLease(leaseID, tenantID, reason string) {
    if leaseID == "" {
        return
    }
    safego.Go("identity.ReleaseLease", func() {
        rctx, cancel := context.WithTimeout(context.Background(), c.deadline)
        defer cancel()
        resp, err := c.stub.ReleaseLease(rctx, &pb.ReleaseLeaseRequest{
            LeaseId:  leaseID,
            TenantId: tenantID,
            Reason:   reason,
        })
        if err != nil {
            slog.Debug("identity ReleaseLease failed (best-effort)",
                "lease_id", leaseID, "tenant", tenantID, "error", err)
            return
        }
        if !resp.Success {
            slog.Debug("identity ReleaseLease returned success=false",
                "lease_id", leaseID, "tenant", tenantID)
        }
    })
}

// UsageReport is the input for ReportUsage.
type UsageReport struct {
    LeaseID          string
    TenantID         string
    ModelName        string
    PromptTokens     int32
    CompletionTokens int32
    ExecutionTimeMS  int64
    Status           pb.InferenceStatus
}

// ReportUsage reports token accounting. Async + best-effort: the inference
// response is already delivered to the client; this is fire-and-forget
// metering into identity's Redis quota counters.
func (c *Client) ReportUsage(rep UsageReport) {
    if rep.LeaseID == "" {
        return
    }
    safego.Go("identity.ReportUsage", func() {
        rctx, cancel := context.WithTimeout(context.Background(), c.deadline)
        defer cancel()
        resp, err := c.stub.ReportUsage(rctx, &pb.ReportUsageRequest{
            LeaseId:         rep.LeaseID,
            TenantId:        rep.TenantID,
            ModelName:       rep.ModelName,
            PromptTokens:    rep.PromptTokens,
            CompletionTokens: rep.CompletionTokens,
            ExecutionTimeMs: rep.ExecutionTimeMS,
            Status:          rep.Status,
        })
        if err != nil {
            slog.Debug("identity ReportUsage failed (best-effort)",
                "lease_id", rep.LeaseID, "tenant", rep.TenantID, "error", err)
            return
        }
        slog.Debug("identity usage reported",
            "lease_id", rep.LeaseID, "tenant", rep.TenantID,
            "remaining_daily_quota", resp.RemainingDailyQuota)
    })
}

// Close tears down the gRPC channel. Called on gateway shutdown.
func (c *Client) Close() error {
    if c == nil || c.conn == nil {
        return nil
    }
    return c.conn.Close()
}

// FallbackToLocal reports whether the client is configured to fall back to
// the local key-store path on identity outage (vs strict 503).
func (c *Client) FallbackToLocal() bool { return c.fallback }

// ErrBreakerOpen is returned when the circuit breaker is open (identity
// unavailable). Callers map this to 503 (strict) or local fallback.
var ErrBreakerOpen = breakerOpenErr{}

type breakerOpenErr struct{}

func (breakerOpenErr) Error() string { return "identity circuit breaker open (service unavailable)" }

// breaker is a minimal consecutive-failure circuit breaker. No external dep
// (sony/gobreaker is already used by the router, but the identity breaker
// has simpler semantics: open after N consecutive failures, half-open after
// a cool-down, one probe, close on success). Kept self-contained so the
// identity package has no import edge on the router.
type breaker struct {
    mu             sync.Mutex
    failures       int
    threshold      int
    openUntil      time.Time
    openDuration   time.Duration
    halfOpenInFlight atomic.Bool
}

func newBreaker(threshold int, openDuration time.Duration) *breaker {
    return &breaker{threshold: threshold, openDuration: openDuration}
}

// allow reports whether a call may proceed. In half-open it allows exactly
// one probe (halfOpenInFlight gate); further calls are rejected until the
// probe resolves (recordSuccess/recordFailure resets the gate).
func (b *breaker) allow() bool {
    b.mu.Lock()
    defer b.mu.Unlock()
    now := time.Now()
    if b.openUntil.IsZero() || now.After(b.openUntil) {
        // closed, or cool-down elapsed → half-open: allow one probe.
        if !b.openUntil.IsZero() {
            if b.halfOpenInFlight.CompareAndSwap(false, true) {
                return true
            }
            // another probe is already in flight → reject until it resolves.
            return false
        }
        return true
    }
    // open → reject.
    return false
}

func (b *breaker) recordSuccess() {
    b.mu.Lock()
    defer b.mu.Unlock()
    b.failures = 0
    b.openUntil = time.Time{}
    b.halfOpenInFlight.Store(false)
}

func (b *breaker) recordFailure() {
    b.mu.Lock()
    defer b.mu.Unlock()
    b.failures++
    b.halfOpenInFlight.Store(false)
    if b.failures >= b.threshold {
        b.openUntil = time.Now().Add(b.openDuration)
        slog.Warn("identity circuit breaker opened",
            "failures", b.failures, "threshold", b.threshold,
            "open_for", b.openDuration)
    }
}
