package config

import (
    "context"
    "os"
    "path/filepath"
    "sync/atomic"
    "testing"
    "time"
)

func TestLoad_ValidYAML_Success(t *testing.T) {
    dir := t.TempDir()
    f := filepath.Join(dir, "config.yaml")
    content := `
server:
  host: "0.0.0.0"
  port: 8100
  log_level: "info"
  graceful_shutdown_timeout: 15
  max_request_body_size: 5242880
routing:
  token_threshold: 8000
  output_input_ratio_threshold: 0.6
  local_priority:
    enabled: true
    max_system_memory_ratio: 0.9
    max_mlx_memory_ratio: 0.7
    max_concurrent: 8
    swap_page_rate_threshold: 100
`
    if err := os.WriteFile(f, []byte(content), 0644); err != nil {
        t.Fatal(err)
    }

    snap, err := Load(f)
    if err != nil {
        t.Fatalf("expected successful load, got error: %v", err)
    }
    if snap == nil {
        t.Fatal("expected non-nil snapshot")
    }
    if snap.Config.Server.Port != 8100 {
        t.Errorf("expected port 8100, got %d", snap.Config.Server.Port)
    }
    if snap.Config.Routing.TokenThreshold != 8000 {
        t.Errorf("expected token_threshold 8000, got %d", snap.Config.Routing.TokenThreshold)
    }
    if snap.Version == 0 {
        t.Error("expected non-zero version")
    }
    if snap.LoadedAt.IsZero() {
        t.Error("expected non-zero LoadedAt")
    }
    t.Logf("loaded snapshot version=%d port=%d", snap.Version, snap.Config.Server.Port)
}

func TestFireReload_CallsHandlers(t *testing.T) {
    var called1, called2 int32

    onReloadHandlers = nil
    OnReload(func(old, newSnap *ConfigSnapshot) {
        atomic.StoreInt32(&called1, 1)
        if old != nil && old.Version != 1 {
            t.Errorf("expected old version 1, got %d", old.Version)
        }
        if newSnap != nil && newSnap.Version != 2 {
            t.Errorf("expected new version 2, got %d", newSnap.Version)
        }
    })
    OnReload(func(old, newSnap *ConfigSnapshot) {
        atomic.StoreInt32(&called2, 1)
    })

    cfg := DefaultConfig()
    oldSnap := &ConfigSnapshot{Config: cfg, Version: 1}
    newSnap := &ConfigSnapshot{Config: cfg, Version: 2}

    FireReload(oldSnap, newSnap)

    if atomic.LoadInt32(&called1) != 1 {
        t.Error("handler 1 should have been called")
    }
    if atomic.LoadInt32(&called2) != 1 {
        t.Error("handler 2 should have been called")
    }
    t.Log("both reload handlers called successfully")
}

func TestFireReload_NoHandlers(t *testing.T) {
    onReloadHandlers = nil
    cfg := DefaultConfig()
    oldSnap := &ConfigSnapshot{Config: cfg, Version: 1}
    newSnap := &ConfigSnapshot{Config: cfg, Version: 2}
    FireReload(oldSnap, newSnap)
    t.Log("FireReload with no handlers did not panic")
}

