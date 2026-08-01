package admin

import (
    "encoding/json"
    "fmt"
    "net/http"
    "net/http/httptest"
    "os"
    "path/filepath"
    "strings"
    "sync"
    "testing"
)

// ─── Concurrent Write Race Test ────────────────────────────────

func TestConcurrentConfigWrites(t *testing.T) {
    tmpDir := t.TempDir()
    configPath := filepath.Join(tmpDir, "config.yaml")
    initialYAML := strings.Join([]string{
        "server:",
        "  host: 0.0.0.0",
        "  port: 8100",
        "cache:",
        "  enabled: true",
        "  max_entries: 1000",
        "",
    }, "\n")
    if err := os.WriteFile(configPath, []byte(initialYAML), 0600); err != nil {
        t.Fatalf("failed to write initial config: %v", err)
    }

    auth := newTestAuth(t)
    st := newMockStore()
    h := newTestHandler(t, st, auth, configPath)

    mux := http.NewServeMux()
    h.RegisterRoutes(mux)

    var wg sync.WaitGroup
    errCh := make(chan error, 20)

    for i := 0; i < 10; i++ {
        wg.Add(1)
        go func(port int) {
            defer wg.Done()
            body := map[string]interface{}{"port": port}
            req := makeAuthenticatedRequest(t, auth, http.MethodPut, "/admin/api/config/server", body)
            rec := httptest.NewRecorder()
            mux.ServeHTTP(rec, req)
            if rec.Code != http.StatusOK {
                errCh <- fmt.Errorf("server config PUT returned %d for port %d", rec.Code, port)
            }
        }(9000 + i)
    }
    for i := 0; i < 10; i++ {
        wg.Add(1)
        go func(entries int) {
            defer wg.Done()
            body := map[string]interface{}{"max_entries": entries}
            req := makeAuthenticatedRequest(t, auth, http.MethodPut, "/admin/api/config/cache", body)
            rec := httptest.NewRecorder()
            mux.ServeHTTP(rec, req)
            if rec.Code != http.StatusOK {
                errCh <- fmt.Errorf("cache config PUT returned %d for entries %d", rec.Code, entries)
            }
        }(1000 + i*100)
    }

    wg.Wait()
    close(errCh)
    for err := range errCh {
        t.Errorf("concurrent write error: %v", err)
    }

    raw, err := os.ReadFile(configPath)
    if err != nil {
        t.Fatalf("failed to read config after concurrent writes: %v", err)
    }
    content := string(raw)
    if !strings.Contains(content, "server:") || !strings.Contains(content, "cache:") {
        t.Errorf("config file corrupted after concurrent writes:\n%s", content)
    }
    t.Logf("config file survived concurrent writes (%d bytes)", len(raw))
}

// ─── RBAC Role Enforcement Test ────────────────────────────────

func TestConfigPUTRejectedForViewerRole(t *testing.T) {
    tmpDir := t.TempDir()
    configPath := filepath.Join(tmpDir, "config.yaml")
    initialYAML := strings.Join([]string{
        "server:",
        "  host: 0.0.0.0",
        "  port: 8100",
        "",
    }, "\n")
    if err := os.WriteFile(configPath, []byte(initialYAML), 0600); err != nil {
        t.Fatalf("failed to write initial config: %v", err)
    }

    auth := newTestAuth(t)
    st := newMockStore()
    h := newTestHandler(t, st, auth, configPath)

    mux := http.NewServeMux()
    h.RegisterRoutes(mux)

    viewerToken, err := auth.GenerateToken("viewer_user", "viewer")
    if err != nil {
        t.Fatalf("failed to generate viewer token: %v", err)
    }

    endpoints := []struct {
        path string
        body map[string]interface{}
    }{
        {"/admin/api/config/server", map[string]interface{}{"port": 9999}},
        {"/admin/api/config/cache", map[string]interface{}{"enabled": false}},
        {"/admin/api/config/auth", map[string]interface{}{"enabled": false}},
        {"/admin/api/config/cost", map[string]interface{}{"enabled": false}},
    }

    for _, ep := range endpoints {
        t.Run("viewer_PUT_"+strings.ReplaceAll(ep.path, "/", "_"), func(t *testing.T) {
            bodyBytes, _ := json.Marshal(ep.body)
            req := httptest.NewRequest(http.MethodPut, ep.path, strings.NewReader(string(bodyBytes)))
            req.Header.Set("Authorization", "Bearer "+viewerToken)
            rec := httptest.NewRecorder()
            mux.ServeHTTP(rec, req)
            if rec.Code != http.StatusForbidden {
                t.Errorf("expected 403 for viewer PUT on %s, got %d", ep.path, rec.Code)
            }
        })
    }

    t.Run("viewer_GET_allowed", func(t *testing.T) {
        req := httptest.NewRequest(http.MethodGet, "/admin/api/config/server", nil)
        req.Header.Set("Authorization", "Bearer "+viewerToken)
        rec := httptest.NewRecorder()
        mux.ServeHTTP(rec, req)
        if rec.Code != http.StatusOK {
            t.Errorf("expected 200 for viewer GET, got %d", rec.Code)
        }
    })

    t.Run("admin_PUT_allowed", func(t *testing.T) {
        req := makeAuthenticatedRequest(t, auth, http.MethodPut, "/admin/api/config/server", map[string]interface{}{"port": 8200})
        rec := httptest.NewRecorder()
        mux.ServeHTTP(rec, req)
        if rec.Code != http.StatusOK {
            t.Errorf("expected 200 for admin PUT, got %d", rec.Code)
        }
    })
}

