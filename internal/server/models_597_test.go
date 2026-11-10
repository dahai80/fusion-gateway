package server

import (
    "context"
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "testing"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/middleware"
)

// Issue #597: /v1/models must filter the catalog by the authenticated key's
// allowed_models. Listed ⇒ callable (same matcher as the inference path);
// a 403-model must not be listed. Master / keys without restrictions keep
// the full list; shell-session principals (no KeyConfig) are unaffected.

func models597IDs(t *testing.T, s *Server, key string, allowed []string) []string {
    t.Helper()
    req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
    if key != "" {
        req.Header.Set("Authorization", "Bearer "+key)
    }
    // Inject the same principal the auth middleware would stamp for this key
    // so the filter path is exercised without a full chain build. The
    // handler reads the snapshot from context — bind the test server's.
    ctx := config.WithSnapshot(req.Context(), s.cfg)
    c2, p := middleware.EnsurePrincipal(ctx)
    p.AuthMethod = "apikey"
    p.KeyConfig = &config.AuthKeyConfig{
        Key:           key,
        Name:          "restricted",
        AllowedModels: allowed,
        RPM:           30,
        TPM:           20000,
    }
    req = req.WithContext(c2)
    w := httptest.NewRecorder()
    s.handleModels(w, req)
    if w.Code != http.StatusOK {
        t.Fatalf("models: %d %s", w.Code, w.Body.String())
    }
    var resp struct {
        Data []struct {
            ID string `json:"id"`
        } `json:"data"`
    }
    if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
        t.Fatalf("json: %v", err)
    }
    ids := make([]string, 0, len(resp.Data))
    for _, m := range resp.Data {
        ids = append(ids, m.ID)
    }
    return ids
}

func contains597(ids []string, id string) bool {
    for _, v := range ids {
        if v == id {
            return true
        }
    }
    return false
}

func Test597_RestrictedKeySeesOnlyAllowedModels(t *testing.T) {
    s := newTestServer()
    s.cfg.Config.Auth.Enabled = true
    s.cfg.Config.Routing.CloudModels = []string{"glm-5", "BAAI--bge-m3", "whisper-large"}
    ids := models597IDs(t, s, "fk-restricted", []string{"glm*", "mlx-community*"})
    if contains597(ids, "BAAI--bge-m3") {
        t.Fatalf("embedding model must not be listed to a glm*/mlx-community* key: %v", ids)
    }
    if contains597(ids, "whisper-large") {
        t.Fatalf("tts model must not be listed to a glm*/mlx-community* key: %v", ids)
    }
    if !contains597(ids, "glm-5") {
        t.Fatalf("allowed glm-5 must stay listed: %v", ids)
    }
}

func Test597_UnrestrictedKeySeesAll(t *testing.T) {
    s := newTestServer()
    s.cfg.Config.Auth.Enabled = true
    s.cfg.Config.Routing.CloudModels = []string{"glm-5", "BAAI--bge-m3", "whisper-large"}
    ids := models597IDs(t, s, "fk-open", nil)
    for _, want := range []string{"glm-5", "BAAI--bge-m3", "whisper-large"} {
        if !contains597(ids, want) {
            t.Fatalf("unrestricted key must see %s; got %v", want, ids)
        }
    }
}

func Test597_MasterSeesAll(t *testing.T) {
    s := newTestServer()
    s.cfg.Config.Auth.Enabled = true
    s.cfg.Config.Routing.CloudModels = []string{"glm-5", "BAAI--bge-m3"}
    ctx := context.Background()
    _ = ctx
    req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
    c2, p := middleware.EnsurePrincipal(config.WithSnapshot(req.Context(), s.cfg))
    p.AuthMethod = "apikey"
    p.IsMaster = true
    p.KeyConfig = &config.AuthKeyConfig{Key: "mk", Name: "master"}
    req = req.WithContext(c2)
    w := httptest.NewRecorder()
    s.handleModels(w, req)
    var resp struct {
        Data []struct {
            ID string `json:"id"`
        } `json:"data"`
    }
    _ = json.Unmarshal(w.Body.Bytes(), &resp)
    found := map[string]bool{}
    for _, m := range resp.Data {
        found[m.ID] = true
    }
    for _, want := range []string{"glm-5", "BAAI--bge-m3"} {
        if !found[want] {
            t.Fatalf("master must see %s; got %v", want, found)
        }
    }
}

func Test597_ListedImpliesCallable(t *testing.T) {
    s := newTestServer()
    s.cfg.Config.Auth.Enabled = true
    s.cfg.Config.Routing.CloudModels = []string{"glm-5", "claude-fable-5", "BAAI--bge-m3"}
    allowed := []string{"glm*"}
    ids := models597IDs(t, s, "fk-restricted", allowed)
    for _, id := range ids {
        if id == "claude-fable-5" || id == "BAAI--bge-m3" {
            t.Fatalf("model %s listed but would 403 on chat (allowed=%v)", id, allowed)
        }
    }
}