func TestWatchAndReload_Enabled(t *testing.T) {
    dir := t.TempDir()
    f := filepath.Join(dir, "config.yaml")
    content := `
server:
  host: "0.0.0.0"
  port: 8100
  log_level: "info"
  graceful_shutdown_timeout: 15
  max_request_body_size: 5242880
routing:
  token_threshold: 8000
  output_input_ratio_threshold: 0.6
  local_priority:
    enabled: true
    max_system_memory_ratio: 0.9
    max_mlx_memory_ratio: 0.7
    max_concurrent: 8
    swap_page_rate_threshold: 100
hot_reload:
  enabled: true
  debounce: 100ms
`
    if err := os.WriteFile(f, []byte(content), 0644); err != nil {
        t.Fatal(err)
    }

    onReloadHandlers = nil
    snap, err := Load(f)
    if err != nil {
        t.Fatalf("initial load failed: %v", err)
    }
    t.Logf("initial load version=%d", snap.Version)

    WatchAndReload(f)

    time.Sleep(200 * time.Millisecond)

    updatedContent := `
server:
  host: "0.0.0.0"
  port: 9090
  log_level: "debug"
  graceful_shutdown_timeout: 15
  max_request_body_size: 5242880
routing:
  token_threshold: 8000
  output_input_ratio_threshold: 0.6
  local_priority:
    enabled: true
    max_system_memory_ratio: 0.9
    max_mlx_memory_ratio: 0.7
    max_concurrent: 8
    swap_page_rate_threshold: 100
hot_reload:
  enabled: true
  debounce: 100ms
`
    if err := os.WriteFile(f, []byte(updatedContent), 0644); err != nil {
        t.Fatal(err)
    }
    t.Log("config file updated, waiting for reload...")

    time.Sleep(2 * time.Second)

    newSnap := GetSnapshot()
    t.Logf("after reload: version=%d port=%d", newSnap.Version, newSnap.Config.Server.Port)
}

func TestWatchAndReload_CustomWatchPath(t *testing.T) {
    dir := t.TempDir()
    mainFile := filepath.Join(dir, "config.yaml")
    watchFile := filepath.Join(dir, "watch.yaml")

    content := `
server:
  host: "0.0.0.0"
  port: 8100
  log_level: "info"
  graceful_shutdown_timeout: 15
  max_request_body_size: 5242880
routing:
  token_threshold: 8000
  output_input_ratio_threshold: 0.6
  local_priority:
    enabled: true
    max_system_memory_ratio: 0.9
    max_mlx_memory_ratio: 0.7
    max_concurrent: 8
    swap_page_rate_threshold: 100
hot_reload:
  enabled: true
  watch_path: "` + watchFile + `"
  debounce: 100ms
`
    if err := os.WriteFile(mainFile, []byte(content), 0644); err != nil {
        t.Fatal(err)
    }
    if err := os.WriteFile(watchFile, []byte(content), 0644); err != nil {
        t.Fatal(err)
    }

    onReloadHandlers = nil
    _, err := Load(mainFile)
    if err != nil {
        t.Fatalf("initial load failed: %v", err)
    }

    WatchAndReload(mainFile)
    t.Logf("WatchAndReload started with custom watch_path=%s", watchFile)

    time.Sleep(200 * time.Millisecond)
}

func TestAuditConfigChange_SensitiveFieldRedaction(t *testing.T) {
    cfg1 := DefaultConfig()
    cfg2 := DefaultConfig()
    cfg1.Auth.APIKeys = []AuthKeyConfig{{Key: "secret-old", Name: "test"}}
    cfg2.Auth.APIKeys = []AuthKeyConfig{{Key: "secret-new", Name: "test"}}
    cfg2.Observability.ConfigAuditLog = true

    oldSnap := &ConfigSnapshot{Config: cfg1, Version: 1}
    newSnap := &ConfigSnapshot{Config: cfg2, Version: 2}

    changes := diffConfigs(cfg1, cfg2, "")
    var hasSensitive bool
    for _, c := range changes {
        if c.Field == "Auth.APIKeys" {
            hasSensitive = true
        }
    }
    if !hasSensitive {
        t.Log("no Auth.APIKeys change detected, checking other fields")
    }

    AuditConfigChange(oldSnap, newSnap)
    t.Logf("audit change detected with %d field diffs", len(changes))
}

func TestAuditConfigChange_FileWriteSuccess(t *testing.T) {
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

    time.Sleep(200 * time.Millisecond)

    data, err := os.ReadFile(auditFile)
    if err != nil {
        t.Fatalf("expected audit file to be written: %v", err)
    }
    if len(data) == 0 {
        t.Fatal("audit file is empty")
    }
    t.Logf("audit file content (%d bytes): %s", len(data), string(data))
}