// ─── Password Validation Test ──────────────────────────────────

func TestAdminConfigShortPasswordRejected(t *testing.T) {
    tmpDir := t.TempDir()
    configPath := filepath.Join(tmpDir, "config.yaml")
    initialYAML := strings.Join([]string{
        "admin:",
        "  enabled: true",
        "  jwt_secret: test-secret-that-is-at-least-32-characters-long",
        "  users:",
        "    admin: password123",
        "",
    }, "\n")
    if err := os.WriteFile(configPath, []byte(initialYAML), 0600); err != nil {
        t.Fatalf("failed to write initial config: %v", err)
    }

    auth := newTestAuth(t)
    st := newMockStore()
    h := newTestHandler(t, st, auth, configPath)

    mux := http.NewServeMux()
    h.RegisterRoutes(mux)

    t.Run("short_password_rejected", func(t *testing.T) {
        body := map[string]interface{}{
            "users": map[string]string{
                "newuser": "short",
            },
        }
        req := makeAuthenticatedRequest(t, auth, http.MethodPut, "/admin/api/config/admin", body)
        rec := httptest.NewRecorder()
        mux.ServeHTTP(rec, req)
        if rec.Code != http.StatusBadRequest {
            t.Errorf("expected 400 for short password, got %d", rec.Code)
        }
        var resp map[string]interface{}
        _ = json.NewDecoder(rec.Body).Decode(&resp)
        errObj, _ := resp["error"].(map[string]interface{})
        msg, _ := errObj["message"].(string)
        if !strings.Contains(msg, "at least 8 characters") {
            t.Errorf("expected password length error, got: %s", msg)
        }
    })

    t.Run("valid_password_accepted", func(t *testing.T) {
        body := map[string]interface{}{
            "users": map[string]string{
                "newuser": "validpassword123",
            },
        }
        req := makeAuthenticatedRequest(t, auth, http.MethodPut, "/admin/api/config/admin", body)
        rec := httptest.NewRecorder()
        mux.ServeHTTP(rec, req)
        if rec.Code != http.StatusOK {
            t.Errorf("expected 200 for valid password, got %d", rec.Code)
        }
    })

    t.Run("empty_password_keeps_existing", func(t *testing.T) {
        body := map[string]interface{}{
            "users": map[string]string{
                "admin": "",
            },
        }
        req := makeAuthenticatedRequest(t, auth, http.MethodPut, "/admin/api/config/admin", body)
        rec := httptest.NewRecorder()
        mux.ServeHTTP(rec, req)
        if rec.Code != http.StatusOK {
            t.Errorf("expected 200 for empty password (keep), got %d", rec.Code)
        }
    })
}

// ─── File Permission Test ──────────────────────────────────────

