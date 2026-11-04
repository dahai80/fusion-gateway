package server

import (
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "testing"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

// tierTestServer builds a minimal Server with the given tier + auth plane.
// authDisabled mirrors a misconfigured lan deployment: the inner chain would
// pass everything, so only the lan guard can reject.
func tierTestServer(tier string, authEnabled bool) *Server {
    cfg := &config.ConfigSnapshot{Config: config.DefaultConfig()}
    cfg.Config.Network.Tier = tier
    cfg.Config.Auth.Enabled = authEnabled
    cfg.Config.Auth.MasterKey = "tier-master-key"
    s := &Server{cfg: cfg}
    return s
}

// #493 AC: lan tier — an unauthenticated request to a non-allowlisted route
// is rejected 401 even when the inner auth plane would pass it.
func TestLanGuard_RejectsAnonymous(t *testing.T) {
    s := tierTestServer("lan", false)
    inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK) // would-be anonymous handler
    })
    h := s.lanGuard(inner)
    r := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
    w := httptest.NewRecorder()
    h.ServeHTTP(w, r)
    if w.Code != http.StatusUnauthorized {
        t.Fatalf("lan guard must 401 anonymous /v1/models, got %d", w.Code)
    }
}

// #493 AC: allowlisted surfaces stay anonymous (probes, admin SPA, shell
// session login).
func TestLanGuard_AllowlistAnonymous(t *testing.T) {
    s := tierTestServer("lan", false)
    inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
    })
    h := s.lanGuard(inner)
    for _, path := range []string{"/healthz", "/readyz", "/livez", "/health", "/admin/", "/api/v1/shell/session", "/gateway/v1/managed-settings"} {
        r := httptest.NewRequest(http.MethodGet, path, nil)
        w := httptest.NewRecorder()
        h.ServeHTTP(w, r)
        if w.Code != http.StatusOK {
            t.Fatalf("allowlisted %s must pass, got %d", path, w.Code)
        }
    }
}

// #493 AC: any credential (api key, shell session cookie) passes the guard —
// the route's own gate still does the real validation.
func TestLanGuard_CredentialPasses(t *testing.T) {
    s := tierTestServer("lan", true)
    inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
    })
    h := s.lanGuard(inner)
    r := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
    r.Header.Set("Authorization", "Bearer some-key")
    w := httptest.NewRecorder()
    h.ServeHTTP(w, r)
    if w.Code != http.StatusOK {
        t.Fatalf("bearer credential must pass the guard, got %d", w.Code)
    }
    r2 := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
    r2.AddCookie(&http.Cookie{Name: shellSessionCookieName, Value: "tok"})
    w2 := httptest.NewRecorder()
    h.ServeHTTP(w2, r2)
    if w2.Code != http.StatusOK {
        t.Fatalf("shell session cookie must pass the guard, got %d", w2.Code)
    }
}

// #493 AC: local tier behavior identical to today — no guard, auth-disabled
// configs keep passing (regression).
func TestLanGuard_LocalTierUnchanged(t *testing.T) {
    s := tierTestServer("local", false)
    if s.lanGuardHandler() != nil {
        t.Fatal("local tier must not arm the guard")
    }
}

// #493 AC: service key registration — a module carrying its own
// FUSION_SERVICE_KEY_<MODULE> credential can register itself.
func TestServiceKey_Registration(t *testing.T) {
    s := tierTestServer("local", true)
    s.cfg.Config.Shell.ServiceKeys = map[string]string{"fusion-rag": "svc-rag-key"}
    body := `{"id":"fusion-rag","title":"RAG","path":"/fusion-rag","entry":"http://127.0.0.1:11432/shell/fusion-rag/","devices":["desktop"],"category":"platform"}`
    r := httptest.NewRequest(http.MethodPost, "/api/v1/shell/manifests", nil)
    r.Header.Set("Authorization", "Bearer svc-rag-key")
    allowed := s.serviceKeyAllowed(r, "fusion-rag")
    if !allowed {
        t.Fatal("module's own service key must authenticate its registration")
    }
    r2 := httptest.NewRequest(http.MethodPost, "/api/v1/shell/manifests", nil)
    r2.Header.Set("Authorization", "Bearer svc-other-key")
    if s.serviceKeyAllowed(r2, "fusion-rag") {
        t.Fatal("a foreign key must not authenticate another module's registration")
    }
    var probe struct {
        ID string `json:"id"`
    }
    _ = json.Unmarshal([]byte(body), &probe)
}