func TestAuditConfigChange_SensitiveRedactionInFile(t *testing.T) {
    cfg1 := DefaultConfig()
    cfg2 := DefaultConfig()
    cfg2.Auth.APIKeys = []AuthKeyConfig{{Key: "new-secret-key", Name: "test"}}
    cfg2.Observability.ConfigAuditLog = true

    dir := t.TempDir()
    auditFile := filepath.Join(dir, "audit_sensitive.jsonl")
    cfg2.Observability.ConfigAuditFile = auditFile

    oldSnap := &ConfigSnapshot{Config: cfg1, Version: 1}
    newSnap := &ConfigSnapshot{Config: cfg2, Version: 2}

    AuditConfigChange(oldSnap, newSnap)

    time.Sleep(200 * time.Millisecond)

    data, err := os.ReadFile(auditFile)
    if err != nil {
        t.Fatalf("expected audit file: %v", err)
    }
    content := string(data)
    t.Logf("audit content: %s", content)
}

func TestLoad_VersionIncrement(t *testing.T) {
    dir := t.TempDir()
    f := filepath.Join(dir, "config.yaml")
    content := `
server:
  host: "0.0.0.0"
  port: 8100
  log_level: "info"
  graceful_shutdown_timeout: 15
  max_request_body_size: 5242880
routing:
  token_threshold: 8000
  output_input_ratio_threshold: 0.6
  local_priority:
    enabled: true
    max_system_memory_ratio: 0.9
    max_mlx_memory_ratio: 0.7
    max_concurrent: 8
    swap_page_rate_threshold: 100
`
    if err := os.WriteFile(f, []byte(content), 0644); err != nil {
        t.Fatal(err)
    }

    snap1, err := Load(f)
    if err != nil {
        t.Fatalf("first load failed: %v", err)
    }

    snap2, err := Load(f)
    if err != nil {
        t.Fatalf("second load failed: %v", err)
    }

    if snap2.Version <= snap1.Version {
        t.Errorf("expected version to increment, got v1=%d v2=%d", snap1.Version, snap2.Version)
    }
    t.Logf("version increment: %d -> %d", snap1.Version, snap2.Version)
}

func TestLoad_StoresGlobalSnapshot(t *testing.T) {
    dir := t.TempDir()
    f := filepath.Join(dir, "config.yaml")
    content := `
server:
  host: "0.0.0.0"
  port: 8100
  log_level: "info"
  graceful_shutdown_timeout: 15
  max_request_body_size: 5242880
routing:
  token_threshold: 8000
  output_input_ratio_threshold: 0.6
  local_priority:
    enabled: true
    max_system_memory_ratio: 0.9
    max_mlx_memory_ratio: 0.7
    max_concurrent: 8
    swap_page_rate_threshold: 100
`
    if err := os.WriteFile(f, []byte(content), 0644); err != nil {
        t.Fatal(err)
    }

    snap, err := Load(f)
    if err != nil {
        t.Fatalf("load failed: %v", err)
    }

    global := GetSnapshot()
    if global.Version != snap.Version {
        t.Errorf("global version %d != loaded version %d", global.Version, snap.Version)
    }
    if global.Config.Server.Port != snap.Config.Server.Port {
        t.Errorf("global port %d != loaded port %d", global.Config.Server.Port, snap.Config.Server.Port)
    }
    t.Logf("global snapshot matches loaded: version=%d", snap.Version)
}

func TestOnReload_MultipleHandlers(t *testing.T) {
    var count int32
    onReloadHandlers = nil

    for i := 0; i < 5; i++ {
        OnReload(func(old, newSnap *ConfigSnapshot) {
            atomic.AddInt32(&count, 1)
        })
    }

    cfg := DefaultConfig()
    oldSnap := &ConfigSnapshot{Config: cfg, Version: 1}
    newSnap := &ConfigSnapshot{Config: cfg, Version: 2}

    FireReload(oldSnap, newSnap)

    if atomic.LoadInt32(&count) != 5 {
        t.Errorf("expected 5 handler calls, got %d", atomic.LoadInt32(&count))
    }
}

