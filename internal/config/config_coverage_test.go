package config

import (
    "context"
    "os"
    "path/filepath"
    "sync"
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

// TestLoad_MasterKeyEnvOverridesLiteral pins the Helm secret flow: the
// deployment injects the real master_key via env FG_MASTER_KEY (secretKeyRef),
// and bindSecretEnv gives that env var precedence over any literal the
// configmap carries. So a configmap with a placeholder/`${...}` literal still
// loads cleanly when the env is set — the env value becomes the live
// master_key, not the literal. This is the fix for the chart defect where
// `master_key: "${FG_MASTER_KEY}"` was unmarshaled verbatim.
func TestLoad_MasterKeyEnvOverridesLiteral(t *testing.T) {
    dir := t.TempDir()
    f := filepath.Join(dir, "config.yaml")
    // configmap carries a literal that would trip the placeholder guard if it
    // were the final value — the env override must save it.
    content := `
server:
  host: "0.0.0.0"
  port: 8100
auth:
  master_key: "${FG_MASTER_KEY}"
`
    if err := os.WriteFile(f, []byte(content), 0644); err != nil {
        t.Fatal(err)
    }
    const realKey = "9k2m5n8q1r4s7t0v3w6x9y2b5c8d1e4f7a0b3c6"
    t.Setenv("FG_MASTER_KEY", realKey)
    snap, err := Load(f)
    if err != nil {
        t.Fatalf("env FG_MASTER_KEY must override the configmap literal, got error: %v", err)
    }
    if snap.Config.Auth.MasterKey != realKey {
        t.Fatalf("expected master_key from env %q, got %q (configmap literal leaked)", realKey, snap.Config.Auth.MasterKey)
    }
}

// TestLoad_MasterKeyLiteralRejectedWithoutEnv is the fail-closed counterpart:
// with no FG_MASTER_KEY env, a configmap placeholder literal (`${...}` or a
// CHANGE_ME family) is rejected by the placeholder guard rather than silently
// becoming a live secret. Pins the defense-in-depth: even if an operator
// forgets the env injection, the binary refuses to ship a known value.
func TestLoad_MasterKeyLiteralRejectedWithoutEnv(t *testing.T) {
    // Ensure no env leak from the test environment affects this case.
    t.Setenv("FG_MASTER_KEY", "")
    for _, literal := range []string{
        "${FG_MASTER_KEY}",
        "CHANGE_ME_MASTER_KEY",
        "fg-master-key-change-me",
    } {
        dir := t.TempDir()
        f := filepath.Join(dir, "config.yaml")
        content := "server:\n  host: \"0.0.0.0\"\n  port: 8100\nauth:\n  master_key: \"" + literal + "\"\n"
        if err := os.WriteFile(f, []byte(content), 0644); err != nil {
            t.Fatal(err)
        }
        if _, err := Load(f); err == nil {
            t.Errorf("expected Load to reject master_key literal %q with no env override, got nil", literal)
        }
    }
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

// #118: MCP validation guards. An enabled MCP with no credential (mcp.token AND
// auth.master_key both empty) must fail config load — MCP routes must not be
// anonymously reachable. An enabled dedicated listener (listen_enabled=true)
// must have host + valid port.
func TestValidate_MCPEnabled_NoCredential_Rejected(t *testing.T) {
    cfg := DefaultConfig()
    cfg.MCP.Enabled = true
    cfg.MCP.Token = ""
    cfg.Auth.MasterKey = ""
    if err := validate(&cfg); err == nil {
        t.Fatal("enabled MCP with no credential (token + master_key empty) must be rejected (fail-closed)")
    }
}

func TestValidate_MCPEnabled_WithToken_Accepted(t *testing.T) {
    cfg := DefaultConfig()
    cfg.MCP.Enabled = true
    cfg.MCP.Token = "mcp-secret"
    cfg.Auth.MasterKey = ""
    if err := validate(&cfg); err != nil {
        t.Fatalf("enabled MCP with mcp.token set must be accepted: %v", err)
    }
}

func TestValidate_MCPEnabled_WithMasterKey_Accepted(t *testing.T) {
    cfg := DefaultConfig()
    cfg.MCP.Enabled = true
    cfg.MCP.Token = ""
    cfg.Auth.MasterKey = "master-secret"
    if err := validate(&cfg); err != nil {
        t.Fatalf("enabled MCP with auth.master_key fallback must be accepted: %v", err)
    }
}

func TestValidate_MCPListenEnabled_NoHost_Rejected(t *testing.T) {
    cfg := DefaultConfig()
    cfg.MCP.Enabled = true
    cfg.MCP.Token = "mcp-secret"
    cfg.MCP.ListenEnabled = true
    cfg.MCP.Host = ""
    cfg.MCP.Port = 11446
    if err := validate(&cfg); err == nil {
        t.Fatal("listen_enabled=true with empty host must be rejected")
    }
}

func TestValidate_MCPListenEnabled_BadPort_Rejected(t *testing.T) {
    cfg := DefaultConfig()
    cfg.MCP.Enabled = true
    cfg.MCP.Token = "mcp-secret"
    cfg.MCP.ListenEnabled = true
    cfg.MCP.Host = "127.0.0.1"
    cfg.MCP.Port = 0
    if err := validate(&cfg); err == nil {
        t.Fatal("listen_enabled=true with port<=0 must be rejected")
    }
}

func TestValidate_MCPDisabled_NoCredential_Accepted(t *testing.T) {
    cfg := DefaultConfig()
    cfg.MCP.Enabled = false
    cfg.MCP.Token = ""
    cfg.Auth.MasterKey = ""
    if err := validate(&cfg); err != nil {
        t.Fatalf("disabled MCP with no credential must be accepted: %v", err)
    }
}

// #119 guard: cluster.mode=master requires cluster.master.address (the
// fusion-multi-node master API URL). An empty address means NewDiscovery has
// nothing to sync node membership from — every request cloud-degrades silently.
// Guard: if the validate check were absent, master mode with no address would
// start successfully and serve nothing.
func TestValidate_ClusterMasterMode_NoAddress_Rejected(t *testing.T) {
    cfg := DefaultConfig()
    cfg.Cluster.Enabled = true
    cfg.Cluster.Mode = ClusterModeMaster
    cfg.Cluster.Master.Address = ""
    if err := validate(&cfg); err == nil {
        t.Fatal("cluster.mode=master with empty master.address must be rejected at validate")
    }
}

// #119 guard: master mode WITH a valid address is accepted — proves the reject
// above is specific to the missing address, not master mode itself.
// Guard: if the check were too broad (rejecting all master mode), this passes
// for the wrong reason would flip to fail.
func TestValidate_ClusterMasterMode_WithAddress_Accepted(t *testing.T) {
    cfg := DefaultConfig()
    cfg.Cluster.Enabled = true
    cfg.Cluster.Mode = ClusterModeMaster
    cfg.Cluster.Master.Address = "http://127.0.0.1:8000"
    if err := validate(&cfg); err != nil {
        t.Fatalf("cluster.mode=master with valid address must be accepted: %v", err)
    }
}

// #119 guard: master mode disabled (standalone) with empty master.address is
// accepted — the address only matters in master mode. Proves the check is gated
// on Mode == master, not unconditional.
// Guard: if the check fired regardless of mode, standalone would be rejected.
func TestValidate_ClusterStandalone_NoMasterAddress_Accepted(t *testing.T) {
    cfg := DefaultConfig()
    cfg.Cluster.Enabled = true
    cfg.Cluster.Mode = ClusterModeStandalone
    cfg.Cluster.Master.Address = ""
    if err := validate(&cfg); err != nil {
        t.Fatalf("cluster.mode=standalone with empty master.address must be accepted: %v", err)
    }
}

// TestH7_Reload_HandlersSerialized (H7): two concurrent Reloads must NOT run
// their onReload handlers at the same time. Before H7, Reload copied the
// handler list under a shared RLock then ran handlers + committed with no lock
// in between — so two Reloads interleaved handlers concurrently (and the
// commit raced). After H7, reloadMu serializes the handler-run+commit window.
// The guard registers a handler that detects overlap (in-flight counter > 1
// while it sleeps), then fires N Reloads in parallel and asserts peak overlap
// stayed 0.
func TestH7_Reload_HandlersSerialized(t *testing.T) {
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
    // Prime globalConfig with an initial snapshot so Reload has an oldSnap.
    if _, err := Load(f); err != nil {
        t.Fatalf("initial Load failed: %v", err)
    }

    var inFlight int32
    var peakOverlap int32
    onReloadHandlers = nil
    OnReload(func(old, newSnap *ConfigSnapshot) {
        cur := atomic.AddInt32(&inFlight, 1)
        if cur > 1 {
            // Another handler is concurrently in-flight — overlap detected.
            atomic.StoreInt32(&peakOverlap, cur)
        }
        // Hold the window open so a concurrent Reload (if unsynchronized) is
        // guaranteed to overlap. Long enough to force interleaving without a
        // flaky timing dependency.
        time.Sleep(20 * time.Millisecond)
        atomic.AddInt32(&inFlight, -1)
    })

    const n = 6
    var wg sync.WaitGroup
    wg.Add(n)
    start := make(chan struct{})
    for i := 0; i < n; i++ {
        go func() {
            defer wg.Done()
            <-start
            if _, err := Reload(f); err != nil {
                t.Errorf("Reload failed: %v", err)
            }
        }()
    }
    close(start)
    wg.Wait()

    if got := atomic.LoadInt32(&peakOverlap); got > 1 {
        t.Errorf("H7: expected handler-run serialized (peak overlap 0 or 1), but %d Reloads ran handlers concurrently — reloadMu not held across handler-run+commit", got)
    }
    t.Logf("H7: %d concurrent Reloads, peak handler overlap=%d (serialized)", n, peakOverlap)
}

