package mcp

import (
    "net/http"
    "net/http/httptest"
    "testing"
)

// reached records whether the protected handler was called.
func reachedHandler(t *testing.T, got *bool) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        *got = true
        w.WriteHeader(http.StatusOK)
    })
}

// TestAuthGate_MissingTokenRejected: no Authorization header → 401, handler NOT reached.
// Guard: if the gate did not check for an empty token first, an anonymous request
// would reach the protected handler (the SSRF amplifier F1 closed at the mux level
// would reopen at the auth level).
func TestAuthGate_MissingTokenRejected(t *testing.T) {
    got := false
    h := AuthGate(AuthConfig{Token: "secret"})(reachedHandler(t, &got))
    rec := httptest.NewRecorder()
    h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/mcp/v1/call", nil))
    if rec.Code != http.StatusUnauthorized {
        t.Fatalf("code=%d, want 401", rec.Code)
    }
    if got {
        t.Fatal("protected handler reached without a token")
    }
}

// TestAuthGate_ValidTokenPasses: Bearer <mcp.token> → 200, handler reached.
// Guard: if the token comparison were inverted or the wrong field compared, a
// valid token would still 401.
func TestAuthGate_ValidTokenPasses(t *testing.T) {
    got := false
    h := AuthGate(AuthConfig{Token: "secret"})(reachedHandler(t, &got))
    rec := httptest.NewRecorder()
    req := httptest.NewRequest(http.MethodPost, "/mcp/v1/call", nil)
    req.Header.Set("Authorization", "Bearer secret")
    h.ServeHTTP(rec, req)
    if rec.Code != http.StatusOK {
        t.Fatalf("code=%d, want 200", rec.Code)
    }
    if !got {
        t.Fatal("protected handler NOT reached with valid token")
    }
}

// TestAuthGate_WrongTokenRejected: Bearer <wrong> → 401, handler NOT reached.
// Guard: if the gate accepted any non-empty token, a wrong token would pass.
func TestAuthGate_WrongTokenRejected(t *testing.T) {
    got := false
    h := AuthGate(AuthConfig{Token: "secret"})(reachedHandler(t, &got))
    rec := httptest.NewRecorder()
    req := httptest.NewRequest(http.MethodPost, "/mcp/v1/call", nil)
    req.Header.Set("Authorization", "Bearer nope")
    h.ServeHTTP(rec, req)
    if rec.Code != http.StatusUnauthorized {
        t.Fatalf("code=%d, want 401", rec.Code)
    }
    if got {
        t.Fatal("protected handler reached with wrong token")
    }
}

// TestAuthGate_MasterKeyFallback: no mcp.token set, Bearer <master_key> → 200.
// Guard: if the master-key fallback branch were missing, an operator relying on
// the master key for MCP (no dedicated token) would be locked out.
func TestAuthGate_MasterKeyFallback(t *testing.T) {
    got := false
    h := AuthGate(AuthConfig{Token: "", MasterKey: "master-secret"})(reachedHandler(t, &got))
    rec := httptest.NewRecorder()
    req := httptest.NewRequest(http.MethodPost, "/mcp/v1/call", nil)
    req.Header.Set("Authorization", "Bearer master-secret")
    h.ServeHTTP(rec, req)
    if rec.Code != http.StatusOK {
        t.Fatalf("code=%d, want 200 (master-key fallback)", rec.Code)
    }
    if !got {
        t.Fatal("protected handler NOT reached with master-key fallback")
    }
}

// TestAuthGate_NoCredentialRejectsAll: both token and master_key empty → 401 even
// with a presented token. Defense in depth: config validation rejects this at
// load, but the gate must still fail-closed at request time if reached anyway.
// Guard: if the gate treated empty-config as "open", an enabled MCP with no
// credential would be anonymously callable.
func TestAuthGate_NoCredentialRejectsAll(t *testing.T) {
    got := false
    h := AuthGate(AuthConfig{})(reachedHandler(t, &got))
    rec := httptest.NewRecorder()
    req := httptest.NewRequest(http.MethodPost, "/mcp/v1/call", nil)
    req.Header.Set("Authorization", "Bearer anything")
    h.ServeHTTP(rec, req)
    if rec.Code != http.StatusUnauthorized {
        t.Fatalf("code=%d, want 401 (no credential configured → fail-closed)", rec.Code)
    }
    if got {
        t.Fatal("protected handler reached with no credential configured")
    }
}

// TestAuthGate_BareTokenTolerated: a bare token (no "Bearer " prefix) is accepted,
// matching the main APIKeyAuth extractAPIKey convention. Guard: if the gate only
// accepted the Bearer-prefixed form, clients sending a bare key would 401.
func TestAuthGate_BareTokenTolerated(t *testing.T) {
    got := false
    h := AuthGate(AuthConfig{Token: "secret"})(reachedHandler(t, &got))
    rec := httptest.NewRecorder()
    req := httptest.NewRequest(http.MethodPost, "/mcp/v1/call", nil)
    req.Header.Set("Authorization", "secret")
    h.ServeHTTP(rec, req)
    if rec.Code != http.StatusOK {
        t.Fatalf("code=%d, want 200 (bare token)", rec.Code)
    }
    if !got {
        t.Fatal("protected handler NOT reached with bare token")
    }
}