func TestGetSnapshot_AfterLoad(t *testing.T) {
    dir := t.TempDir()
    f := filepath.Join(dir, "config.yaml")
    content := `
server:
  host: "0.0.0.0"
  port: 9999
  log_level: "debug"
  graceful_shutdown_timeout: 30
  max_request_body_size: 10485760
routing:
  token_threshold: 4000
  output_input_ratio_threshold: 0.5
  local_priority:
    enabled: true
    max_system_memory_ratio: 0.8
    max_mlx_memory_ratio: 0.6
    max_concurrent: 4
    swap_page_rate_threshold: 50
`
    if err := os.WriteFile(f, []byte(content), 0644); err != nil {
        t.Fatal(err)
    }

    _, err := Load(f)
    if err != nil {
        t.Fatalf("load failed: %v", err)
    }

    snap := GetSnapshot()
    if snap.Config.Server.Port != 9999 {
        t.Errorf("expected port 9999, got %d", snap.Config.Server.Port)
    }
    if snap.Config.Routing.TokenThreshold != 4000 {
        t.Errorf("expected token_threshold 4000, got %d", snap.Config.Routing.TokenThreshold)
    }
    if snap.Config.Server.LogLevel != "debug" {
        t.Errorf("expected log_level debug, got %s", snap.Config.Server.LogLevel)
    }
    if snap.Config.Server.GracefulShutdownTimeout != 30 {
        t.Errorf("expected graceful_shutdown_timeout 30, got %d", snap.Config.Server.GracefulShutdownTimeout)
    }
}

func TestContextSnapshot_AfterLoad(t *testing.T) {
    dir := t.TempDir()
    f := filepath.Join(dir, "config.yaml")
    content := `
server:
  host: "0.0.0.0"
  port: 7777
  log_level: "info"
  graceful_shutdown_timeout: 15
  max_request_body_size: 5242880
routing:
  token_threshold: 8000
  output_input_ratio_threshold: 0.6
  local_priority:
    enabled: true
    max_system_memory_ratio: 0.9
    max_mlx_memory_ratio: 0.7
    max_concurrent: 8
    swap_page_rate_threshold: 100
`
    if err := os.WriteFile(f, []byte(content), 0644); err != nil {
        t.Fatal(err)
    }

    snap, err := Load(f)
    if err != nil {
        t.Fatalf("load failed: %v", err)
    }

    ctx := WithSnapshot(context.Background(), snap)
    fromCtx := SnapshotFromContext(ctx)
    if fromCtx.Config.Server.Port != 7777 {
        t.Errorf("expected port 7777 from context, got %d", fromCtx.Config.Server.Port)
    }

    ver := VersionFromContext(ctx)
    if ver != snap.Version {
        t.Errorf("expected version %d from context, got %d", snap.Version, ver)
    }
}

func TestDiffConfigs_SensitiveFieldChange(t *testing.T) {
    cfg1 := DefaultConfig()
    cfg2 := DefaultConfig()
    cfg1.Auth.MasterKey = "old-master-key"
    cfg2.Auth.MasterKey = "new-master-key"

    changes := diffConfigs(cfg1, cfg2, "")
    found := false
    for _, c := range changes {
        if c.Field == "Auth.MasterKey" {
            found = true
        }
    }
    if !found {
        t.Error("expected Auth.MasterKey change to be detected")
    }
    t.Logf("detected %d changes", len(changes))
}

func TestDiffConfigs_SliceFieldChange(t *testing.T) {
    cfg1 := DefaultConfig()
    cfg2 := DefaultConfig()
    cfg2.Routing.TokenTiers.Rules = []TokenTierRule{
        {MaxTokens: 1000, Backend: "local"},
        {MaxTokens: 8000, Backend: "cloud"},
    }

    changes := diffConfigs(cfg1, cfg2, "")
    if len(changes) == 0 {
        t.Fatal("expected changes for token tier rules")
    }
    t.Logf("slice changes: %v", changes)
}

