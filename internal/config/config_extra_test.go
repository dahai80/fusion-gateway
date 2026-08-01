package config

import (
    "context"
    "os"
    "path/filepath"
    "testing"
    "time"
)

func TestValidate_InvalidPort_Zero(t *testing.T) {
    cfg := DefaultConfig()
    cfg.Server.Port = 0
    if err := validate(&cfg); err == nil {
        t.Fatal("expected error for port 0")
    }
}

func TestValidate_InvalidPort_Negative(t *testing.T) {
    cfg := DefaultConfig()
    cfg.Server.Port = -1
    if err := validate(&cfg); err == nil {
        t.Fatal("expected error for negative port")
    }
}

func TestValidate_InvalidPort_TooLarge(t *testing.T) {
    cfg := DefaultConfig()
    cfg.Server.Port = 70000
    if err := validate(&cfg); err == nil {
        t.Fatal("expected error for port > 65535")
    }
}

func TestValidate_TokenThresholdZero(t *testing.T) {
    cfg := DefaultConfig()
    cfg.Routing.TokenThreshold = 0
    if err := validate(&cfg); err == nil {
        t.Fatal("expected error for token_threshold 0")
    }
}

func TestValidate_TokenThresholdNegative(t *testing.T) {
    cfg := DefaultConfig()
    cfg.Routing.TokenThreshold = -5
    if err := validate(&cfg); err == nil {
        t.Fatal("expected error for negative token_threshold")
    }
}

func TestValidate_OutputInputRatioNegative(t *testing.T) {
    cfg := DefaultConfig()
    cfg.Routing.OutputInputRatioThreshold = -0.1
    if err := validate(&cfg); err == nil {
        t.Fatal("expected error for negative output_input_ratio_threshold")
    }
}

func TestValidate_RatioTiers_InvalidMaxRatio(t *testing.T) {
    cfg := DefaultConfig()
    cfg.Routing.RatioTiers.Enabled = true
    cfg.Routing.RatioTiers.Rules = []RatioTierRule{{MaxRatio: 0, Backend: "cloud"}}
    if err := validate(&cfg); err == nil {
        t.Fatal("expected error for max_ratio <= 0")
    }
}

func TestValidate_RatioTiers_EmptyBackend(t *testing.T) {
    cfg := DefaultConfig()
    cfg.Routing.RatioTiers.Enabled = true
    cfg.Routing.RatioTiers.Rules = []RatioTierRule{{MaxRatio: 0.5, Backend: ""}}
    if err := validate(&cfg); err == nil {
        t.Fatal("expected error for empty backend")
    }
}

func TestValidate_RatioTiers_Valid(t *testing.T) {
    cfg := DefaultConfig()
    cfg.Routing.RatioTiers.Enabled = true
    cfg.Routing.RatioTiers.Rules = []RatioTierRule{{MaxRatio: 0.5, Backend: "cloud"}}
    if err := validate(&cfg); err != nil {
        t.Fatalf("valid ratio tier should pass: %v", err)
    }
}

func TestValidate_MaxSystemMemoryRatio_Zero(t *testing.T) {
    cfg := DefaultConfig()
    cfg.Routing.LocalPriority.MaxSystemMemoryRatio = 0
    if err := validate(&cfg); err == nil {
        t.Fatal("expected error for zero memory ratio")
    }
}

func TestValidate_MaxSystemMemoryRatio_OverOne(t *testing.T) {
    cfg := DefaultConfig()
    cfg.Routing.LocalPriority.MaxSystemMemoryRatio = 1.5
    if err := validate(&cfg); err == nil {
        t.Fatal("expected error for memory ratio > 1")
    }
}

func TestValidate_BaseURLConflict(t *testing.T) {
    cfg := DefaultConfig()
    cfg.Validation.BaseURLConflictCheck = true
    cfg.Backends = map[string]BackendConfig{
        "a": {BaseURL: "http://same:8080", Enabled: true},
        "b": {BaseURL: "http://same:8080", Enabled: true},
    }
    if err := validate(&cfg); err == nil {
        t.Fatal("expected error for base_url conflict")
    }
}

func TestValidate_BaseURLConflict_Disabled(t *testing.T) {
    cfg := DefaultConfig()
    cfg.Validation.BaseURLConflictCheck = true
    cfg.Backends = map[string]BackendConfig{
        "a": {BaseURL: "http://same:8080", Enabled: true},
        "b": {BaseURL: "http://same:8080", Enabled: false},
    }
    if err := validate(&cfg); err != nil {
        t.Fatalf("disabled backend should not conflict: %v", err)
    }
}

func TestValidate_AdminJWTSecret_TooShort(t *testing.T) {
    cfg := DefaultConfig()
    cfg.Admin = &AdminConfig{Enabled: true, JWTSecret: "short"}
    if err := validate(&cfg); err == nil {
        t.Fatal("expected error for short JWT secret")
    }
}

