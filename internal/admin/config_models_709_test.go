package admin

import (
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "os"
    "testing"
)

// #709 test config: minimal but complete enough for config.Load, with a
// backends models list, models.supply routes, a cloud_models allowlist and
// one API key with an allowed_models entry.
const models709TestYAML = `
server:
    host: "127.0.0.1"
    port: 8100
    log_level: "info"
auth:
    enabled: true
    master_key: "sk-test-master-key-12345"
    passthrough: false
    api_keys:
        - key: "sk-test-key-1234567890"
          name: "demo"
          rpm: 60
          tpm: 100000
          allowed_models: ["glm-5", "glm*"]
routing:
    token_threshold: 8000
    cloud_models: ["glm-5", "deepseek-chat"]
models:
    supply:
        glm-5:
            - zhipu
            - fusion-mlx
        deepseek-chat:
            - deepseek
backends:
    zhipu:
        type: "anthropic"
        base_url: "https://example.invalid"
        api_key: "zk-test"
        enabled: true
        models: ["glm-5", "glm-4.7"]
    deepseek:
        type: "openai"
        base_url: "https://example.invalid"
        api_key: "dk-test"
        enabled: true
        models: ["deepseek-chat"]
`

func setupModels709Test(t *testing.T) (*AdminAuth, *mockStore) {
    t.Helper()
    auth := newTestAuth(t)
    ms := newMockStore()
    loadTestConfig(t, models709TestYAML)
    return auth, ms
}

// setupModels709TestWithFile also writes the YAML to a temp file so PUT
// handlers that persist through updateYAMLSection have a real config path.
func setupModels709TestWithFile(t *testing.T) (*AdminAuth, *mockStore, string) {
    t.Helper()
    auth := newTestAuth(t)
    ms := newMockStore()
    configPath := writeTempConfig(t, models709TestYAML)
    loadTestConfig(t, models709TestYAML)
    return auth, ms, configPath
}

func writeTempConfig(t *testing.T, yamlContent string) string {
    t.Helper()
    tmpDir := t.TempDir()
    configPath := tmpDir + "/config.yaml"
    if err := os.WriteFile(configPath, []byte(yamlContent), 0644); err != nil {
        t.Fatalf("failed to write test config: %v", err)
    }
    return configPath
}

// GET /admin/api/config/models must expose supply routes, the cloud
// allowlist, per-key allowlists, the merged catalog and the mutation-lock
// state — the read half of the #709 console surface.
func TestHandleModelsConfig_Get(t *testing.T) {
    auth, ms := setupModels709Test(t)
    h := newTestHandler(t, ms, auth, "")

    req := makeAuthenticatedRequest(t, auth, http.MethodGet, "/admin/api/config/models", nil)
    rec := httptest.NewRecorder()
    h.handleModelsConfig(rec, req)
    if rec.Code != http.StatusOK {
        t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
    }
    var body modelsConfigResponse
    if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
        t.Fatalf("decode response: %v", err)
    }

    if len(body.Supply["glm-5"]) != 2 || body.Supply["glm-5"][0] != "zhipu" {
        t.Errorf("supply[glm-5] = %v, want [zhipu fusion-mlx]", body.Supply["glm-5"])
    }
    if len(body.CloudModels) != 2 {
        t.Errorf("cloud_models = %v, want 2 entries", body.CloudModels)
    }
    if len(body.Keys) != 1 || body.Keys[0].Name != "demo" {
        t.Errorf("keys = %+v, want one demo key", body.Keys)
    }
    for _, src := range []string{"backend:zhipu", "models.supply", "cloud_models"} {
        found := false
        for _, s := range body.Catalog["glm-5"] {
            if s == src {
                found = true
            }
        }
        if !found {
            t.Errorf("catalog[glm-5] missing source %q, got %v", src, body.Catalog["glm-5"])
        }
    }
    if body.MutationLock {
        t.Errorf("mutation_lock should default false in test config")
    }

    // Route-level auth is enforced by the requireAdminRole wrapper in
    // RegisterRoutes (same shape as every other /admin/api/config domain);
    // wrong method must 405.
    req = httptest.NewRequest(http.MethodPost, "/admin/api/config/models", nil)
    rec = httptest.NewRecorder()
    h.handleModelsConfig(rec, req)
    if rec.Code != http.StatusMethodNotAllowed {
        t.Errorf("POST = %d, want 405 (read-only surface)", rec.Code)
    }
}

// #709: allowlist validation is a WARNING, never a write rejection —
// wildcard prefixes and future model names are legitimate.
func TestValidateAllowedModels(t *testing.T) {
    catalog := map[string][]string{"glm-5": {"backend:zhipu"}}

    warnings := validateAllowedModels([]string{"glm-5", "glm*", ""}, catalog)
    if len(warnings) != 1 || warnings[0] != "empty allowed_models entry (drop it or fill in a model name)" {
        t.Errorf("warnings = %v, want only the empty-entry warning", warnings)
    }

    warnings = validateAllowedModels([]string{"glm*extra", "unknown-model"}, catalog)
    if len(warnings) != 2 {
        t.Fatalf("warnings = %v, want 2 (bad wildcard + catalog miss)", warnings)
    }
}

// The AuthConfig PUT must attach allowlist warnings to its response so the
// editor can surface them inline (#709).
func TestHandleAuthConfig_PutWarnings(t *testing.T) {
    auth, ms, configPath := setupModels709TestWithFile(t)
    h := newTestHandler(t, ms, auth, configPath)

    req := makeAuthenticatedRequest(t, auth, http.MethodPut, "/admin/api/config/auth", map[string]interface{}{
        "api_keys": []map[string]interface{}{
            {
                "name":           "demo",
                "key":            "",
                "rpm":            60,
                "tpm":            100000,
                "allowed_models": []string{"glm-5", "typo-modle"},
            },
        },
    })
    rec := httptest.NewRecorder()
    h.handleAuthConfig(rec, req)
    if rec.Code != http.StatusOK {
        t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
    }
    var body authConfigResponse
    if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
        t.Fatalf("decode response: %v", err)
    }
    if len(body.Warnings) != 1 {
        t.Fatalf("warnings = %v, want 1 catalog-miss warning for typo-modle", body.Warnings)
    }
    if !contains(body.Warnings[0], "typo-modle") || !contains(body.Warnings[0], "key demo") {
        t.Errorf("warning text = %q, want key demo + typo-modle", body.Warnings[0])
    }
}

func contains(haystack, needle string) bool {
    for i := 0; i+len(needle) <= len(haystack); i++ {
        if haystack[i:i+len(needle)] == needle {
            return true
        }
    }
    return false
}
