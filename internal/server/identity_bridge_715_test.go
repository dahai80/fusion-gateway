package server

import (
    "context"
    "encoding/json"
    "fmt"
    "net/http"
    "net/http/httptest"
    "strings"
    "sync"
    "sync/atomic"
    "testing"
    "time"
)

// #715: the identity vault resolves refresh tokens to access tokens with
// per-entry singleflight (identity rotates refresh tokens single-use; a
// parallel refresh trips the reuse detector and revokes the family).

func newTestVault(t *testing.T, handler http.HandlerFunc) (*identityVault, *httptest.Server) {
    t.Helper()
    ts := httptest.NewServer(handler)
    t.Cleanup(ts.Close)
    v := newIdentityVault(ts.URL)
    v.client = ts.Client()
    return v, ts
}

func TestIdentityVaultStoreAndResolve(t *testing.T) {
    var refreshes atomic.Int64
    v, _ := newTestVault(t, func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path != "/api/v1/auth/refresh" {
            w.WriteHeader(http.StatusNotFound)
            return
        }
        n := refreshes.Add(1)
        _ = json.NewEncoder(w).Encode(map[string]interface{}{
            "access_token":  fmt.Sprintf("acc-%d", n),
            "refresh_token": fmt.Sprintf("rt-%d", n),
            "expires_in":    3600,
        })
    })
    v.Store("sess-1", "usr_1", "community", "rt-0", time.Now().Add(time.Hour).Unix())

    tok, err := v.AccessToken(context.Background(), "sess-1")
    if err != nil {
        t.Fatalf("first resolve: %v", err)
    }
    if tok == "" {
        t.Fatal("empty access token")
    }
    // Second resolve inside the slack window reuses the cached token —
    // no second refresh round-trip.
    tok2, err := v.AccessToken(context.Background(), "sess-1")
    if err != nil {
        t.Fatalf("second resolve: %v", err)
    }
    if tok2 != tok {
        t.Fatalf("cached token changed: %s vs %s", tok, tok2)
    }
    if got := refreshes.Load(); got != 1 {
        t.Fatalf("expected 1 refresh round-trip, got %d", got)
    }
}

func TestIdentityVaultNoPrincipal(t *testing.T) {
    v, _ := newTestVault(t, func(w http.ResponseWriter, _ *http.Request) {
        t.Error("refresh must not be called for unknown sessions")
    })
    if _, err := v.AccessToken(context.Background(), "unknown"); err == nil {
        t.Fatal("expected error for session without vault entry")
    }
}

func TestIdentityVaultRefreshRejectedDropsEntry(t *testing.T) {
    calls := 0
    var mu sync.Mutex
    v, _ := newTestVault(t, func(w http.ResponseWriter, r *http.Request) {
        mu.Lock()
        calls++
        mu.Unlock()
        w.WriteHeader(http.StatusUnauthorized)
    })
    v.Store("sess-x", "usr_1", "community", "rt-dead", time.Now().Add(time.Hour).Unix())
    if _, err := v.AccessToken(context.Background(), "sess-x"); err == nil {
        t.Fatal("expected refresh failure")
    }
    v.mu.Lock()
    _, alive := v.entries[identityVaultKey("sess-x")]
    v.mu.Unlock()
    if alive {
        t.Fatal("rejected refresh must drop the vault entry")
    }
}

func TestIdentityVaultConcurrentResolveSingleRefresh(t *testing.T) {
    var refreshes atomic.Int64
    v, _ := newTestVault(t, func(w http.ResponseWriter, r *http.Request) {
        refreshes.Add(1)
        time.Sleep(20 * time.Millisecond) // widen the race window
        _ = json.NewEncoder(w).Encode(map[string]interface{}{
            "access_token":  "acc-concurrent",
            "refresh_token": "rt-next",
            "expires_in":    3600,
        })
    })
    v.Store("sess-c", "usr_1", "community", "rt-0", time.Now().Add(time.Hour).Unix())

    const n = 8
    var wg sync.WaitGroup
    errs := make(chan error, n)
    for i := 0; i < n; i++ {
        wg.Add(1)
        go func() {
            defer wg.Done()
            tok, err := v.AccessToken(context.Background(), "sess-c")
            if err != nil {
                errs <- err
                return
            }
            if tok != "acc-concurrent" {
                errs <- errBadToken{tok}
            }
        }()
    }
    wg.Wait()
    close(errs)
    for err := range errs {
        t.Fatalf("concurrent resolve: %v", err)
    }
    if got := refreshes.Load(); got != 1 {
        t.Fatalf("expected exactly 1 refresh under concurrency, got %d", got)
    }
}