func TestValidate_AdminPassword_TooShort(t *testing.T) {
    cfg := DefaultConfig()
    cfg.Admin = &AdminConfig{
        Enabled:   true,
        JWTSecret: "this-is-a-very-long-secret-key-32chars",
        Users:     map[string]string{"admin": "123"},
    }
    if err := validate(&cfg); err == nil {
        t.Fatal("expected error for short password")
    }
}

func TestValidate_Admin_Valid(t *testing.T) {
    cfg := DefaultConfig()
    cfg.Admin = &AdminConfig{
        Enabled:   true,
        JWTSecret: "this-is-a-very-long-secret-key-32chars",
        Users:     map[string]string{"admin": "securepassword123"},
    }
    if err := validate(&cfg); err != nil {
        t.Fatalf("valid admin config should pass: %v", err)
    }
}

func TestValidate_MaxConcurrent_Negative(t *testing.T) {
    cfg := DefaultConfig()
    cfg.Routing.LocalPriority.MaxConcurrent = -1
    if err := validate(&cfg); err == nil {
        t.Fatal("expected error for negative max_concurrent")
    }
}

func TestValidate_CacheMaxMemoryMB_Negative(t *testing.T) {
    cfg := DefaultConfig()
    cfg.Cache.MaxMemoryMB = -1
    if err := validate(&cfg); err == nil {
        t.Fatal("expected error for negative cache max_memory_mb")
    }
}

func TestValidate_CacheMaxEntries_Negative(t *testing.T) {
    cfg := DefaultConfig()
    cfg.Cache.MaxEntries = -1
    if err := validate(&cfg); err == nil {
        t.Fatal("expected error for negative cache max_entries")
    }
}

func TestValidate_ClusterNodeAddress_NoProtocol(t *testing.T) {
    cfg := DefaultConfig()
    cfg.Cluster.Nodes = []ClusterNodeConfig{{ID: "n1", Address: "localhost:8080"}}
    if err := validate(&cfg); err == nil {
        t.Fatal("expected error for node address without http(s):// prefix")
    }
}

func TestValidate_ClusterNodeAddress_HTTP(t *testing.T) {
    cfg := DefaultConfig()
    cfg.Cluster.Nodes = []ClusterNodeConfig{{ID: "n1", Address: "http://localhost:8080"}}
    if err := validate(&cfg); err != nil {
        t.Fatalf("http:// address should be valid: %v", err)
    }
}

func TestValidate_ClusterNodeAddress_HTTPS(t *testing.T) {
    cfg := DefaultConfig()
    cfg.Cluster.Nodes = []ClusterNodeConfig{{ID: "n1", Address: "https://localhost:8080"}}
    if err := validate(&cfg); err != nil {
        t.Fatalf("https:// address should be valid: %v", err)
    }
}

func TestLoad_NonExistentFile(t *testing.T) {
    _, err := Load("/nonexistent/path/config.yaml")
    if err == nil {
        t.Fatal("expected error for non-existent file")
    }
    t.Logf("error: %v", err)
}

func TestLoad_InvalidYAML(t *testing.T) {
    dir := t.TempDir()
    f := filepath.Join(dir, "config.yaml")
    if err := os.WriteFile(f, []byte("{{invalid yaml"), 0644); err != nil {
        t.Fatal(err)
    }
    _, err := Load(f)
    if err == nil {
        t.Fatal("expected error for invalid YAML")
    }
    t.Logf("error: %v", err)
}

func TestLoad_ValidYAML_ValidationFails(t *testing.T) {
    dir := t.TempDir()
    f := filepath.Join(dir, "config.yaml")
    content := `
server:
  port: 0
routing:
  token_threshold: 8000
`
    if err := os.WriteFile(f, []byte(content), 0644); err != nil {
        t.Fatal(err)
    }
    _, err := Load(f)
    if err == nil {
        t.Fatal("expected error for invalid config values")
    }
    t.Logf("error: %v", err)
}

func TestGetSnapshot_Default(t *testing.T) {
    globalConfig.Store(nil)
    snap := GetSnapshot()
    if snap == nil {
        t.Fatal("expected non-nil snapshot")
    }
    if snap.Version != 0 {
        t.Fatalf("expected version 0 for default, got %d", snap.Version)
    }
}