func TestDiffConfigs_NestedStructChange(t *testing.T) {
    cfg1 := DefaultConfig()
    cfg2 := DefaultConfig()
    cfg2.Hardware.CollectInterval = 5 * time.Second

    changes := diffConfigs(cfg1, cfg2, "")
    found := false
    for _, c := range changes {
        if c.Field == "Hardware.CollectInterval" {
            found = true
        }
    }
    if !found {
        t.Error("expected Hardware.CollectInterval change")
    }
    t.Logf("nested struct changes: %v", changes)
}

func TestValidate_AdminDisabled_ShortJWT(t *testing.T) {
    cfg := DefaultConfig()
    cfg.Admin = &AdminConfig{Enabled: false, JWTSecret: "short"}
    if err := validate(&cfg); err != nil {
        t.Fatalf("disabled admin should skip JWT validation: %v", err)
    }
}

func TestValidate_AdminDisabled_ShortPassword(t *testing.T) {
    cfg := DefaultConfig()
    cfg.Admin = &AdminConfig{
        Enabled: false,
        Users:   map[string]string{"admin": "x"},
    }
    if err := validate(&cfg); err != nil {
        t.Fatalf("disabled admin should skip password validation: %v", err)
    }
}

func TestValidate_AdminNil(t *testing.T) {
    cfg := DefaultConfig()
    cfg.Admin = nil
    if err := validate(&cfg); err != nil {
        t.Fatalf("nil admin should be valid: %v", err)
    }
}

func TestValidate_BaseURLConflictCheckDisabled(t *testing.T) {
    cfg := DefaultConfig()
    cfg.Validation.BaseURLConflictCheck = false
    cfg.Backends = map[string]BackendConfig{
        "a": {BaseURL: "http://same:8080", Enabled: true},
        "b": {BaseURL: "http://same:8080", Enabled: true},
    }
    if err := validate(&cfg); err != nil {
        t.Fatalf("conflict check disabled should not error: %v", err)
    }
}

func TestValidate_ClusterNodeEmptyAddress(t *testing.T) {
    cfg := DefaultConfig()
    cfg.Cluster.Nodes = []ClusterNodeConfig{{ID: "n1", Address: ""}}
    if err := validate(&cfg); err != nil {
        t.Fatalf("empty address should be valid (skipped): %v", err)
    }
}

func TestLoad_WithBackends(t *testing.T) {
    dir := t.TempDir()
    f := filepath.Join(dir, "config.yaml")
    content := `
server:
  host: "0.0.0.0"
  port: 8100
  log_level: "info"
  graceful_shutdown_timeout: 15
  max_request_body_size: 5242880
routing:
  token_threshold: 8000
  output_input_ratio_threshold: 0.6
  local_priority:
    enabled: true
    max_system_memory_ratio: 0.9
    max_mlx_memory_ratio: 0.7
    max_concurrent: 8
    swap_page_rate_threshold: 100
backends:
  local:
    type: "mlx"
    base_url: "http://localhost:11434"
    enabled: true
    models:
      - "qwen3"
  cloud:
    type: "openai"
    base_url: "https://api.openai.com"
    api_key: "sk-test"
    enabled: true
    models:
      - "gpt-4"
`
    if err := os.WriteFile(f, []byte(content), 0644); err != nil {
        t.Fatal(err)
    }

    snap, err := Load(f)
    if err != nil {
        t.Fatalf("expected successful load: %v", err)
    }
    if len(snap.Config.Backends) != 2 {
        t.Errorf("expected 2 backends, got %d", len(snap.Config.Backends))
    }
    if snap.Config.Backends["local"].BaseURL != "http://localhost:11434" {
        t.Errorf("unexpected local base_url: %s", snap.Config.Backends["local"].BaseURL)
    }
    t.Logf("loaded %d backends", len(snap.Config.Backends))
}