type errBadToken struct{ tok string }

func (e errBadToken) Error() string { return "unexpected token: " + e.tok }

func TestIdentityVaultDropAndLogoutNilSafety(t *testing.T) {
    v, _ := newTestVault(t, func(w http.ResponseWriter, _ *http.Request) {})
    v.Store("sess-d", "usr_1", "community", "rt", time.Now().Add(time.Hour).Unix())
    v.Drop("sess-d")
    v.mu.Lock()
    _, alive := v.entries[identityVaultKey("sess-d")]
    v.mu.Unlock()
    if alive {
        t.Fatal("drop must remove the entry")
    }
    // Nil receiver must be a no-op (logout path on a bridge-disabled server).
    var nilVault *identityVault
    nilVault.Drop("anything")
    nilVault.Store("t", "s", "c", "r", 1)
}

func TestIdentityMessagingProxyAuthSurfaces(t *testing.T) {
    s := &Server{} // no vault: bridge disabled
    req := httptest.NewRequest(http.MethodGet, "/api/messaging/conversations", nil)
    rec := httptest.NewRecorder()
    s.handleIdentityMessagingProxy(rec, req)
    if rec.Code != http.StatusNotFound {
        t.Fatalf("bridge disabled: expected 404, got %d", rec.Code)
    }

    v, ts := newTestVault(t, func(w http.ResponseWriter, r *http.Request) {
        _ = json.NewEncoder(w).Encode(map[string]interface{}{
            "access_token": "acc-1", "refresh_token": "rt-1", "expires_in": 3600,
        })
    })
    t.Cleanup(ts.Close)
    s2 := &Server{identityVault: v}
    req2 := httptest.NewRequest(http.MethodGet, "/api/messaging/conversations", nil)
    rec2 := httptest.NewRecorder()
    s2.handleIdentityMessagingProxy(rec2, req2)
    if rec2.Code != http.StatusForbidden {
        t.Fatalf("no principal: expected 403, got %d", rec2.Code)
    }
    var env struct {
        Code string `json:"code"`
    }
    _ = json.Unmarshal(rec2.Body.Bytes(), &env)
    if env.Code != "identity_linked_login_required" {
        t.Fatalf("expected identity_linked_login_required, got %q", env.Code)
    }
}

func TestIdentityMessagingProxyForwardsBearer(t *testing.T) {
    var gotAuthz, gotPath atomic.Value
    upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path == "/api/v1/auth/refresh" {
            _ = json.NewEncoder(w).Encode(map[string]interface{}{
                "access_token": "acc-fwd", "refresh_token": "rt-fwd", "expires_in": 3600,
            })
            return
        }
        gotAuthz.Store(r.Header.Get("Authorization"))
        gotPath.Store(r.URL.Path)
        w.Header().Set("Content-Type", "application/json")
        w.WriteHeader(http.StatusOK)
        _, _ = w.Write([]byte(`{"conversations":[]}`))
    }))
    defer upstream.Close()

    v := newIdentityVault(upstream.URL)
    v.client = upstream.Client()
    v.Store("sess-f", "usr_1", "community", "rt-0", time.Now().Add(time.Hour).Unix())
    s := &Server{identityVault: v}

    // Session token rides the context exactly as withShellSession validated
    // it; a client-forged Authorization header must NOT reach upstream.
    req := httptest.NewRequest(http.MethodGet, "/api/messaging/conversations?scope=factory_order", nil)
    req.Header.Set("Authorization", "Bearer client-forged")
    req = req.WithContext(withShellToken(req.Context(), "sess-f"))
    rec := httptest.NewRecorder()
    s.handleIdentityMessagingProxy(rec, req)
    if rec.Code != http.StatusOK {
        t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
    }
    if a, _ := gotAuthz.Load().(string); a != "Bearer acc-fwd" {
        t.Fatalf("upstream got Authorization %q", a)
    }
    if p, _ := gotPath.Load().(string); p != "/api/messaging/conversations" {
        t.Fatalf("upstream got path %q", p)
    }
    if !strings.Contains(rec.Body.String(), "conversations") {
        t.Fatalf("response body not forwarded: %s", rec.Body.String())
    }
}
