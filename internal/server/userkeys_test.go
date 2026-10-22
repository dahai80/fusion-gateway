package server

import (
    "bytes"
    "crypto/sha256"
    "encoding/hex"
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "strings"
    "testing"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/store"
)

// #262 K2: /api/v1/keys CRUD — fk- issuance, tail-4 display, hash-only
// storage, rotate grace fields, soft revoke.

func userKeyRequest(t *testing.T, s *Server, method, path string, body any) *httptest.ResponseRecorder {
    t.Helper()
    var reader *bytes.Reader
    if body != nil {
        raw, err := json.Marshal(body)
        if err != nil {
            t.Fatalf("marshal body: %v", err)
        }
        reader = bytes.NewReader(raw)
    } else {
        reader = bytes.NewReader(nil)
    }
    req := httptest.NewRequest(method, path, reader)
    req.Header.Set("Authorization", "Bearer "+s.cfg.Config.Auth.MasterKey)
    rec := httptest.NewRecorder()
    // Dispatch through the same mux wiring the real server uses so the
    // withMasterKey gate and Go 1.22 path patterns are exercised verbatim.
    s.mux.ServeHTTP(rec, req)
    return rec
}

func TestUserKeys_CRUDLifecycle(t *testing.T) {
    s := newTestServer()
    s.cfg.Config.Auth.MasterKey = "mk-userkeys-test"
    mux := http.NewServeMux()
    s.registerUserKeyRoutes(mux)
    s.mux = mux

    // Create requires scopes.
    rec := userKeyRequest(t, s, http.MethodPost, "/api/v1/keys", map[string]any{"name": "ext-runner"})
    if rec.Code != http.StatusBadRequest {
        t.Fatalf("create without scopes: %d, want 400", rec.Code)
    }

    // Create.
    rec = userKeyRequest(t, s, http.MethodPost, "/api/v1/keys", map[string]any{
        "name": "ext-runner", "scopes": []string{"chat", "rag:embed"},
    })
    if rec.Code != http.StatusCreated {
        t.Fatalf("create: %d body=%s, want 201", rec.Code, rec.Body.String())
    }
    var created userKeyResponse
    if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
        t.Fatalf("decode create response: %v", err)
    }
    if !strings.HasPrefix(created.RawKey, "fk-") || len(created.RawKey) != len("fk-")+48 {
        t.Fatalf("expected fk-<48hex>, got %q", created.RawKey)
    }
    if !strings.HasPrefix(created.KeyPrefix, "fk-…") || len(created.KeyPrefix) != len("fk-…")+4 {
        t.Fatalf("expected tail-4 display prefix, got %q", created.KeyPrefix)
    }

    // Duplicate name rejected.
    rec = userKeyRequest(t, s, http.MethodPost, "/api/v1/keys", map[string]any{
        "name": "ext-runner", "scopes": []string{"chat"},
    })
    if rec.Code != http.StatusConflict {
        t.Fatalf("duplicate create: %d, want 409", rec.Code)
    }

    // List: never exposes hashes or plaintext; carries scopes.
    rec = userKeyRequest(t, s, http.MethodGet, "/api/v1/keys", nil)
    if rec.Code != http.StatusOK {
        t.Fatalf("list: %d", rec.Code)
    }
    body := rec.Body.String()
    if strings.Contains(body, "key_hash") || strings.Contains(body, "raw_key") {
        t.Fatalf("list response leaks key material: %s", body)
    }
    if !strings.Contains(body, "ext-runner") || !strings.Contains(body, "rag:embed") {
        t.Fatalf("list missing key/scopes: %s", body)
    }

    // Rotate: new plaintext once, prev hash + rotated-at recorded.
    rec = userKeyRequest(t, s, http.MethodPost, "/api/v1/keys/ext-runner/rotate", nil)
    if rec.Code != http.StatusOK {
        t.Fatalf("rotate: %d body=%s", rec.Code, rec.Body.String())
    }
    var rotated userKeyResponse
    _ = json.Unmarshal(rec.Body.Bytes(), &rotated)
    if rotated.RawKey == "" || rotated.RawKey == created.RawKey {
        t.Fatalf("rotate must return a new plaintext once, got %q", rotated.RawKey)
    }
    entry, err := s.store.GetKey("ext-runner")
    if err != nil {
        t.Fatalf("get after rotate: %v", err)
    }
    if entry.KeyPrevHash == "" || entry.KeyRotatedAt == nil {
        t.Fatalf("rotate must record prev hash + rotated-at, got %+v", entry)
    }
    sum := sha256.Sum256([]byte(created.RawKey))
    if entry.KeyPrevHash != hex.EncodeToString(sum[:]) {
        t.Fatalf("prev hash mismatch after rotate")
    }
    // Old hash still resolves to the entry inside the grace window
    // (dual-active, memory store indexes KeyPrevHash).
    if _, err := s.store.GetKeyByHash(entry.KeyPrevHash); err != nil {
        t.Fatalf("prev hash must resolve to entry inside grace: %v", err)
    }

    // Revoke: soft — status flips, entry kept.
    rec = userKeyRequest(t, s, http.MethodDelete, "/api/v1/keys/ext-runner", nil)
    if rec.Code != http.StatusOK {
        t.Fatalf("revoke: %d", rec.Code)
    }
    entry, err = s.store.GetKey("ext-runner")
    if err != nil {
        t.Fatalf("revoked key must remain (soft delete): %v", err)
    }
    if entry.Status != "revoked" {
        t.Fatalf("status after revoke = %q, want revoked", entry.Status)
    }
    // Second revoke: 409.
    rec = userKeyRequest(t, s, http.MethodDelete, "/api/v1/keys/ext-runner", nil)
    if rec.Code != http.StatusConflict {
        t.Fatalf("double revoke: %d, want 409", rec.Code)
    }
    // Rotate on revoked key: 409.
    rec = userKeyRequest(t, s, http.MethodPost, "/api/v1/keys/ext-runner/rotate", nil)
    if rec.Code != http.StatusConflict {
        t.Fatalf("rotate after revoke: %d, want 409", rec.Code)
    }
}