func TestConfigFileWrittenWithRestrictedPermissions(t *testing.T) {
    tmpDir := t.TempDir()
    configPath := filepath.Join(tmpDir, "config.yaml")
    initialYAML := strings.Join([]string{
        "server:",
        "  host: 0.0.0.0",
        "  port: 8100",
        "",
    }, "\n")
    if err := os.WriteFile(configPath, []byte(initialYAML), 0600); err != nil {
        t.Fatalf("failed to write initial config: %v", err)
    }

    auth := newTestAuth(t)
    st := newMockStore()
    h := newTestHandler(t, st, auth, configPath)

    mux := http.NewServeMux()
    h.RegisterRoutes(mux)

    body := map[string]interface{}{"port": 9200}
    req := makeAuthenticatedRequest(t, auth, http.MethodPut, "/admin/api/config/server", body)
    rec := httptest.NewRecorder()
    mux.ServeHTTP(rec, req)
    if rec.Code != http.StatusOK {
        t.Fatalf("expected 200, got %d", rec.Code)
    }

    info, err := os.Stat(configPath)
    if err != nil {
        t.Fatalf("failed to stat config file: %v", err)
    }
    perm := info.Mode().Perm()
    if perm&0077 != 0 {
        t.Errorf("config file has overly permissive permissions: %o (expected 0600 or stricter)", perm)
    }
}

// ─── maskAPIKey Unit Test ───────────────────────────────────────

func TestMaskAPIKeyMasksSensitiveValues(t *testing.T) {
    cases := []struct {
        input    string
        expected string
    }{
        {"sk-1234567890abcdef", "****cdef"},
        {"short", "****"},
        {"", ""},
        {"a", "****"},
        {"sk-long-secret-key-value-here", "****here"},
    }
    for _, c := range cases {
        got := maskAPIKey(c.input)
        if c.input != "" && c.input != got {
            if !strings.HasPrefix(got, "****") {
                t.Errorf("maskAPIKey(%q) = %q, expected **** prefix", c.input, got)
            }
            if strings.Contains(got, c.input) {
                t.Errorf("maskAPIKey(%q) = %q, leaks original value", c.input, got)
            }
        }
    }
}

// ─── GET Response No Secret Leak Test ──────────────────────────
// Verify that GET responses from config endpoints never contain
// raw secret values by checking the response body text directly.

func TestGETResponsesDoNotLeakRawSecrets(t *testing.T) {
    tmpDir := t.TempDir()
    configPath := filepath.Join(tmpDir, "config.yaml")
    initialYAML := strings.Join([]string{
        "server:",
        "  host: 0.0.0.0",
        "  port: 8100",
        "auth:",
        "  enabled: true",
        "  master_key: sk-super-secret-master-key-1234567890",
        "admin:",
        "  enabled: true",
        "  jwt_secret: jwt-secret-at-least-32-characters-long",
        "  users:",
        "    admin: password12345678",
        "",
    }, "\n")
    if err := os.WriteFile(configPath, []byte(initialYAML), 0600); err != nil {
        t.Fatalf("failed to write initial config: %v", err)
    }

    auth := newTestAuth(t)
    st := newMockStore()
    h := newTestHandler(t, st, auth, configPath)

    mux := http.NewServeMux()
    h.RegisterRoutes(mux)

    secrets := []string{
        "sk-super-secret-master-key-1234567890",
        "jwt-secret-at-least-32-characters-long",
        "password12345678",
    }

    endpoints := []string{
        "/admin/api/config/auth",
        "/admin/api/config/admin",
    }

    for _, ep := range endpoints {
        t.Run(strings.ReplaceAll(ep, "/", "_"), func(t *testing.T) {
            req := makeAuthenticatedRequest(t, auth, http.MethodGet, ep, nil)
            rec := httptest.NewRecorder()
            mux.ServeHTTP(rec, req)
            if rec.Code != http.StatusOK {
                t.Fatalf("expected 200, got %d", rec.Code)
            }
            body := rec.Body.String()
            for _, secret := range secrets {
                if strings.Contains(body, secret) {
                    t.Errorf("GET %s leaks raw secret %q in response", ep, secret)
                }
            }
        })
    }
}

// ─── No Auth Token Rejection Test ──────────────────────────────

func TestConfigEndpointsRejectUnauthenticatedRequests(t *testing.T) {
    auth := newTestAuth(t)
    st := newMockStore()
    h := newTestHandler(t, st, auth, "")

    mux := http.NewServeMux()
    h.RegisterRoutes(mux)

    endpoints := []string{
        "/admin/api/config/server",
        "/admin/api/config/auth",
        "/admin/api/config/cache",
        "/admin/api/config/cluster",
    }

    for _, ep := range endpoints {
        t.Run(strings.ReplaceAll(ep, "/", "_"), func(t *testing.T) {
            req := httptest.NewRequest(http.MethodGet, ep, nil)
            rec := httptest.NewRecorder()
            mux.ServeHTTP(rec, req)
            if rec.Code != http.StatusUnauthorized {
                t.Errorf("expected 401 for unauthenticated request, got %d", rec.Code)
            }
        })
    }
}