func TestOnReload_HandlerCalled(t *testing.T) {
    called := false
    OnReload(func(old, newSnap *ConfigSnapshot) {
        called = true
    })

    cfg := DefaultConfig()
    oldSnap := &ConfigSnapshot{Config: cfg, Version: 1}
    newSnap := &ConfigSnapshot{Config: cfg, Version: 2}

    configMu.RLock()
    handlers := make([]func(old, new *ConfigSnapshot), len(onReloadHandlers))
    copy(handlers, onReloadHandlers)
    configMu.RUnlock()

    for _, fn := range handlers {
        fn(oldSnap, newSnap)
    }
    if !called {
        t.Fatal("handler should have been called")
    }
}

func TestWatchAndReload_Disabled(t *testing.T) {
    cfg := DefaultConfig()
    cfg.HotReload.Enabled = false
    snap := &ConfigSnapshot{Config: cfg, Version: 1}
    globalConfig.Store(snap)
    WatchAndReload("nonexistent.yaml")
}

func TestContextSnapshot_MissingContext(t *testing.T) {
    ctx := context.Background()
    snap := SnapshotFromContext(ctx)
    if snap == nil {
        t.Fatal("should return default snapshot when context has none")
    }
}

func TestVersionFromContext_MissingContext(t *testing.T) {
    ctx := context.Background()
    ver := VersionFromContext(ctx)
    if ver != 0 {
        t.Fatalf("expected 0 for missing context version, got %d", ver)
    }
}

func TestAuditConfigChange_Disabled(t *testing.T) {
    cfg := DefaultConfig()
    cfg.Observability.ConfigAuditLog = false
    oldSnap := &ConfigSnapshot{Config: cfg, Version: 1}
    newSnap := &ConfigSnapshot{Config: cfg, Version: 2}
    AuditConfigChange(oldSnap, newSnap)
}

func TestAuditConfigChange_NoChanges(t *testing.T) {
    cfg := DefaultConfig()
    cfg.Observability.ConfigAuditLog = true
    oldSnap := &ConfigSnapshot{Config: cfg, Version: 1}
    newSnap := &ConfigSnapshot{Config: cfg, Version: 2}
    AuditConfigChange(oldSnap, newSnap)
}

func TestAuditConfigChange_WithChanges(t *testing.T) {
    cfg1 := DefaultConfig()
    cfg2 := DefaultConfig()
    cfg2.Server.Port = 9090
    cfg2.Observability.ConfigAuditLog = true

    dir := t.TempDir()
    auditFile := filepath.Join(dir, "audit.jsonl")
    cfg2.Observability.ConfigAuditFile = auditFile

    oldSnap := &ConfigSnapshot{Config: cfg1, Version: 1}
    newSnap := &ConfigSnapshot{Config: cfg2, Version: 2}
    AuditConfigChange(oldSnap, newSnap)

    time.Sleep(100 * time.Millisecond)
    data, err := os.ReadFile(auditFile)
    if err != nil {
        t.Logf("audit file read error (may be ok if no write): %v", err)
    } else {
        t.Logf("audit content: %s", string(data))
    }
}

func TestDiffConfigs_SimpleChange(t *testing.T) {
    cfg1 := DefaultConfig()
    cfg2 := DefaultConfig()
    cfg2.Server.Port = 9090
    changes := diffConfigs(cfg1, cfg2, "")
    if len(changes) == 0 {
        t.Fatal("expected changes")
    }
    found := false
    for _, c := range changes {
        if c.Field == "Server.Port" {
            found = true
            if c.Old != 11432 || c.New != 9090 {
                t.Fatalf("expected 11432->9090, got %v->%v", c.Old, c.New)
            }
        }
    }
    if !found {
        t.Fatal("expected Server.Port change")
    }
}

func TestDiffConfigs_NoChange(t *testing.T) {
    cfg := DefaultConfig()
    changes := diffConfigs(cfg, cfg, "")
    if len(changes) != 0 {
        t.Fatalf("expected 0 changes, got %d", len(changes))
    }
}

func TestDiffConfigs_MapChange(t *testing.T) {
    cfg1 := DefaultConfig()
    cfg2 := DefaultConfig()
    cfg2.Backends = map[string]BackendConfig{
        "cloud": {Type: "openai", BaseURL: "http://cloud"},
    }
    changes := diffConfigs(cfg1, cfg2, "")
    if len(changes) == 0 {
        t.Fatal("expected changes for backends map")
    }
    t.Logf("map changes: %v", changes)
}

func TestIsSensitivePath(t *testing.T) {
    cases := []struct {
        path     string
        expected bool
    }{
        {"APIKey", true},
        {"Auth.APIKey", true},
        {"Backend.Key", true},
        {"Password", true},
        {"Secret", true},
        {"SharedToken", true},
        {"Port", false},
        {"Server.Port", false},
        {"Name", false},
    }
    for _, tc := range cases {
        got := isSensitivePath(tc.path)
        if got != tc.expected {
            t.Errorf("isSensitivePath(%q) = %v, want %v", tc.path, got, tc.expected)
        }
    }
}