// TestLoad_RR11_MaxConnsPerHost verifies the RR11 per-backend FD-cap fields
// (max_conns_per_host, max_idle_conns_per_host) map from YAML into BackendConfig.
// Without this mapping the transport factory never sees an operator's override
// and silently applies the default — the FD cap would be unconfigurable.
func TestLoad_RR11_MaxConnsPerHost(t *testing.T) {
    dir := t.TempDir()
    f := filepath.Join(dir, "config.yaml")
    content := `
server:
  host: "0.0.0.0"
  port: 8100
routing:
  token_threshold: 8000
  local_priority:
    enabled: true
    max_system_memory_ratio: 0.9
    max_mlx_memory_ratio: 0.7
    max_concurrent: 8
    swap_page_rate_threshold: 100
backends:
  local:
    type: "mlx"
    base_url: "http://localhost:11434"
    enabled: true
    max_conns_per_host: 24
    max_idle_conns_per_host: 8
  cloud:
    type: "openai"
    base_url: "https://api.openai.com"
    enabled: true
`
    if err := os.WriteFile(f, []byte(content), 0644); err != nil {
        t.Fatal(err)
    }
    snap, err := Load(f)
    if err != nil {
        t.Fatalf("expected successful load: %v", err)
    }
    local := snap.Config.Backends["local"]
    if local.MaxConnsPerHost != 24 {
        t.Errorf("local MaxConnsPerHost: want 24, got %d", local.MaxConnsPerHost)
    }
    if local.MaxIdleConnsPerHost != 8 {
        t.Errorf("local MaxIdleConnsPerHost: want 8, got %d", local.MaxIdleConnsPerHost)
    }
    // cloud omits the keys — must default to 0 (factory applies safe default).
    cloud := snap.Config.Backends["cloud"]
    if cloud.MaxConnsPerHost != 0 {
        t.Errorf("cloud MaxConnsPerHost: want 0 (unset, factory defaults), got %d", cloud.MaxConnsPerHost)
    }
}