func TestUserKeys_MasterGate(t *testing.T) {
    s := newTestServer()
    s.cfg.Config.Auth.MasterKey = "mk-userkeys-test"
    mux := http.NewServeMux()
    s.registerUserKeyRoutes(mux)
    s.mux = mux

    // Anonymous: 401. (raw request — the userKeyRequest helper always sends
    // the master Bearer.)
    req := httptest.NewRequest(http.MethodGet, "/api/v1/keys", nil)
    rec := httptest.NewRecorder()
    s.mux.ServeHTTP(rec, req)
    if rec.Code != http.StatusUnauthorized {
        t.Fatalf("anonymous list: %d, want 401", rec.Code)
    }
    // Wrong key: 401.
    req = httptest.NewRequest(http.MethodGet, "/api/v1/keys", nil)
    req.Header.Set("Authorization", "Bearer fk-not-the-master")
    rr := httptest.NewRecorder()
    s.mux.ServeHTTP(rr, req)
    if rr.Code != http.StatusUnauthorized {
        t.Fatalf("non-master list: %d, want 401", rr.Code)
    }
    // fk- inference key must NOT open the CRUD surface even though it
    // authenticates on /v1/*: withMasterKey is the sole gate here.
    req = httptest.NewRequest(http.MethodPost, "/api/v1/keys", strings.NewReader(`{"name":"x","scopes":["chat"]}`))
    req.Header.Set("Authorization", "Bearer fk-anything")
    rr = httptest.NewRecorder()
    s.mux.ServeHTTP(rr, req)
    if rr.Code != http.StatusUnauthorized {
        t.Fatalf("fk- key create: %d, want 401", rr.Code)
    }
}

func TestUserKeys_TenantBinding(t *testing.T) {
    s := newTestServer()
    s.cfg.Config.Auth.MasterKey = "mk-userkeys-test"
    mux := http.NewServeMux()
    s.registerUserKeyRoutes(mux)
    s.mux = mux

    // Unknown tenant: 400, nothing created.
    rec := userKeyRequest(t, s, http.MethodPost, "/api/v1/keys", map[string]any{
        "name": "bound-key", "scopes": []string{"chat"}, "tenant": "no-such-team",
    })
    if rec.Code != http.StatusBadRequest {
        t.Fatalf("unknown tenant: %d, want 400", rec.Code)
    }

    // Real tenant: created + bound.
    if err := s.store.CreateTeam(&store.Team{ID: "team-a", Name: "Team A"}); err != nil {
        t.Fatalf("create team: %v", err)
    }
    rec = userKeyRequest(t, s, http.MethodPost, "/api/v1/keys", map[string]any{
        "name": "bound-key", "scopes": []string{"chat"}, "tenant": "team-a",
    })
    if rec.Code != http.StatusCreated {
        t.Fatalf("create with tenant: %d body=%s", rec.Code, rec.Body.String())
    }
    var created userKeyResponse
    _ = json.Unmarshal(rec.Body.Bytes(), &created)
    team, err := s.store.GetTeamByKey(created.RawKey)
    if err != nil || team == nil || team.ID != "team-a" {
        t.Fatalf("key must be bound to team-a, got %v err=%v", team, err)
    }
    entry, err := s.store.GetKey("bound-key")
    if err != nil || entry.Metadata["tenant"] != "team-a" {
        t.Fatalf("tenant metadata missing: %+v err=%v", entry, err)
    }
}

