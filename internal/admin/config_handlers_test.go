package admin

import (
    "encoding/json"
    "fmt"
    "net/http"
    "net/http/httptest"
    "os"
    "path/filepath"
    "testing"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

const fullTestYAML = `
server:
  host: "0.0.0.0"
  port: 8100
  log_level: "info"
  graceful_shutdown_timeout: 15
  max_request_body_size: 5242880
  enable_pprof: false
routing:
  token_threshold: 8000
  output_input_ratio_threshold: 0.6
  local_priority:
    enabled: true
    max_system_memory_ratio: 0.9
    max_mlx_memory_ratio: 0.7
    max_concurrent: 8
  circuit_breaker:
    failure_threshold: 5
  fallback:
    enabled: false
    cloud_default: "openai"
  ratio_tiers:
    enabled: false
    rules: []
  token_tiers:
    enabled: false
    metric: "total"
    rules: []
  rate_limit:
    enabled: true
    global_rpm: 100
    global_tpm: 100000
    key_enforcement: true
  retry:
    max_retries: 3
    initial_backoff: "1s"
    max_backoff: "30s"
    retryable_status_codes: [429, 500, 502, 503]
  negotiation:
    disable_fusion_mlx_routing: true
    route_header: "X-Fusion-Route"
    route_header_value: "gateway-decision"
auth:
  enabled: true
  master_key: "sk-test-master-key-12345"
  passthrough: false
  api_keys:
    - key: "sk-test-key-1234567890"
      name: "test-key"
      rpm: 60
      tpm: 100000
      allowed_models: ["gpt-4", "claude-3"]
      allowed_backends: ["openai"]
      expires_at: ""
      budget_limit: 0
cache:
  enabled: true
  max_entries: 1000
  ttl: "5m"
  max_memory_mb: 512
  backend: "memory"
  warmup_file: ""
  redis:
    addr: "localhost:6379"
    password: "redis-secret-pass"
    db: 0
    pool_size: 10
cost:
  enabled: true
  pricing_file: "pricing.yaml"
  budget_alert_threshold: 0.8
cost_markup:
  enabled: false
  global_markup: 0.0
pii:
  enabled: true
  action: "mask"
  patterns:
    - name: "email"
      regex: "[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\\.[a-zA-Z]{2,}"
    - name: "phone"
      regex: "\\d{3}-\\d{3}-\\d{4}"
cloud_routing:
  strategy: "weighted"
  cloud_weights:
    openai: 50
    claude: 30
    volcengine: 20
hardware:
  enabled: true
  collect_interval: "2s"
  iokit:
    enabled: true
  gopsutil:
    enabled: true
  mlx_metrics:
    enabled: true
    interval: "5s"
  swap:
    page_rate_sampling: true
    page_rate_threshold: 100
  collection_error_protection: true
tokenizer:
  provider: "local"
  default_max_tokens_strategy: "context_window_ratio"
  context_window_ratio: 0.1
  min_max_tokens: 256
  vision_token_estimate: 256
  scene_presets:
    chat: 1024
    code: 2048
    tool_call: 512
  calibration:
    enabled: true
    sample_interval: 1000
    sample_size: 10
    deviation_threshold: 0.02
    auto_switch_threshold: 0.05
observability:
  log_format: "json"
  log_file: ""
  log_rotation_max_size: 100
  log_rotation_max_backups: 3
  metrics_enabled: true
  metrics_path: "/metrics"
  audit_log_enabled: false
  config_audit_log: false
  config_audit_file: ""
  otel_enabled: false
  otel_endpoint: ""
  otel_protocol: "grpc"
  otel_service_name: "fusion-gateway"
cors:
  allowed_origins: ["*"]
  allowed_methods: ["GET", "POST", "PUT", "DELETE"]
  allowed_headers: ["Authorization", "Content-Type"]
hot_reload:
  enabled: true
  watch_path: ""
  debounce: "1s"
  versioning: true
  breaker_drain_timeout: "5s"
  breaker_warmup_success: 3
cluster:
  enabled: false
  mode: "standalone"
  nodes:
    - id: "node-1"
      address: "http://localhost:8101"
      gpu: "M2"
      memory_gb: 16
  master:
    address: ""
    shared_token: "cluster-shared-secret-token"
  load_balancer: "round_robin"
  health_check_interval: "10s"
  failure_threshold: 3
  recovery_interval: "30s"
realtime:
  enabled: false
  backend_url: ""
  api_key: "rt-secret-api-key-abcdef"
  max_message_mb: 10
admin:
  enabled: true
  listen: ":9090"
  log_max_len: 10000
  jwt_secret: "test-jwt-secret-that-is-at-least-32-characters-long"
  users:
    admin: "password12345678"
oidc:
  enabled: false
  issuer: ""
  client_id: ""
  audiences: ""
  scopes: ""
  claim_mappings: ""
rbac:
  enabled: false
  default_role: "viewer"
team:
  enabled: false
  default_team: "default"
semantic_cache:
  enabled: false
  similarity_threshold: 0.95
  max_entries: 1000
  provider: ""
  endpoint: ""
prompt_injection:
  enabled: true
  action: "block"
  provider: "local"
  api_key: "pi-secret-api-key-xyz123"
  threshold: 0.8
batch:
  enabled: false
  max_batch_size: 100
  poll_interval: "5s"
  timeout: "60s"
store:
  backend: "memory"
  redis:
    addr: "localhost:6379"
    password: "store-redis-pass"
    db: 0
    pool_size: 10
validation:
  base_url_conflict_check: false
`

func setupConfigTest(t *testing.T) (*AdminAuth, *mockStore, string) {
    t.Helper()
    auth := newTestAuth(t)
    ms := newMockStore()
    loadTestConfig(t, fullTestYAML)
    return auth, ms, ""
}

func setupConfigTestWithFile(t *testing.T) (*AdminAuth, *mockStore, string) {
    t.Helper()
    auth := newTestAuth(t)
    ms := newMockStore()
    tmpDir := t.TempDir()
    configPath := filepath.Join(tmpDir, "config.yaml")
    if err := os.WriteFile(configPath, []byte(fullTestYAML), 0644); err != nil {
        t.Fatalf("failed to write config file: %v", err)
    }
    loadTestConfig(t, fullTestYAML)
    return auth, ms, configPath
}

func readYAMLSection(t *testing.T, configPath, section string) map[string]interface{} {
    t.Helper()
    doc, err := readYAMLDoc(configPath)
    if err != nil {
        t.Fatalf("failed to read YAML: %v", err)
    }
    sec, ok := doc[section]
    if !ok || sec == nil {
        return map[string]interface{}{}
    }
    m, ok := sec.(map[string]interface{})
    if !ok {
        return map[string]interface{}{}
    }
    return m
}

func readYAMLNestedSection(t *testing.T, configPath string, keys ...string) map[string]interface{} {
    t.Helper()
    doc, err := readYAMLDoc(configPath)
    if err != nil {
        t.Fatalf("failed to read YAML: %v", err)
    }
    sec := getOrCreateSection(doc, keys...)
    return sec
}

// ─── Server Config Tests ────────────────────────────────────────

func TestHandleServerConfig(t *testing.T) {
    auth, ms, _ := setupConfigTest(t)

    t.Run("get", func(t *testing.T) {
        h := newTestHandler(t, ms, auth, "")
        req := makeAuthenticatedRequest(t, auth, http.MethodGet, "/admin/api/config/server", nil)
        rec := httptest.NewRecorder()
        h.handleServerConfig(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
        }
        body := decodeResponse(t, rec)
        if body["host"] != "0.0.0.0" {
            t.Errorf("expected host 0.0.0.0, got %v", body["host"])
        }
        if body["port"] != float64(8100) {
            t.Errorf("expected port 8100, got %v", body["port"])
        }
        if body["log_level"] != "info" {
            t.Errorf("expected log_level info, got %v", body["log_level"])
        }
    })

    t.Run("method_not_allowed", func(t *testing.T) {
        h := newTestHandler(t, ms, auth, "")
        req := makeAuthenticatedRequest(t, auth, http.MethodDelete, "/admin/api/config/server", nil)
        rec := httptest.NewRecorder()
        h.handleServerConfig(rec, req)
        if rec.Code != http.StatusMethodNotAllowed {
            t.Fatalf("expected 405, got %d", rec.Code)
        }
    })

    t.Run("put_no_config_path", func(t *testing.T) {
        h := newTestHandler(t, ms, auth, "")
        body := map[string]interface{}{"host": "127.0.0.1"}
        req := makeAuthenticatedRequest(t, auth, http.MethodPut, "/admin/api/config/server", body)
        rec := httptest.NewRecorder()
        h.handleServerConfig(rec, req)
        if rec.Code != http.StatusInternalServerError {
            t.Fatalf("expected 500, got %d; body: %s", rec.Code, rec.Body.String())
        }
    })

    t.Run("put_invalid_json", func(t *testing.T) {
        _, _, configPath := setupConfigTestWithFile(t)
        h := newTestHandler(t, ms, auth, configPath)
        req := httptest.NewRequest(http.MethodPut, "/admin/api/config/server", nil)
        token, _ := auth.GenerateToken("admin", "admin")
        req.Header.Set("Authorization", "Bearer "+token)
        req.Header.Set("Content-Type", "application/json")
        rec := httptest.NewRecorder()
        h.handleServerConfig(rec, req)
        if rec.Code != http.StatusBadRequest {
            t.Fatalf("expected 400, got %d", rec.Code)
        }
    })

    t.Run("update_host", func(t *testing.T) {
        _, _, configPath := setupConfigTestWithFile(t)
        h := newTestHandler(t, ms, auth, configPath)
        body := map[string]interface{}{"host": "127.0.0.1"}
        req := makeAuthenticatedRequest(t, auth, http.MethodPut, "/admin/api/config/server", body)
        rec := httptest.NewRecorder()
        h.handleServerConfig(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
        }
        sec := readYAMLSection(t, configPath, "server")
        if sec["host"] != "127.0.0.1" {
            t.Errorf("expected host 127.0.0.1, got %v", sec["host"])
        }
    })

    t.Run("update_port", func(t *testing.T) {
        _, _, configPath := setupConfigTestWithFile(t)
        h := newTestHandler(t, ms, auth, configPath)
        body := map[string]interface{}{"port": 9090}
        req := makeAuthenticatedRequest(t, auth, http.MethodPut, "/admin/api/config/server", body)
        rec := httptest.NewRecorder()
        h.handleServerConfig(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
        }
        sec := readYAMLSection(t, configPath, "server")
        if sec["port"] != 9090 {
            t.Errorf("expected port 9090, got %v", sec["port"])
        }
    })

    t.Run("update_log_level", func(t *testing.T) {
        _, _, configPath := setupConfigTestWithFile(t)
        h := newTestHandler(t, ms, auth, configPath)
        body := map[string]interface{}{"log_level": "debug"}
        req := makeAuthenticatedRequest(t, auth, http.MethodPut, "/admin/api/config/server", body)
        rec := httptest.NewRecorder()
        h.handleServerConfig(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
        }
        sec := readYAMLSection(t, configPath, "server")
        if sec["log_level"] != "debug" {
            t.Errorf("expected log_level debug, got %v", sec["log_level"])
        }
    })

    t.Run("update_enable_pprof", func(t *testing.T) {
        _, _, configPath := setupConfigTestWithFile(t)
        h := newTestHandler(t, ms, auth, configPath)
        body := map[string]interface{}{"enable_pprof": true}
        req := makeAuthenticatedRequest(t, auth, http.MethodPut, "/admin/api/config/server", body)
        rec := httptest.NewRecorder()
        h.handleServerConfig(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
        }
        sec := readYAMLSection(t, configPath, "server")
        if sec["enable_pprof"] != true {
            t.Errorf("expected enable_pprof true, got %v", sec["enable_pprof"])
        }
    })
}

// ─── Rate Limit Config Tests ────────────────────────────────────

func TestHandleRateLimitConfig(t *testing.T) {
    auth, ms, _ := setupConfigTest(t)

    t.Run("get", func(t *testing.T) {
        h := newTestHandler(t, ms, auth, "")
        req := makeAuthenticatedRequest(t, auth, http.MethodGet, "/admin/api/config/rate-limit", nil)
        rec := httptest.NewRecorder()
        h.handleRateLimitConfig(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
        }
        body := decodeResponse(t, rec)
        if body["enabled"] != true {
            t.Errorf("expected enabled true, got %v", body["enabled"])
        }
        if body["global_rpm"] != float64(100) {
            t.Errorf("expected global_rpm 100, got %v", body["global_rpm"])
        }
    })

    t.Run("method_not_allowed", func(t *testing.T) {
        h := newTestHandler(t, ms, auth, "")
        req := makeAuthenticatedRequest(t, auth, http.MethodPost, "/admin/api/config/rate-limit", nil)
        rec := httptest.NewRecorder()
        h.handleRateLimitConfig(rec, req)
        if rec.Code != http.StatusMethodNotAllowed {
            t.Fatalf("expected 405, got %d", rec.Code)
        }
    })

    t.Run("update_enabled", func(t *testing.T) {
        _, _, configPath := setupConfigTestWithFile(t)
        h := newTestHandler(t, ms, auth, configPath)
        body := map[string]interface{}{"enabled": false}
        req := makeAuthenticatedRequest(t, auth, http.MethodPut, "/admin/api/config/rate-limit", body)
        rec := httptest.NewRecorder()
        h.handleRateLimitConfig(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
        }
        sec := readYAMLNestedSection(t, configPath, "routing", "rate_limit")
        if sec["enabled"] != false {
            t.Errorf("expected enabled false, got %v", sec["enabled"])
        }
    })

    t.Run("update_global_rpm", func(t *testing.T) {
        _, _, configPath := setupConfigTestWithFile(t)
        h := newTestHandler(t, ms, auth, configPath)
        body := map[string]interface{}{"global_rpm": 200}
        req := makeAuthenticatedRequest(t, auth, http.MethodPut, "/admin/api/config/rate-limit", body)
        rec := httptest.NewRecorder()
        h.handleRateLimitConfig(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
        }
        sec := readYAMLNestedSection(t, configPath, "routing", "rate_limit")
        if sec["global_rpm"] != 200 {
            t.Errorf("expected global_rpm 200, got %v", sec["global_rpm"])
        }
    })
}

// ─── Retry Config Tests ─────────────────────────────────────────

func TestHandleRetryConfig(t *testing.T) {
    auth, ms, _ := setupConfigTest(t)

    t.Run("get", func(t *testing.T) {
        h := newTestHandler(t, ms, auth, "")
        req := makeAuthenticatedRequest(t, auth, http.MethodGet, "/admin/api/config/retry", nil)
        rec := httptest.NewRecorder()
        h.handleRetryConfig(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
        }
        body := decodeResponse(t, rec)
        if body["max_retries"] != float64(3) {
            t.Errorf("expected max_retries 3, got %v", body["max_retries"])
        }
        if body["initial_backoff"] != "1s" {
            t.Errorf("expected initial_backoff 1s, got %v", body["initial_backoff"])
        }
    })

    t.Run("method_not_allowed", func(t *testing.T) {
        h := newTestHandler(t, ms, auth, "")
        req := makeAuthenticatedRequest(t, auth, http.MethodDelete, "/admin/api/config/retry", nil)
        rec := httptest.NewRecorder()
        h.handleRetryConfig(rec, req)
        if rec.Code != http.StatusMethodNotAllowed {
            t.Fatalf("expected 405, got %d", rec.Code)
        }
    })

    t.Run("update_max_retries", func(t *testing.T) {
        _, _, configPath := setupConfigTestWithFile(t)
        h := newTestHandler(t, ms, auth, configPath)
        body := map[string]interface{}{"max_retries": 5}
        req := makeAuthenticatedRequest(t, auth, http.MethodPut, "/admin/api/config/retry", body)
        rec := httptest.NewRecorder()
        h.handleRetryConfig(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
        }
        sec := readYAMLNestedSection(t, configPath, "routing", "retry")
        if sec["max_retries"] != 5 {
            t.Errorf("expected max_retries 5, got %v", sec["max_retries"])
        }
    })
}

// ─── Negotiation Config Tests ───────────────────────────────────

func TestHandleNegotiationConfig(t *testing.T) {
    auth, ms, _ := setupConfigTest(t)

    t.Run("get", func(t *testing.T) {
        h := newTestHandler(t, ms, auth, "")
        req := makeAuthenticatedRequest(t, auth, http.MethodGet, "/admin/api/config/negotiation", nil)
        rec := httptest.NewRecorder()
        h.handleNegotiationConfig(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
        }
        body := decodeResponse(t, rec)
        if body["disable_fusion_mlx_routing"] != true {
            t.Errorf("expected disable_fusion_mlx_routing true, got %v", body["disable_fusion_mlx_routing"])
        }
        if body["route_header"] != "X-Fusion-Route" {
            t.Errorf("expected route_header X-Fusion-Route, got %v", body["route_header"])
        }
    })

    t.Run("update_route_header", func(t *testing.T) {
        _, _, configPath := setupConfigTestWithFile(t)
        h := newTestHandler(t, ms, auth, configPath)
        body := map[string]interface{}{"route_header": "X-Custom-Route"}
        req := makeAuthenticatedRequest(t, auth, http.MethodPut, "/admin/api/config/negotiation", body)
        rec := httptest.NewRecorder()
        h.handleNegotiationConfig(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
        }
        sec := readYAMLNestedSection(t, configPath, "routing", "negotiation")
        if sec["route_header"] != "X-Custom-Route" {
            t.Errorf("expected route_header X-Custom-Route, got %v", sec["route_header"])
        }
    })
}

// ─── Cache Config Tests ─────────────────────────────────────────

func TestHandleCacheConfig(t *testing.T) {
    auth, ms, _ := setupConfigTest(t)

    t.Run("get", func(t *testing.T) {
        h := newTestHandler(t, ms, auth, "")
        req := makeAuthenticatedRequest(t, auth, http.MethodGet, "/admin/api/config/cache", nil)
        rec := httptest.NewRecorder()
        h.handleCacheConfig(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
        }
        body := decodeResponse(t, rec)
        if body["enabled"] != true {
            t.Errorf("expected enabled true, got %v", body["enabled"])
        }
        if body["backend"] != "memory" {
            t.Errorf("expected backend memory, got %v", body["backend"])
        }
        redis, ok := body["redis"].(map[string]interface{})
        if !ok {
            t.Fatalf("expected redis object, got %v", body["redis"])
        }
        if redis["password"] != "****pass" {
            t.Errorf("expected masked redis password, got %v", redis["password"])
        }
    })

    t.Run("update_enabled", func(t *testing.T) {
        _, _, configPath := setupConfigTestWithFile(t)
        h := newTestHandler(t, ms, auth, configPath)
        body := map[string]interface{}{"enabled": false}
        req := makeAuthenticatedRequest(t, auth, http.MethodPut, "/admin/api/config/cache", body)
        rec := httptest.NewRecorder()
        h.handleCacheConfig(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
        }
        sec := readYAMLSection(t, configPath, "cache")
        if sec["enabled"] != false {
            t.Errorf("expected enabled false, got %v", sec["enabled"])
        }
    })

    t.Run("update_redis_password_empty_keeps", func(t *testing.T) {
        _, _, configPath := setupConfigTestWithFile(t)
        h := newTestHandler(t, ms, auth, configPath)
        body := map[string]interface{}{
            "redis": map[string]interface{}{
                "password": "",
            },
        }
        req := makeAuthenticatedRequest(t, auth, http.MethodPut, "/admin/api/config/cache", body)
        rec := httptest.NewRecorder()
        h.handleCacheConfig(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
        }
    })
}

// ─── Cost Config Tests ──────────────────────────────────────────

func TestHandleCostConfig(t *testing.T) {
    auth, ms, _ := setupConfigTest(t)

    t.Run("get", func(t *testing.T) {
        h := newTestHandler(t, ms, auth, "")
        req := makeAuthenticatedRequest(t, auth, http.MethodGet, "/admin/api/config/cost", nil)
        rec := httptest.NewRecorder()
        h.handleCostConfig(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
        }
        body := decodeResponse(t, rec)
        if body["enabled"] != true {
            t.Errorf("expected enabled true, got %v", body["enabled"])
        }
        if body["pricing_file"] != "pricing.yaml" {
            t.Errorf("expected pricing_file pricing.yaml, got %v", body["pricing_file"])
        }
    })

    t.Run("update", func(t *testing.T) {
        _, _, configPath := setupConfigTestWithFile(t)
        h := newTestHandler(t, ms, auth, configPath)
        body := map[string]interface{}{"enabled": false, "budget_alert_threshold": 0.9}
        req := makeAuthenticatedRequest(t, auth, http.MethodPut, "/admin/api/config/cost", body)
        rec := httptest.NewRecorder()
        h.handleCostConfig(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
        }
        sec := readYAMLSection(t, configPath, "cost")
        if sec["enabled"] != false {
            t.Errorf("expected enabled false, got %v", sec["enabled"])
        }
    })
}

// ─── CostMarkup Config Tests ────────────────────────────────────

func TestHandleCostMarkupConfig(t *testing.T) {
    auth, ms, _ := setupConfigTest(t)

    t.Run("get", func(t *testing.T) {
        h := newTestHandler(t, ms, auth, "")
        req := makeAuthenticatedRequest(t, auth, http.MethodGet, "/admin/api/config/cost-markup", nil)
        rec := httptest.NewRecorder()
        h.handleCostMarkupConfig(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
        }
        body := decodeResponse(t, rec)
        if body["enabled"] != false {
            t.Errorf("expected enabled false, got %v", body["enabled"])
        }
    })

    t.Run("update", func(t *testing.T) {
        _, _, configPath := setupConfigTestWithFile(t)
        h := newTestHandler(t, ms, auth, configPath)
        body := map[string]interface{}{"enabled": true, "global_markup": 0.15}
        req := makeAuthenticatedRequest(t, auth, http.MethodPut, "/admin/api/config/cost-markup", body)
        rec := httptest.NewRecorder()
        h.handleCostMarkupConfig(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
        }
        sec := readYAMLSection(t, configPath, "cost_markup")
        if sec["enabled"] != true {
            t.Errorf("expected enabled true, got %v", sec["enabled"])
        }
    })
}

// ─── PII Config Tests ───────────────────────────────────────────

func TestHandlePIIConfig(t *testing.T) {
    auth, ms, _ := setupConfigTest(t)

    t.Run("get", func(t *testing.T) {
        h := newTestHandler(t, ms, auth, "")
        req := makeAuthenticatedRequest(t, auth, http.MethodGet, "/admin/api/config/pii", nil)
        rec := httptest.NewRecorder()
        h.handlePIIConfig(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
        }
        body := decodeResponse(t, rec)
        if body["enabled"] != true {
            t.Errorf("expected enabled true, got %v", body["enabled"])
        }
        if body["action"] != "mask" {
            t.Errorf("expected action mask, got %v", body["action"])
        }
        patterns, ok := body["patterns"].([]interface{})
        if !ok || len(patterns) != 2 {
            t.Fatalf("expected 2 patterns, got %v", body["patterns"])
        }
    })

    t.Run("update_action", func(t *testing.T) {
        _, _, configPath := setupConfigTestWithFile(t)
        h := newTestHandler(t, ms, auth, configPath)
        body := map[string]interface{}{"action": "block"}
        req := makeAuthenticatedRequest(t, auth, http.MethodPut, "/admin/api/config/pii", body)
        rec := httptest.NewRecorder()
        h.handlePIIConfig(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
        }
        sec := readYAMLSection(t, configPath, "pii")
        if sec["action"] != "block" {
            t.Errorf("expected action block, got %v", sec["action"])
        }
    })

    t.Run("update_patterns", func(t *testing.T) {
        _, _, configPath := setupConfigTestWithFile(t)
        h := newTestHandler(t, ms, auth, configPath)
        newPatterns := []map[string]interface{}{
            {"name": "ssn", "regex": "\\d{3}-\\d{2}-\\d{4}"},
        }
        body := map[string]interface{}{"patterns": newPatterns}
        req := makeAuthenticatedRequest(t, auth, http.MethodPut, "/admin/api/config/pii", body)
        rec := httptest.NewRecorder()
        h.handlePIIConfig(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
        }
        sec := readYAMLSection(t, configPath, "pii")
        patterns, ok := sec["patterns"].([]interface{})
        if !ok || len(patterns) != 1 {
            t.Fatalf("expected 1 pattern, got %v", sec["patterns"])
        }
    })
}

// ─── Cloud Routing Config Tests ─────────────────────────────────

func TestHandleCloudRoutingConfig(t *testing.T) {
    auth, ms, _ := setupConfigTest(t)

    t.Run("get", func(t *testing.T) {
        h := newTestHandler(t, ms, auth, "")
        req := makeAuthenticatedRequest(t, auth, http.MethodGet, "/admin/api/config/cloud-routing", nil)
        rec := httptest.NewRecorder()
        h.handleCloudRoutingConfig(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
        }
        body := decodeResponse(t, rec)
        if body["strategy"] != "weighted" {
            t.Errorf("expected strategy weighted, got %v", body["strategy"])
        }
    })

    t.Run("update_strategy", func(t *testing.T) {
        _, _, configPath := setupConfigTestWithFile(t)
        h := newTestHandler(t, ms, auth, configPath)
        body := map[string]interface{}{"strategy": "random"}
        req := makeAuthenticatedRequest(t, auth, http.MethodPut, "/admin/api/config/cloud-routing", body)
        rec := httptest.NewRecorder()
        h.handleCloudRoutingConfig(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
        }
        sec := readYAMLSection(t, configPath, "cloud_routing")
        if sec["strategy"] != "random" {
            t.Errorf("expected strategy random, got %v", sec["strategy"])
        }
    })
}

// ─── Hardware Config Tests ──────────────────────────────────────

func TestHandleHardwareConfig(t *testing.T) {
    auth, ms, _ := setupConfigTest(t)

    t.Run("get", func(t *testing.T) {
        h := newTestHandler(t, ms, auth, "")
        req := makeAuthenticatedRequest(t, auth, http.MethodGet, "/admin/api/config/hardware", nil)
        rec := httptest.NewRecorder()
        h.handleHardwareConfig(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
        }
        body := decodeResponse(t, rec)
        if body["enabled"] != true {
            t.Errorf("expected enabled true, got %v", body["enabled"])
        }
        if body["iokit_enabled"] != true {
            t.Errorf("expected iokit_enabled true, got %v", body["iokit_enabled"])
        }
    })

    t.Run("update_enabled", func(t *testing.T) {
        _, _, configPath := setupConfigTestWithFile(t)
        h := newTestHandler(t, ms, auth, configPath)
        body := map[string]interface{}{"enabled": false}
        req := makeAuthenticatedRequest(t, auth, http.MethodPut, "/admin/api/config/hardware", body)
        rec := httptest.NewRecorder()
        h.handleHardwareConfig(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
        }
        sec := readYAMLSection(t, configPath, "hardware")
        if sec["enabled"] != false {
            t.Errorf("expected enabled false, got %v", sec["enabled"])
        }
    })

    t.Run("update_iokit_enabled", func(t *testing.T) {
        _, _, configPath := setupConfigTestWithFile(t)
        h := newTestHandler(t, ms, auth, configPath)
        body := map[string]interface{}{"iokit_enabled": false}
        req := makeAuthenticatedRequest(t, auth, http.MethodPut, "/admin/api/config/hardware", body)
        rec := httptest.NewRecorder()
        h.handleHardwareConfig(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
        }
        iokitSec := readYAMLNestedSection(t, configPath, "hardware", "iokit")
        if iokitSec["enabled"] != false {
            t.Errorf("expected iokit enabled false, got %v", iokitSec["enabled"])
        }
    })
}

// ─── Tokenizer Config Tests ─────────────────────────────────────

func TestHandleTokenizerConfig(t *testing.T) {
    auth, ms, _ := setupConfigTest(t)

    t.Run("get", func(t *testing.T) {
        h := newTestHandler(t, ms, auth, "")
        req := makeAuthenticatedRequest(t, auth, http.MethodGet, "/admin/api/config/tokenizer", nil)
        rec := httptest.NewRecorder()
        h.handleTokenizerConfig(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
        }
        body := decodeResponse(t, rec)
        if body["provider"] != "local" {
            t.Errorf("expected provider local, got %v", body["provider"])
        }
        if body["scene_presets_chat"] != float64(1024) {
            t.Errorf("expected scene_presets_chat 1024, got %v", body["scene_presets_chat"])
        }
    })

    t.Run("update_provider", func(t *testing.T) {
        _, _, configPath := setupConfigTestWithFile(t)
        h := newTestHandler(t, ms, auth, configPath)
        body := map[string]interface{}{"provider": "remote"}
        req := makeAuthenticatedRequest(t, auth, http.MethodPut, "/admin/api/config/tokenizer", body)
        rec := httptest.NewRecorder()
        h.handleTokenizerConfig(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
        }
        sec := readYAMLSection(t, configPath, "tokenizer")
        if sec["provider"] != "remote" {
            t.Errorf("expected provider remote, got %v", sec["provider"])
        }
    })
}

// ─── Observability Config Tests ─────────────────────────────────

func TestHandleObservabilityConfig(t *testing.T) {
    auth, ms, _ := setupConfigTest(t)

    t.Run("get", func(t *testing.T) {
        h := newTestHandler(t, ms, auth, "")
        req := makeAuthenticatedRequest(t, auth, http.MethodGet, "/admin/api/config/observability", nil)
        rec := httptest.NewRecorder()
        h.handleObservabilityConfig(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
        }
        body := decodeResponse(t, rec)
        if body["log_format"] != "json" {
            t.Errorf("expected log_format json, got %v", body["log_format"])
        }
        if body["metrics_enabled"] != true {
            t.Errorf("expected metrics_enabled true, got %v", body["metrics_enabled"])
        }
    })

    t.Run("update_log_format", func(t *testing.T) {
        _, _, configPath := setupConfigTestWithFile(t)
        h := newTestHandler(t, ms, auth, configPath)
        body := map[string]interface{}{"log_format": "text"}
        req := makeAuthenticatedRequest(t, auth, http.MethodPut, "/admin/api/config/observability", body)
        rec := httptest.NewRecorder()
        h.handleObservabilityConfig(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
        }
        sec := readYAMLSection(t, configPath, "observability")
        if sec["log_format"] != "text" {
            t.Errorf("expected log_format text, got %v", sec["log_format"])
        }
    })
}

// ─── CORS Config Tests ──────────────────────────────────────────

func TestHandleCORSConfig(t *testing.T) {
    auth, ms, _ := setupConfigTest(t)

    t.Run("get", func(t *testing.T) {
        h := newTestHandler(t, ms, auth, "")
        req := makeAuthenticatedRequest(t, auth, http.MethodGet, "/admin/api/config/cors", nil)
        rec := httptest.NewRecorder()
        h.handleCORSConfig(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
        }
        body := decodeResponse(t, rec)
        origins, ok := body["allowed_origins"].([]interface{})
        if !ok || len(origins) == 0 {
            t.Errorf("expected non-empty allowed_origins, got %v", body["allowed_origins"])
        }
    })

    t.Run("update_origins", func(t *testing.T) {
        _, _, configPath := setupConfigTestWithFile(t)
        h := newTestHandler(t, ms, auth, configPath)
        body := map[string]interface{}{
            "allowed_origins": []string{"https://example.com"},
        }
        req := makeAuthenticatedRequest(t, auth, http.MethodPut, "/admin/api/config/cors", body)
        rec := httptest.NewRecorder()
        h.handleCORSConfig(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
        }
    })
}

// ─── Hot Reload Config Tests ────────────────────────────────────

func TestHandleHotReloadConfig(t *testing.T) {
    auth, ms, _ := setupConfigTest(t)

    t.Run("get", func(t *testing.T) {
        h := newTestHandler(t, ms, auth, "")
        req := makeAuthenticatedRequest(t, auth, http.MethodGet, "/admin/api/config/hot-reload", nil)
        rec := httptest.NewRecorder()
        h.handleHotReloadConfig(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
        }
        body := decodeResponse(t, rec)
        if body["enabled"] != true {
            t.Errorf("expected enabled true, got %v", body["enabled"])
        }
        if body["versioning"] != true {
            t.Errorf("expected versioning true, got %v", body["versioning"])
        }
    })

    t.Run("update_enabled", func(t *testing.T) {
        _, _, configPath := setupConfigTestWithFile(t)
        h := newTestHandler(t, ms, auth, configPath)
        body := map[string]interface{}{"enabled": false}
        req := makeAuthenticatedRequest(t, auth, http.MethodPut, "/admin/api/config/hot-reload", body)
        rec := httptest.NewRecorder()
        h.handleHotReloadConfig(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
        }
        sec := readYAMLSection(t, configPath, "hot_reload")
        if sec["enabled"] != false {
            t.Errorf("expected enabled false, got %v", sec["enabled"])
        }
    })
}

// ─── Cluster Config Tests ───────────────────────────────────────

func TestHandleClusterConfig(t *testing.T) {
    auth, ms, _ := setupConfigTest(t)

    t.Run("get", func(t *testing.T) {
        h := newTestHandler(t, ms, auth, "")
        req := makeAuthenticatedRequest(t, auth, http.MethodGet, "/admin/api/config/cluster", nil)
        rec := httptest.NewRecorder()
        h.handleClusterConfig(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
        }
        body := decodeResponse(t, rec)
        if body["enabled"] != false {
            t.Errorf("expected enabled false, got %v", body["enabled"])
        }
        if body["master_shared_token"] != "****oken" {
            t.Errorf("expected masked master_shared_token, got %v", body["master_shared_token"])
        }
        nodes, ok := body["nodes"].([]interface{})
        if !ok || len(nodes) != 1 {
            t.Fatalf("expected 1 node, got %v", body["nodes"])
        }
    })

    t.Run("update_enabled", func(t *testing.T) {
        _, _, configPath := setupConfigTestWithFile(t)
        h := newTestHandler(t, ms, auth, configPath)
        body := map[string]interface{}{"enabled": true}
        req := makeAuthenticatedRequest(t, auth, http.MethodPut, "/admin/api/config/cluster", body)
        rec := httptest.NewRecorder()
        h.handleClusterConfig(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
        }
        sec := readYAMLSection(t, configPath, "cluster")
        if sec["enabled"] != true {
            t.Errorf("expected enabled true, got %v", sec["enabled"])
        }
    })
}

// ─── Realtime Config Tests ──────────────────────────────────────

func TestHandleRealtimeConfig(t *testing.T) {
    auth, ms, _ := setupConfigTest(t)

    t.Run("get", func(t *testing.T) {
        h := newTestHandler(t, ms, auth, "")
        req := makeAuthenticatedRequest(t, auth, http.MethodGet, "/admin/api/config/realtime", nil)
        rec := httptest.NewRecorder()
        h.handleRealtimeConfig(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
        }
        body := decodeResponse(t, rec)
        if body["api_key"] != "****cdef" {
            t.Errorf("expected masked api_key, got %v", body["api_key"])
        }
    })

    t.Run("update_enabled", func(t *testing.T) {
        _, _, configPath := setupConfigTestWithFile(t)
        h := newTestHandler(t, ms, auth, configPath)
        body := map[string]interface{}{"enabled": true, "backend_url": "ws://localhost:8080"}
        req := makeAuthenticatedRequest(t, auth, http.MethodPut, "/admin/api/config/realtime", body)
        rec := httptest.NewRecorder()
        h.handleRealtimeConfig(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
        }
        sec := readYAMLSection(t, configPath, "realtime")
        if sec["enabled"] != true {
            t.Errorf("expected enabled true, got %v", sec["enabled"])
        }
    })
}

// ─── Admin Config Tests ─────────────────────────────────────────

func TestHandleAdminConfig(t *testing.T) {
    auth, ms, _ := setupConfigTest(t)

    t.Run("get", func(t *testing.T) {
        h := newTestHandler(t, ms, auth, "")
        req := makeAuthenticatedRequest(t, auth, http.MethodGet, "/admin/api/config/admin", nil)
        rec := httptest.NewRecorder()
        h.handleAdminConfig(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
        }
        body := decodeResponse(t, rec)
        jwtSecret, _ := body["jwt_secret"].(string)
        if len(jwtSecret) < 4 || jwtSecret[:4] != "****" {
            t.Errorf("expected masked jwt_secret, got %v", body["jwt_secret"])
        }
        users, ok := body["users"].(map[string]interface{})
        if !ok {
            t.Fatalf("expected users map, got %v", body["users"])
        }
        for k, v := range users {
            if v != "********" {
                t.Errorf("expected masked password for user %s, got %v", k, v)
            }
        }
    })

    t.Run("update_listen", func(t *testing.T) {
        _, _, configPath := setupConfigTestWithFile(t)
        h := newTestHandler(t, ms, auth, configPath)
        body := map[string]interface{}{"listen": ":9091"}
        req := makeAuthenticatedRequest(t, auth, http.MethodPut, "/admin/api/config/admin", body)
        rec := httptest.NewRecorder()
        h.handleAdminConfig(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
        }
        sec := readYAMLSection(t, configPath, "admin")
        if sec["listen"] != ":9091" {
            t.Errorf("expected listen :9091, got %v", sec["listen"])
        }
    })

    // F9: an empty password must NOT be written — the prior `v != "" &&
    // len(v) < 8` guard let "" through, producing a passwordless admin
    // login. Empty now means "keep existing" (the user is skipped, not
    // blanked), and a real password under 8 chars is rejected.
    t.Run("empty_password_not_written", func(t *testing.T) {
        _, _, configPath := setupConfigTestWithFile(t)
        h := newTestHandler(t, ms, auth, configPath)
        users := map[string]string{"admin": ""}
        body := map[string]interface{}{"users": users}
        req := makeAuthenticatedRequest(t, auth, http.MethodPut, "/admin/api/config/admin", body)
        rec := httptest.NewRecorder()
        h.handleAdminConfig(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("empty password (no-change) should be accepted, got %d; body: %s", rec.Code, rec.Body.String())
        }
        sec := readYAMLSection(t, configPath, "admin")
        written, _ := sec["users"].(map[string]interface{})
        if v, ok := written["admin"]; ok && v == "" {
            t.Fatalf("empty password must not be persisted, got users=%v", written)
        }
    })

    t.Run("short_password_rejected", func(t *testing.T) {
        _, _, configPath := setupConfigTestWithFile(t)
        h := newTestHandler(t, ms, auth, configPath)
        users := map[string]string{"admin": "short"}
        body := map[string]interface{}{"users": users}
        req := makeAuthenticatedRequest(t, auth, http.MethodPut, "/admin/api/config/admin", body)
        rec := httptest.NewRecorder()
        h.handleAdminConfig(rec, req)
        if rec.Code != http.StatusBadRequest {
            t.Fatalf("short password should be 400, got %d; body: %s", rec.Code, rec.Body.String())
        }
    })
}

// ─── OIDC Config Tests ──────────────────────────────────────────

func TestHandleOIDCConfig(t *testing.T) {
    auth, ms, _ := setupConfigTest(t)

    t.Run("get", func(t *testing.T) {
        h := newTestHandler(t, ms, auth, "")
        req := makeAuthenticatedRequest(t, auth, http.MethodGet, "/admin/api/config/oidc", nil)
        rec := httptest.NewRecorder()
        h.handleOIDCConfig(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
        }
        body := decodeResponse(t, rec)
        if body["enabled"] != false {
            t.Errorf("expected enabled false, got %v", body["enabled"])
        }
    })

    t.Run("update_enabled", func(t *testing.T) {
        _, _, configPath := setupConfigTestWithFile(t)
        h := newTestHandler(t, ms, auth, configPath)
        body := map[string]interface{}{
            "enabled":   true,
            "issuer":    "https://auth.example.com",
            "client_id": "my-client",
        }
        req := makeAuthenticatedRequest(t, auth, http.MethodPut, "/admin/api/config/oidc", body)
        rec := httptest.NewRecorder()
        h.handleOIDCConfig(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
        }
        sec := readYAMLSection(t, configPath, "oidc")
        if sec["enabled"] != true {
            t.Errorf("expected enabled true, got %v", sec["enabled"])
        }
        if sec["issuer"] != "https://auth.example.com" {
            t.Errorf("expected issuer, got %v", sec["issuer"])
        }
    })
}

// ─── RBAC Config Tests ──────────────────────────────────────────

func TestHandleRBACConfig(t *testing.T) {
    auth, ms, _ := setupConfigTest(t)

    t.Run("get", func(t *testing.T) {
        h := newTestHandler(t, ms, auth, "")
        req := makeAuthenticatedRequest(t, auth, http.MethodGet, "/admin/api/config/rbac", nil)
        rec := httptest.NewRecorder()
        h.handleRBACConfig(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
        }
        body := decodeResponse(t, rec)
        if body["enabled"] != false {
            t.Errorf("expected enabled false, got %v", body["enabled"])
        }
        if body["default_role"] != "viewer" {
            t.Errorf("expected default_role viewer, got %v", body["default_role"])
        }
        if body["team_enabled"] != false {
            t.Errorf("expected team_enabled false, got %v", body["team_enabled"])
        }
    })

    t.Run("update_rbac", func(t *testing.T) {
        _, _, configPath := setupConfigTestWithFile(t)
        h := newTestHandler(t, ms, auth, configPath)
        body := map[string]interface{}{
            "enabled":      true,
            "default_role": "admin",
        }
        req := makeAuthenticatedRequest(t, auth, http.MethodPut, "/admin/api/config/rbac", body)
        rec := httptest.NewRecorder()
        h.handleRBACConfig(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
        }
        sec := readYAMLSection(t, configPath, "rbac")
        if sec["enabled"] != true {
            t.Errorf("expected enabled true, got %v", sec["enabled"])
        }
    })
}

// ─── Semantic Cache Config Tests ────────────────────────────────

func TestHandleSemanticCacheConfig(t *testing.T) {
    auth, ms, _ := setupConfigTest(t)

    t.Run("get", func(t *testing.T) {
        h := newTestHandler(t, ms, auth, "")
        req := makeAuthenticatedRequest(t, auth, http.MethodGet, "/admin/api/config/semantic-cache", nil)
        rec := httptest.NewRecorder()
        h.handleSemanticCacheConfig(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
        }
        body := decodeResponse(t, rec)
        if body["enabled"] != false {
            t.Errorf("expected enabled false, got %v", body["enabled"])
        }
    })

    t.Run("update", func(t *testing.T) {
        _, _, configPath := setupConfigTestWithFile(t)
        h := newTestHandler(t, ms, auth, configPath)
        body := map[string]interface{}{
            "enabled":              true,
            "similarity_threshold": 0.9,
            "provider":             "openai",
        }
        req := makeAuthenticatedRequest(t, auth, http.MethodPut, "/admin/api/config/semantic-cache", body)
        rec := httptest.NewRecorder()
        h.handleSemanticCacheConfig(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
        }
        sec := readYAMLSection(t, configPath, "semantic_cache")
        if sec["enabled"] != true {
            t.Errorf("expected enabled true, got %v", sec["enabled"])
        }
    })
}

// ─── Prompt Injection Config Tests ──────────────────────────────

func TestHandlePromptInjectionConfig(t *testing.T) {
    auth, ms, _ := setupConfigTest(t)

    t.Run("get", func(t *testing.T) {
        h := newTestHandler(t, ms, auth, "")
        req := makeAuthenticatedRequest(t, auth, http.MethodGet, "/admin/api/config/prompt-injection", nil)
        rec := httptest.NewRecorder()
        h.handlePromptInjectionConfig(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
        }
        body := decodeResponse(t, rec)
        if body["enabled"] != true {
            t.Errorf("expected enabled true, got %v", body["enabled"])
        }
        if body["api_key"] != "****z123" {
            t.Errorf("expected masked api_key, got %v", body["api_key"])
        }
    })

    t.Run("update_threshold", func(t *testing.T) {
        _, _, configPath := setupConfigTestWithFile(t)
        h := newTestHandler(t, ms, auth, configPath)
        body := map[string]interface{}{"threshold": 0.95}
        req := makeAuthenticatedRequest(t, auth, http.MethodPut, "/admin/api/config/prompt-injection", body)
        rec := httptest.NewRecorder()
        h.handlePromptInjectionConfig(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
        }
        sec := readYAMLSection(t, configPath, "prompt_injection")
        if sec["threshold"] != 0.95 {
            t.Errorf("expected threshold 0.95, got %v", sec["threshold"])
        }
    })

    t.Run("update_api_key_empty_keeps", func(t *testing.T) {
        _, _, configPath := setupConfigTestWithFile(t)
        h := newTestHandler(t, ms, auth, configPath)
        body := map[string]interface{}{"api_key": ""}
        req := makeAuthenticatedRequest(t, auth, http.MethodPut, "/admin/api/config/prompt-injection", body)
        rec := httptest.NewRecorder()
        h.handlePromptInjectionConfig(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
        }
    })
}

// ─── Batch Config Tests ─────────────────────────────────────────

func TestHandleBatchConfig(t *testing.T) {
    auth, ms, _ := setupConfigTest(t)

    t.Run("get", func(t *testing.T) {
        h := newTestHandler(t, ms, auth, "")
        req := makeAuthenticatedRequest(t, auth, http.MethodGet, "/admin/api/config/batch", nil)
        rec := httptest.NewRecorder()
        h.handleBatchConfig(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
        }
        body := decodeResponse(t, rec)
        if body["enabled"] != false {
            t.Errorf("expected enabled false, got %v", body["enabled"])
        }
        if body["max_batch_size"] != float64(100) {
            t.Errorf("expected max_batch_size 100, got %v", body["max_batch_size"])
        }
    })

    t.Run("update_enabled", func(t *testing.T) {
        _, _, configPath := setupConfigTestWithFile(t)
        h := newTestHandler(t, ms, auth, configPath)
        body := map[string]interface{}{"enabled": true}
        req := makeAuthenticatedRequest(t, auth, http.MethodPut, "/admin/api/config/batch", body)
        rec := httptest.NewRecorder()
        h.handleBatchConfig(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
        }
        sec := readYAMLSection(t, configPath, "batch")
        if sec["enabled"] != true {
            t.Errorf("expected enabled true, got %v", sec["enabled"])
        }
    })
}

// ─── Store Config Tests ─────────────────────────────────────────

func TestHandleStoreConfig(t *testing.T) {
    auth, ms, _ := setupConfigTest(t)

    t.Run("get", func(t *testing.T) {
        h := newTestHandler(t, ms, auth, "")
        req := makeAuthenticatedRequest(t, auth, http.MethodGet, "/admin/api/config/store", nil)
        rec := httptest.NewRecorder()
        h.handleStoreConfig(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
        }
        body := decodeResponse(t, rec)
        if body["backend"] != "memory" {
            t.Errorf("expected backend memory, got %v", body["backend"])
        }
        if body["redis_password"] != "****pass" {
            t.Errorf("expected masked redis_password, got %v", body["redis_password"])
        }
    })

    t.Run("update_backend", func(t *testing.T) {
        _, _, configPath := setupConfigTestWithFile(t)
        h := newTestHandler(t, ms, auth, configPath)
        body := map[string]interface{}{"backend": "redis"}
        req := makeAuthenticatedRequest(t, auth, http.MethodPut, "/admin/api/config/store", body)
        rec := httptest.NewRecorder()
        h.handleStoreConfig(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
        }
        sec := readYAMLSection(t, configPath, "store")
        if sec["backend"] != "redis" {
            t.Errorf("expected backend redis, got %v", sec["backend"])
        }
    })
}

// ─── Validation Config Tests ────────────────────────────────────

func TestHandleValidationConfig(t *testing.T) {
    auth, ms, _ := setupConfigTest(t)

    t.Run("get", func(t *testing.T) {
        h := newTestHandler(t, ms, auth, "")
        req := makeAuthenticatedRequest(t, auth, http.MethodGet, "/admin/api/config/validation", nil)
        rec := httptest.NewRecorder()
        h.handleValidationConfig(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
        }
        body := decodeResponse(t, rec)
        if body["base_url_conflict_check"] != false {
            t.Errorf("expected base_url_conflict_check false, got %v", body["base_url_conflict_check"])
        }
    })

    t.Run("update", func(t *testing.T) {
        _, _, configPath := setupConfigTestWithFile(t)
        h := newTestHandler(t, ms, auth, configPath)
        body := map[string]interface{}{"base_url_conflict_check": true}
        req := makeAuthenticatedRequest(t, auth, http.MethodPut, "/admin/api/config/validation", body)
        rec := httptest.NewRecorder()
        h.handleValidationConfig(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
        }
        sec := readYAMLSection(t, configPath, "validation")
        if sec["base_url_conflict_check"] != true {
            t.Errorf("expected base_url_conflict_check true, got %v", sec["base_url_conflict_check"])
        }
    })
}

// EI1 guard: a config PUT must reload the live snapshot, not just write the
// file. The prior updateYAMLSection wrote the YAML and returned — on macOS
// fsnotify is unreliable, so the in-memory ConfigSnapshot stayed on the old
// value. The operator saw "saved" but routing kept the old config. This guard
// PUTs validation.base_url_conflict_check true then reads the LIVE snapshot
// (config.GetSnapshot, what handlers/routing actually consult), not the file.
// On the BUG (no Reload call) the snapshot stays false → guard FAILS.
func TestHandleValidationConfig_EI1_PUTReloadsSnapshot(t *testing.T) {
    _, _, configPath := setupConfigTestWithFile(t)
    h := newTestHandler(t, newMockStore(), newTestAuth(t), configPath)

    // Snapshot reflects the initial fullTestYAML (base_url_conflict_check=false).
    if config.GetSnapshot().Config.Validation.BaseURLConflictCheck {
        t.Fatalf("precondition: expected initial BaseURLConflictCheck false")
    }

    body := map[string]interface{}{"base_url_conflict_check": true}
    req := makeAuthenticatedRequest(t, newTestAuth(t), http.MethodPut, "/admin/api/config/validation", body)
    rec := httptest.NewRecorder()
    h.handleValidationConfig(rec, req)
    if rec.Code != http.StatusOK {
        t.Fatalf("PUT failed: %d %s", rec.Code, rec.Body.String())
    }

    // EI1: the LIVE snapshot (not the file) must now reflect the new value —
    // handlers read config.GetSnapshot, so this is what routing actually uses.
    if !config.GetSnapshot().Config.Validation.BaseURLConflictCheck {
        t.Fatal("EI1: config PUT wrote the file but did not reload the live snapshot — operator sees 'saved' but runtime keeps old config (fsnotify-unreliable-on-macOS silent no-op)")
    }
}

// ─── Auth Config Tests ──────────────────────────────────────────

func TestHandleAuthConfig(t *testing.T) {
    auth, ms, _ := setupConfigTest(t)

    t.Run("get", func(t *testing.T) {
        h := newTestHandler(t, ms, auth, "")
        req := makeAuthenticatedRequest(t, auth, http.MethodGet, "/admin/api/config/auth", nil)
        rec := httptest.NewRecorder()
        h.handleAuthConfig(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
        }
        body := decodeResponse(t, rec)
        if body["enabled"] != true {
            t.Errorf("expected enabled true, got %v", body["enabled"])
        }
        masterKey, _ := body["master_key"].(string)
        if len(masterKey) < 4 || masterKey[:4] != "****" {
            t.Errorf("expected masked master_key, got %v", body["master_key"])
        }
        apiKeys, ok := body["api_keys"].([]interface{})
        if !ok || len(apiKeys) != 1 {
            t.Fatalf("expected 1 api_key, got %v", body["api_keys"])
        }
        key0, _ := apiKeys[0].(map[string]interface{})
        keyVal, _ := key0["key"].(string)
        if len(keyVal) < 4 || keyVal[:4] != "****" {
            t.Errorf("expected masked key in api_keys[0], got %v", key0["key"])
        }
    })

    t.Run("update_passthrough", func(t *testing.T) {
        _, _, configPath := setupConfigTestWithFile(t)
        h := newTestHandler(t, ms, auth, configPath)
        body := map[string]interface{}{"passthrough": true}
        req := makeAuthenticatedRequest(t, auth, http.MethodPut, "/admin/api/config/auth", body)
        rec := httptest.NewRecorder()
        h.handleAuthConfig(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
        }
        sec := readYAMLSection(t, configPath, "auth")
        if sec["passthrough"] != true {
            t.Errorf("expected passthrough true, got %v", sec["passthrough"])
        }
    })

    t.Run("update_master_key_empty_keeps", func(t *testing.T) {
        _, _, configPath := setupConfigTestWithFile(t)
        h := newTestHandler(t, ms, auth, configPath)
        body := map[string]interface{}{"master_key": ""}
        req := makeAuthenticatedRequest(t, auth, http.MethodPut, "/admin/api/config/auth", body)
        rec := httptest.NewRecorder()
        h.handleAuthConfig(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
        }
    })

    // F9: GET masks every api_key. PUTting the masked values back verbatim
    // must NOT drop the real key — the prior code skipped writing "key" when
    // masked, then replaced the whole api_keys list, so the real key vanished
    // on next reload. Verify the real key survives a masked round-trip.
    t.Run("masked_apikey_roundtrip_preserves_real_key", func(t *testing.T) {
        _, _, configPath := setupConfigTestWithFile(t)
        h := newTestHandler(t, ms, auth, configPath)

        // GET -> masked key
        getReq := makeAuthenticatedRequest(t, auth, http.MethodGet, "/admin/api/config/auth", nil)
        getRec := httptest.NewRecorder()
        h.handleAuthConfig(getRec, getReq)
        if getRec.Code != http.StatusOK {
            t.Fatalf("GET 200, got %d", getRec.Code)
        }
        body := decodeResponse(t, getRec)
        apiKeys, _ := body["api_keys"].([]interface{})
        if len(apiKeys) != 1 {
            t.Fatalf("expected 1 api_key, got %d", len(apiKeys))
        }
        maskedEntry, _ := apiKeys[0].(map[string]interface{})
        maskedKey, _ := maskedEntry["key"].(string)
        if maskedKey[:4] != "****" {
            t.Fatalf("expected masked key, got %q", maskedKey)
        }

        // PUT the masked payload back (simulates a dashboard save that did
        // not retype the key)
        putBody := map[string]interface{}{
            "api_keys": []map[string]interface{}{
                {
                    "key":             maskedKey,
                    "name":            maskedEntry["name"],
                    "rpm":             maskedEntry["rpm"],
                    "tpm":             maskedEntry["tpm"],
                    "allowed_models":  maskedEntry["allowed_models"],
                    "allowed_backends": maskedEntry["allowed_backends"],
                    "model_modules":   maskedEntry["model_modules"],
                    "expires_at":      maskedEntry["expires_at"],
                    "budget_limit":    maskedEntry["budget_limit"],
                    "daily_budget_limit": maskedEntry["daily_budget_limit"],
                },
            },
        }
        putReq := makeAuthenticatedRequest(t, auth, http.MethodPut, "/admin/api/config/auth", putBody)
        putRec := httptest.NewRecorder()
        h.handleAuthConfig(putRec, putReq)
        if putRec.Code != http.StatusOK {
            t.Fatalf("PUT 200, got %d; body: %s", putRec.Code, putRec.Body.String())
        }

        // real key must still be in the persisted YAML
        sec := readYAMLSection(t, configPath, "auth")
        list, _ := sec["api_keys"].([]interface{})
        if len(list) != 1 {
            t.Fatalf("expected 1 api_key in YAML, got %d", len(list))
        }
        entry, _ := list[0].(map[string]interface{})
        savedKey, _ := entry["key"].(string)
        if savedKey != "sk-test-key-1234567890" {
            t.Fatalf("masked round-trip dropped the real key: got %q (empty=lost, masked=echoed-back-verbatim)", savedKey)
        }
    })
}

// ─── Full Config Tests ──────────────────────────────────────────

func TestHandleFullConfig(t *testing.T) {
    auth, ms, _ := setupConfigTest(t)

    t.Run("get", func(t *testing.T) {
        h := newTestHandler(t, ms, auth, "")
        req := makeAuthenticatedRequest(t, auth, http.MethodGet, "/admin/api/config/full", nil)
        rec := httptest.NewRecorder()
        h.handleFullConfig(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
        }
        body := decodeResponse(t, rec)
        server, ok := body["server"].(map[string]interface{})
        if !ok {
            t.Fatalf("expected server object, got %v", body["server"])
        }
        if server["port"] != float64(8100) {
            t.Errorf("expected server port 8100, got %v", server["port"])
        }
        authMap, ok := body["auth"].(map[string]interface{})
        if !ok {
            t.Fatalf("expected auth object, got %v", body["auth"])
        }
        masterKey, _ := authMap["master_key"].(string)
        if len(masterKey) < 4 || masterKey[:4] != "****" {
            t.Errorf("expected masked master_key in full config, got %v", authMap["master_key"])
        }
    })

    t.Run("method_not_allowed", func(t *testing.T) {
        h := newTestHandler(t, ms, auth, "")
        req := makeAuthenticatedRequest(t, auth, http.MethodPut, "/admin/api/config/full", nil)
        rec := httptest.NewRecorder()
        h.handleFullConfig(rec, req)
        if rec.Code != http.StatusMethodNotAllowed {
            t.Fatalf("expected 405, got %d", rec.Code)
        }
    })
}

// ─── Shared Helper Tests ────────────────────────────────────────

func TestApplyString(t *testing.T) {
    sec := map[string]interface{}{"existing": "old"}
    applyString(sec, "existing", strPtr("new"))
    if sec["existing"] != "new" {
        t.Errorf("expected new, got %v", sec["existing"])
    }
    applyString(sec, "untouched", nil)
    if _, ok := sec["untouched"]; ok {
        t.Errorf("expected untouched to not exist")
    }
}

func TestApplyInt(t *testing.T) {
    sec := map[string]interface{}{}
    applyInt(sec, "count", intPtr(42))
    if sec["count"] != 42 {
        t.Errorf("expected 42, got %v", sec["count"])
    }
    applyInt(sec, "missing", nil)
    if _, ok := sec["missing"]; ok {
        t.Errorf("expected missing to not exist")
    }
}

func TestApplyBool(t *testing.T) {
    sec := map[string]interface{}{}
    applyBool(sec, "flag", boolPtr(true))
    if sec["flag"] != true {
        t.Errorf("expected true, got %v", sec["flag"])
    }
    applyBool(sec, "missing", nil)
    if _, ok := sec["missing"]; ok {
        t.Errorf("expected missing to not exist")
    }
}

func TestStringSliceToInterface(t *testing.T) {
    input := []string{"a", "b", "c"}
    result := stringSliceToInterface(input)
    if len(result) != 3 {
        t.Fatalf("expected 3, got %d", len(result))
    }
    if result[0] != "a" || result[1] != "b" || result[2] != "c" {
        t.Errorf("expected [a b c], got %v", result)
    }
}

func TestWriteYAMLError(t *testing.T) {
    t.Run("no_config_path", func(t *testing.T) {
        rec := httptest.NewRecorder()
        writeYAMLError(rec, errNoConfigPath)
        if rec.Code != http.StatusInternalServerError {
            t.Fatalf("expected 500, got %d", rec.Code)
        }
        var body map[string]interface{}
        _ = json.NewDecoder(rec.Body).Decode(&body)
        errObj, _ := body["error"].(map[string]interface{})
        if errObj["message"] != "config file path not configured" {
            t.Errorf("expected specific message, got %v", errObj["message"])
        }
    })

    t.Run("generic_error", func(t *testing.T) {
        rec := httptest.NewRecorder()
        writeYAMLError(rec, fmt.Errorf("some IO error"))
        if rec.Code != http.StatusInternalServerError {
            t.Fatalf("expected 500, got %d", rec.Code)
        }
        var body map[string]interface{}
        _ = json.NewDecoder(rec.Body).Decode(&body)
        errObj, _ := body["error"].(map[string]interface{})
        if errObj["message"] != "some IO error" {
            t.Errorf("expected actual error message, got %v", errObj["message"])
        }
    })
}

func TestConfigHelperGetOrCreateSection(t *testing.T) {
    doc := map[string]interface{}{
        "existing": map[string]interface{}{
            "nested": map[string]interface{}{
                "value": "hello",
            },
        },
    }

    sec := getOrCreateSection(doc, "existing", "nested")
    if sec["value"] != "hello" {
        t.Errorf("expected hello, got %v", sec["value"])
    }

    newSec := getOrCreateSection(doc, "new", "deep", "path")
    if newSec == nil {
        t.Fatalf("expected non-nil section")
    }
    newSec["key"] = "val"
    deep, ok := doc["new"].(map[string]interface{})
    if !ok {
        t.Fatalf("expected new section to be created")
    }
    path, ok := deep["deep"].(map[string]interface{})["path"].(map[string]interface{})
    if !ok {
        t.Fatalf("expected deep path to be created")
    }
    if path["key"] != "val" {
        t.Errorf("expected val, got %v", path["key"])
    }
}

func TestReadYAMLDoc(t *testing.T) {
    t.Run("valid_file", func(t *testing.T) {
        tmpDir := t.TempDir()
        path := filepath.Join(tmpDir, "test.yaml")
        content := []byte("server:\n  host: 0.0.0.0\n  port: 8100\n")
        if err := os.WriteFile(path, content, 0644); err != nil {
            t.Fatalf("failed to write: %v", err)
        }
        doc, err := readYAMLDoc(path)
        if err != nil {
            t.Fatalf("unexpected error: %v", err)
        }
        if doc["server"] == nil {
            t.Fatalf("expected server section")
        }
    })

    t.Run("missing_file", func(t *testing.T) {
        _, err := readYAMLDoc("/nonexistent/path.yaml")
        if err == nil {
            t.Fatalf("expected error for missing file")
        }
    })
}

func TestWriteYAMLDoc(t *testing.T) {
    tmpDir := t.TempDir()
    path := filepath.Join(tmpDir, "output.yaml")
    doc := map[string]interface{}{
        "server": map[string]interface{}{
            "host": "0.0.0.0",
            "port": 8100,
        },
    }
    if err := writeYAMLDoc(path, doc); err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    data, err := os.ReadFile(path)
    if err != nil {
        t.Fatalf("failed to read: %v", err)
    }
    if len(data) == 0 {
        t.Fatalf("expected non-empty output")
    }
}

// ─── Config YAML Error Edge Case Tests ──────────────────────────

func TestConfigHandlersInvalidJSON(t *testing.T) {
    auth, ms, _ := setupConfigTest(t)
    handlers := []struct {
        name    string
        handler func(*Handler, http.ResponseWriter, *http.Request)
    }{
        {"server", (*Handler).handleServerConfig},
        {"rate_limit", (*Handler).handleRateLimitConfig},
        {"retry", (*Handler).handleRetryConfig},
        {"negotiation", (*Handler).handleNegotiationConfig},
        {"cache", (*Handler).handleCacheConfig},
        {"cost", (*Handler).handleCostConfig},
        {"cost_markup", (*Handler).handleCostMarkupConfig},
        {"pii", (*Handler).handlePIIConfig},
        {"cloud_routing", (*Handler).handleCloudRoutingConfig},
        {"hardware", (*Handler).handleHardwareConfig},
        {"tokenizer", (*Handler).handleTokenizerConfig},
        {"observability", (*Handler).handleObservabilityConfig},
        {"cors", (*Handler).handleCORSConfig},
        {"hot_reload", (*Handler).handleHotReloadConfig},
        {"cluster", (*Handler).handleClusterConfig},
        {"realtime", (*Handler).handleRealtimeConfig},
        {"admin", (*Handler).handleAdminConfig},
        {"oidc", (*Handler).handleOIDCConfig},
        {"rbac", (*Handler).handleRBACConfig},
        {"semantic_cache", (*Handler).handleSemanticCacheConfig},
        {"prompt_injection", (*Handler).handlePromptInjectionConfig},
        {"batch", (*Handler).handleBatchConfig},
        {"store", (*Handler).handleStoreConfig},
        {"validation", (*Handler).handleValidationConfig},
        {"auth", (*Handler).handleAuthConfig},
    }

    for _, h := range handlers {
        t.Run(h.name+"_invalid_json", func(t *testing.T) {
            hdl := newTestHandler(t, ms, auth, "")
            req := httptest.NewRequest(http.MethodPut, "/test", nil)
            token, _ := auth.GenerateToken("admin", "admin")
            req.Header.Set("Authorization", "Bearer "+token)
            rec := httptest.NewRecorder()
            h.handler(hdl, rec, req)
            if rec.Code != http.StatusBadRequest {
                t.Errorf("%s: expected 400 for invalid JSON, got %d; body: %s", h.name, rec.Code, rec.Body.String())
            }
        })
    }
}

func TestConfigHandlersMethodNotAllowed(t *testing.T) {
    auth, ms, _ := setupConfigTest(t)
    handlers := []struct {
        name    string
        handler func(*Handler, http.ResponseWriter, *http.Request)
    }{
        {"server", (*Handler).handleServerConfig},
        {"rate_limit", (*Handler).handleRateLimitConfig},
        {"retry", (*Handler).handleRetryConfig},
        {"negotiation", (*Handler).handleNegotiationConfig},
        {"cache", (*Handler).handleCacheConfig},
        {"cost", (*Handler).handleCostConfig},
        {"cost_markup", (*Handler).handleCostMarkupConfig},
        {"pii", (*Handler).handlePIIConfig},
        {"cloud_routing", (*Handler).handleCloudRoutingConfig},
        {"hardware", (*Handler).handleHardwareConfig},
        {"tokenizer", (*Handler).handleTokenizerConfig},
        {"observability", (*Handler).handleObservabilityConfig},
        {"cors", (*Handler).handleCORSConfig},
        {"hot_reload", (*Handler).handleHotReloadConfig},
        {"cluster", (*Handler).handleClusterConfig},
        {"realtime", (*Handler).handleRealtimeConfig},
        {"admin", (*Handler).handleAdminConfig},
        {"oidc", (*Handler).handleOIDCConfig},
        {"rbac", (*Handler).handleRBACConfig},
        {"semantic_cache", (*Handler).handleSemanticCacheConfig},
        {"prompt_injection", (*Handler).handlePromptInjectionConfig},
        {"batch", (*Handler).handleBatchConfig},
        {"store", (*Handler).handleStoreConfig},
        {"validation", (*Handler).handleValidationConfig},
        {"auth", (*Handler).handleAuthConfig},
    }

    for _, h := range handlers {
        t.Run(h.name+"_method_not_allowed", func(t *testing.T) {
            hdl := newTestHandler(t, ms, auth, "")
            req := makeAuthenticatedRequest(t, auth, http.MethodDelete, "/test", nil)
            rec := httptest.NewRecorder()
            h.handler(hdl, rec, req)
            if rec.Code != http.StatusMethodNotAllowed {
                t.Errorf("%s: expected 405, got %d", h.name, rec.Code)
            }
        })
    }
}

func TestConfigHandlersPUTNoConfigPath(t *testing.T) {
    auth, ms, _ := setupConfigTest(t)
    handlers := []struct {
        name    string
        handler func(*Handler, http.ResponseWriter, *http.Request)
        body    map[string]interface{}
    }{
        {"server", (*Handler).handleServerConfig, map[string]interface{}{"host": "127.0.0.1"}},
        {"rate_limit", (*Handler).handleRateLimitConfig, map[string]interface{}{"enabled": false}},
        {"retry", (*Handler).handleRetryConfig, map[string]interface{}{"max_retries": 5}},
        {"negotiation", (*Handler).handleNegotiationConfig, map[string]interface{}{"route_header": "X-Test"}},
        {"cache", (*Handler).handleCacheConfig, map[string]interface{}{"enabled": false}},
        {"cost", (*Handler).handleCostConfig, map[string]interface{}{"enabled": false}},
        {"cost_markup", (*Handler).handleCostMarkupConfig, map[string]interface{}{"enabled": true}},
        {"pii", (*Handler).handlePIIConfig, map[string]interface{}{"enabled": false}},
        {"cloud_routing", (*Handler).handleCloudRoutingConfig, map[string]interface{}{"strategy": "random"}},
        {"hardware", (*Handler).handleHardwareConfig, map[string]interface{}{"enabled": false}},
        {"tokenizer", (*Handler).handleTokenizerConfig, map[string]interface{}{"provider": "remote"}},
        {"observability", (*Handler).handleObservabilityConfig, map[string]interface{}{"log_format": "text"}},
        {"cors", (*Handler).handleCORSConfig, map[string]interface{}{"allowed_origins": []string{"*"}}},
        {"hot_reload", (*Handler).handleHotReloadConfig, map[string]interface{}{"enabled": false}},
        {"cluster", (*Handler).handleClusterConfig, map[string]interface{}{"enabled": true}},
        {"realtime", (*Handler).handleRealtimeConfig, map[string]interface{}{"enabled": true}},
        {"admin", (*Handler).handleAdminConfig, map[string]interface{}{"listen": ":9091"}},
        {"oidc", (*Handler).handleOIDCConfig, map[string]interface{}{"enabled": true}},
        {"rbac", (*Handler).handleRBACConfig, map[string]interface{}{"enabled": true}},
        {"semantic_cache", (*Handler).handleSemanticCacheConfig, map[string]interface{}{"enabled": true}},
        {"prompt_injection", (*Handler).handlePromptInjectionConfig, map[string]interface{}{"enabled": false}},
        {"batch", (*Handler).handleBatchConfig, map[string]interface{}{"enabled": true}},
        {"store", (*Handler).handleStoreConfig, map[string]interface{}{"backend": "redis"}},
        {"validation", (*Handler).handleValidationConfig, map[string]interface{}{"base_url_conflict_check": true}},
        {"auth", (*Handler).handleAuthConfig, map[string]interface{}{"passthrough": true}},
    }

    for _, h := range handlers {
        t.Run(h.name+"_no_config_path", func(t *testing.T) {
            hdl := newTestHandler(t, ms, auth, "")
            req := makeAuthenticatedRequest(t, auth, http.MethodPut, "/test", h.body)
            rec := httptest.NewRecorder()
            h.handler(hdl, rec, req)
            if rec.Code != http.StatusInternalServerError {
                t.Errorf("%s: expected 500 for no config path, got %d; body: %s", h.name, rec.Code, rec.Body.String())
            }
        })
    }
}

// ─── Pointer helpers ────────────────────────────────────────────

func strPtr(s string) *string { return &s }
func intPtr(i int) *int       { return &i }
func boolPtr(b bool) *bool    { return &b }