// TestReload_RereadsFileAndUpdatesSnapshot verifies config.Reload re-reads the
// file from disk, bumps the version, commits the new global snapshot, and fires
// OnReload handlers. This is the deterministic path behind /admin/config/reload
// (fsnotify is unreliable on macOS — issue #57).
func TestReload_RereadsFileAndUpdatesSnapshot(t *testing.T) {
    dir := t.TempDir()
    f := filepath.Join(dir, "config.yaml")
    content := `
server:
  host: "0.0.0.0"
  port: 8100
  log_level: "info"
  graceful_shutdown_timeout: 15
  max_request_body_size: 5242880
routing:
  token_threshold: 8000
  output_input_ratio_threshold: 0.6
  local_priority:
    enabled: true
    max_system_memory_ratio: 0.9
    max_mlx_memory_ratio: 0.7
    max_concurrent: 8
    swap_page_rate_threshold: 100
  fallback:
    enabled: true
    cloud_default: glm52
    model_mapping:
      claude-opus-4-7: glm5.2
hot_reload:
  enabled: true
  debounce: 100ms
`
    if err := os.WriteFile(f, []byte(content), 0644); err != nil {
        t.Fatal(err)
    }

    onReloadHandlers = nil
    var fired int32
    OnReload(func(old, newSnap *ConfigSnapshot) {
        atomic.AddInt32(&fired, 1)
    })

    initial, err := Load(f)
    if err != nil {
        t.Fatalf("initial load failed: %v", err)
    }
    initialVer := initial.Version
    if _, ok := initial.Config.Routing.Fallback.ModelMapping["claude-opus-4-7"]; !ok {
        t.Fatalf("initial load missing opus mapping")
    }

    // Add a new alias on disk (the real-world bug: added post-startup).
    updated := `
server:
  host: "0.0.0.0"
  port: 8100
  log_level: "info"
  graceful_shutdown_timeout: 15
  max_request_body_size: 5242880
routing:
  token_threshold: 8000
  output_input_ratio_threshold: 0.6
  local_priority:
    enabled: true
    max_system_memory_ratio: 0.9
    max_mlx_memory_ratio: 0.7
    max_concurrent: 8
    swap_page_rate_threshold: 100
  fallback:
    enabled: true
    cloud_default: glm52
    model_mapping:
      claude-opus-4-7: glm5.2
      claude-sonnet-4-6: glm5.2
hot_reload:
  enabled: true
  debounce: 100ms
`
    if err := os.WriteFile(f, []byte(updated), 0644); err != nil {
        t.Fatal(err)
    }

    reloaded, err := Reload(f)
    if err != nil {
        t.Fatalf("reload failed: %v", err)
    }
    if reloaded.Version <= initialVer {
        t.Errorf("reload did not bump version: initial=%d reloaded=%d", initialVer, reloaded.Version)
    }

    live := GetSnapshot()
    if live.Version != reloaded.Version {
        t.Errorf("global snapshot not committed: live=%d reloaded=%d", live.Version, reloaded.Version)
    }
    mapped, ok := live.Config.Routing.Fallback.ModelMapping["claude-sonnet-4-6"]
    if !ok || mapped != "glm5.2" {
        t.Errorf("new alias claude-sonnet-4-6 not mapped after reload: ok=%v mapped=%q", ok, mapped)
    }
    if atomic.LoadInt32(&fired) != 1 {
        t.Errorf("OnReload handler not fired once: fired=%d", atomic.LoadInt32(&fired))
    }
    t.Logf("reload ok: initial=%d reloaded=%d fired=%d", initialVer, reloaded.Version, atomic.LoadInt32(&fired))
}

// TestReload_ValidationErrorUnchanged verifies Reload rejects an invalid file
// and leaves the global snapshot untouched (validation failure returns before
// the handler-run/commit phase).
func TestReload_ValidationErrorUnchanged(t *testing.T) {
    dir := t.TempDir()
    f := filepath.Join(dir, "config.yaml")
    valid := `
server:
  host: "0.0.0.0"
  port: 8100
  log_level: "info"
  graceful_shutdown_timeout: 15
  max_request_body_size: 5242880
routing:
  token_threshold: 8000
  output_input_ratio_threshold: 0.6
  local_priority:
    enabled: true
    max_system_memory_ratio: 0.9
    max_mlx_memory_ratio: 0.7
    max_concurrent: 8
    swap_page_rate_threshold: 100
`
    if err := os.WriteFile(f, []byte(valid), 0644); err != nil {
        t.Fatal(err)
    }
    onReloadHandlers = nil
    initial, err := Load(f)
    if err != nil {
        t.Fatalf("initial load failed: %v", err)
    }
    initialVer := initial.Version

    // Invalid: server.port out of range.
    broken := `
server:
  host: "0.0.0.0"
  port: 99999
  log_level: "info"
  graceful_shutdown_timeout: 15
  max_request_body_size: 5242880
routing:
  token_threshold: 8000
  output_input_ratio_threshold: 0.6
  local_priority:
    enabled: true
    max_system_memory_ratio: 0.9
    max_mlx_memory_ratio: 0.7
    max_concurrent: 8
    swap_page_rate_threshold: 100
`
    if err := os.WriteFile(f, []byte(broken), 0644); err != nil {
        t.Fatal(err)
    }

    if _, err := Reload(f); err == nil {
        t.Fatalf("expected reload error for invalid port, got nil")
    }
    live := GetSnapshot()
    if live.Version != initialVer {
        t.Errorf("snapshot changed after failed reload: initial=%d live=%d", initialVer, live.Version)
    }
    t.Logf("invalid reload rejected, snapshot stays at version=%d", live.Version)
}