// #271: session-gated /api/v1/shell/keys — anonymous 401, non-admin 403,
// admin session reaches the K2 handlers unchanged.
func TestShellKeys_SessionGated(t *testing.T) {
    s := newTestServer()
    s.cfg.Config.Shell.Enabled = true
    s.cfg.Config.Shell.Session.Secret = "test-shell-secret-at-least-32-chars!"
    s.cfg.Config.Shell.Session.Users = []config.ShellUser{
        {Username: "alice", Password: "alicepw", Roles: []string{"user"}},
        {Username: "boss", Password: "bosspw", Roles: []string{"admin"}},
    }
    s.setupShell()
    mux := http.NewServeMux()
    s.registerShellRoutes(mux)
    s.mux = mux

    shellKeysReq := func(method, path, token string, body any) *httptest.ResponseRecorder {
        t.Helper()
        var reader *bytes.Reader
        if body != nil {
            raw, err := json.Marshal(body)
            if err != nil {
                t.Fatalf("marshal body: %v", err)
            }
            reader = bytes.NewReader(raw)
        } else {
            reader = bytes.NewReader(nil)
        }
        req := httptest.NewRequest(method, path, reader)
        if token != "" {
            req.Header.Set("Authorization", "Bearer "+token)
        }
        rec := httptest.NewRecorder()
        s.mux.ServeHTTP(rec, req)
        return rec
    }

    // Anonymous: 401 from the shell session gate.
    rec := shellKeysReq(http.MethodGet, "/api/v1/shell/keys", "", nil)
    if rec.Code != http.StatusUnauthorized {
        t.Fatalf("anonymous list: %d, want 401", rec.Code)
    }

    // Non-admin session: 403 from the admin gate.
    _, alice := loginShell(t, s, "alice", "alicepw")
    aliceTok, _ := alice["token"].(string)
    rec = shellKeysReq(http.MethodGet, "/api/v1/shell/keys", aliceTok, nil)
    if rec.Code != http.StatusForbidden {
        t.Fatalf("non-admin list: %d body=%s, want 403", rec.Code, rec.Body.String())
    }

    // Admin session: full create → list → rotate → revoke lifecycle.
    _, boss := loginShell(t, s, "boss", "bosspw")
    bossTok, _ := boss["token"].(string)
    rec = shellKeysReq(http.MethodPost, "/api/v1/shell/keys", bossTok,
        map[string]any{"name": "shell-made", "scopes": []string{"chat"}})
    if rec.Code != http.StatusCreated {
        t.Fatalf("admin create: %d body=%s, want 201", rec.Code, rec.Body.String())
    }
    var created userKeyResponse
    if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
        t.Fatalf("create response: %v", err)
    }
    if created.RawKey == "" || !strings.HasPrefix(created.RawKey, "fk-") {
        t.Fatalf("create did not return one-time plaintext: %+v", created)
    }

    rec = shellKeysReq(http.MethodGet, "/api/v1/shell/keys", bossTok, nil)
    if rec.Code != http.StatusOK {
        t.Fatalf("admin list: %d, want 200", rec.Code)
    }
    var listed []userKeyListEntry
    if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
        t.Fatalf("list decode: %v body=%s", err, rec.Body.String())
    }
    if len(listed) != 1 || listed[0].Name != "shell-made" {
        t.Fatalf("list = %+v, want one shell-made key", listed)
    }
    if strings.Contains(rec.Body.String(), created.RawKey) {
        t.Fatalf("list leaked plaintext: %s", rec.Body.String())
    }

    rec = shellKeysReq(http.MethodPost, "/api/v1/shell/keys/shell-made/rotate", bossTok, nil)
    if rec.Code != http.StatusOK {
        t.Fatalf("admin rotate: %d body=%s, want 200", rec.Code, rec.Body.String())
    }
    var rotated userKeyResponse
    if err := json.Unmarshal(rec.Body.Bytes(), &rotated); err != nil {
        t.Fatalf("rotate response: %v", err)
    }
    if rotated.RawKey == created.RawKey {
        t.Fatalf("rotate returned the same plaintext")
    }

    rec = shellKeysReq(http.MethodDelete, "/api/v1/shell/keys/shell-made", bossTok, nil)
    if rec.Code != http.StatusOK {
        t.Fatalf("admin revoke: %d body=%s, want 200", rec.Code, rec.Body.String())
    }
}
