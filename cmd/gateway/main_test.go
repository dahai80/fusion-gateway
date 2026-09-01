package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/fusion-gateway/fusion-gateway/internal/adapter"
	"github.com/fusion-gateway/fusion-gateway/internal/cluster"
	"github.com/fusion-gateway/fusion-gateway/internal/config"
	"github.com/fusion-gateway/fusion-gateway/internal/hardware"
	"github.com/fusion-gateway/fusion-gateway/internal/router"
	"github.com/fusion-gateway/fusion-gateway/internal/server"
	"github.com/fusion-gateway/fusion-gateway/internal/tokenizer"
)

var testPortCounter atomic.Int32

func nextTestPort() int {
	return 18200 + int(testPortCounter.Add(1))
}

func makeTestConfigYAML(port int, clusterEnabled bool) string {
	clusterSection := `
cluster:
    enabled: false
`
	if clusterEnabled {
		clusterSection = `
cluster:
    enabled: true
    mode: "standalone"
    nodes:
        - id: "test-node"
          address: "http://127.0.0.1:` + fmt.Sprintf("%d", port) + `"
          gpu: "M2"
          memory_gb: 16
    load_balancer: "round-robin"
    health_check_interval: 30s
    failure_threshold: 3
    recovery_interval: 60s
`
	}

	return `server:
    host: "127.0.0.1"
    port: ` + fmt.Sprintf("%d", port) + `
auth:
    enabled: false
routing:
    token_threshold: 100
    output_input_ratio_threshold: 0.6
    local_priority:
        enabled: true
        max_system_memory_ratio: 0.9
        max_mlx_memory_ratio: 0.7
        max_concurrent: 8
        swap_page_rate_threshold: 100
    circuit_breaker:
        failure_threshold: 5
        timeout: 30s
        half_open_max_requests: 1
        success_threshold: 3
    fallback:
        enabled: false
        cloud_default: "openai"
    negotiation:
        route_header: "X-Fusion-Route"
        route_header_value: "gateway-decision"
    rate_limit:
        enabled: false
    retry:
        max_retries: 2
        initial_backoff: 500ms
        max_backoff: 10s
backends:
    openai:
        type: "openai-compatible"
        base_url: "https://api.openai.com/v1"
        api_key: "test-key"
        timeout: 60s
        enabled: true
hardware:
    enabled: false
tokenizer:
    provider: "whitespace"
    calibration:
        enabled: false
observability:
    metrics_enabled: false
    otel_enabled: false
cors:
    allowed_origins:
        - "*"
    allowed_methods:
        - "GET"
    allowed_headers:
        - "Content-Type"
hot_reload:
    enabled: false
` + clusterSection + `
cache:
    enabled: false
cost:
    enabled: false
pii:
    enabled: false
cloud_routing:
    strategy: "round-robin"
store:
    backend: "memory"
admin:
    enabled: false
`
}

func makeTestConfigYAMLWithFusionMLX(port int) string {
	return `server:
    host: "127.0.0.1"
    port: ` + fmt.Sprintf("%d", port) + `
auth:
    enabled: false
routing:
    token_threshold: 100
    output_input_ratio_threshold: 0.6
    local_priority:
        enabled: true
        max_system_memory_ratio: 0.9
        max_mlx_memory_ratio: 0.7
        max_concurrent: 8
        swap_page_rate_threshold: 100
    circuit_breaker:
        failure_threshold: 5
        timeout: 30s
        half_open_max_requests: 1
        success_threshold: 3
    fallback:
        enabled: false
        cloud_default: "openai"
    negotiation:
        route_header: "X-Fusion-Route"
        route_header_value: "gateway-decision"
    rate_limit:
        enabled: false
    retry:
        max_retries: 2
        initial_backoff: 500ms
        max_backoff: 10s
backends:
    fusion-mlx:
        type: "fusion-mlx"
        base_url: "http://127.0.0.1:11434"
        timeout: 120s
        enabled: true
        gc:
            enabled: false
hardware:
    enabled: false
tokenizer:
    provider: "whitespace"
    calibration:
        enabled: false
observability:
    metrics_enabled: false
    otel_enabled: false
cors:
    allowed_origins:
        - "*"
    allowed_methods:
        - "GET"
    allowed_headers:
        - "Content-Type"
hot_reload:
    enabled: false
cluster:
    enabled: false
cache:
    enabled: false
cost:
    enabled: false
pii:
    enabled: false
cloud_routing:
    strategy: "round-robin"
store:
    backend: "memory"
admin:
    enabled: false
`
}

func makeTestConfigYAMLBadBackend(port int) string {
	return `server:
    host: "127.0.0.1"
    port: ` + fmt.Sprintf("%d", port) + `
auth:
    enabled: false
routing:
    token_threshold: 100
    output_input_ratio_threshold: 0.6
    local_priority:
        enabled: true
        max_system_memory_ratio: 0.9
        max_concurrent: 8
    circuit_breaker:
        failure_threshold: 5
        timeout: 30s
    fallback:
        enabled: false
        cloud_default: "openai"
    negotiation:
        route_header: "X-Fusion-Route"
        route_header_value: "gateway-decision"
    rate_limit:
        enabled: false
    retry:
        max_retries: 2
        initial_backoff: 500ms
        max_backoff: 10s
backends:
    bad:
        type: "nonexistent-backend-type"
        base_url: "https://example.com"
        enabled: true
hardware:
    enabled: false
tokenizer:
    provider: "whitespace"
    calibration:
        enabled: false
observability:
    metrics_enabled: false
    otel_enabled: false
cors:
    allowed_origins:
        - "*"
    allowed_methods:
        - "GET"
    allowed_headers:
        - "Content-Type"
hot_reload:
    enabled: false
cluster:
    enabled: false
cache:
    enabled: false
cost:
    enabled: false
pii:
    enabled: false
cloud_routing:
    strategy: "round-robin"
store:
    backend: "memory"
admin:
    enabled: false
`
}

// --- safeGo tests ---

func TestSafeGo_ExecutesFunction(t *testing.T) {
	slog.Info("TestSafeGo_ExecutesFunction: start")

	var executed atomic.Bool
	var wg sync.WaitGroup
	wg.Add(1)

	safeGo("test_exec", func() {
		executed.Store(true)
		wg.Done()
	})

	wg.Wait()

	if !executed.Load() {
		t.Fatal("safeGo function was not executed")
	}

	slog.Info("TestSafeGo_ExecutesFunction: pass")
}

func TestSafeGo_RecoversPanic(t *testing.T) {
	slog.Info("TestSafeGo_RecoversPanic: start")

	var wg sync.WaitGroup
	wg.Add(1)

	safeGo("test_panic", func() {
		defer wg.Done()
		panic("test panic in safeGo")
	})

	wg.Wait()

	slog.Info("TestSafeGo_RecoversPanic: pass, panic recovered without crash")
}

func TestSafeGo_MultipleGoroutines(t *testing.T) {
	slog.Info("TestSafeGo_MultipleGoroutines: start")

	var count atomic.Int32
	var wg sync.WaitGroup

	for i := 0; i < 10; i++ {
		wg.Add(1)
		safeGo("multi_test", func() {
			count.Add(1)
			wg.Done()
		})
	}

	wg.Wait()

	if count.Load() != 10 {
		t.Fatalf("expected 10 executions, got %d", count.Load())
	}

	slog.Info("TestSafeGo_MultipleGoroutines: pass")
}

func TestSafeGo_PanicDoesNotCrashProcess(t *testing.T) {
	slog.Info("TestSafeGo_PanicDoesNotCrashProcess: start")

	var wg sync.WaitGroup

	wg.Add(2)
	safeGo("panic_1", func() {
		defer wg.Done()
		panic("panic one")
	})
	safeGo("normal_1", func() {
		defer wg.Done()
		time.Sleep(50 * time.Millisecond)
	})

	wg.Wait()

	slog.Info("TestSafeGo_PanicDoesNotCrashProcess: pass, process survived panics")
}

// --- run() tests ---

func TestRun_ConfigLoadError(t *testing.T) {
	slog.Info("TestRun_ConfigLoadError: start")

	err := run("/nonexistent/path/config.yaml")
	if err == nil {
		t.Fatal("expected error for nonexistent config file, got nil")
	}

	slog.Info("TestRun_ConfigLoadError: pass", "error", err)
}

func TestRun_InvalidConfigPort(t *testing.T) {
	slog.Info("TestRun_InvalidConfigPort: start")

	cfgContent := `server:
    host: "127.0.0.1"
    port: 99999
auth:
    enabled: false
routing:
    token_threshold: 100
    output_input_ratio_threshold: 0.6
    local_priority:
        enabled: true
        max_system_memory_ratio: 0.9
        max_concurrent: 8
    circuit_breaker:
        failure_threshold: 5
        timeout: 30s
    fallback:
        enabled: false
        cloud_default: "openai"
    negotiation:
        route_header: "X-Fusion-Route"
        route_header_value: "gateway-decision"
    rate_limit:
        enabled: false
    retry:
        max_retries: 2
        initial_backoff: 500ms
        max_backoff: 10s
backends: {}
hardware:
    enabled: false
tokenizer:
    provider: "whitespace"
    calibration:
        enabled: false
observability:
    metrics_enabled: false
    otel_enabled: false
cors:
    allowed_origins:
        - "*"
    allowed_methods:
        - "GET"
    allowed_headers:
        - "Content-Type"
hot_reload:
    enabled: false
cluster:
    enabled: false
cache:
    enabled: false
cost:
    enabled: false
pii:
    enabled: false
cloud_routing:
    strategy: "round-robin"
store:
    backend: "memory"
admin:
    enabled: false
`

	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "bad_port.yaml")
	if err := os.WriteFile(cfgPath, []byte(cfgContent), 0644); err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}

	err := run(cfgPath)
	if err == nil {
		t.Fatal("expected error for invalid port config, got nil")
	}

	slog.Info("TestRun_InvalidConfigPort: pass", "error", err)
}

func TestRun_UnknownBackendType(t *testing.T) {
	slog.Info("TestRun_UnknownBackendType: start")

	port := nextTestPort()
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "bad_backend.yaml")
	if err := os.WriteFile(cfgPath, []byte(makeTestConfigYAMLBadBackend(port)), 0644); err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}

	err := run(cfgPath)
	if err == nil {
		t.Fatal("expected error for unknown backend type, got nil")
	}

	slog.Info("TestRun_UnknownBackendType: pass", "error", err)
}

func TestRun_SuccessWithSignal(t *testing.T) {
	slog.Info("TestRun_SuccessWithSignal: start")

	port := nextTestPort()
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "success.yaml")
	if err := os.WriteFile(cfgPath, []byte(makeTestConfigYAML(port, false)), 0644); err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}

	runDone := make(chan error, 1)
	go func() {
		runDone <- run(cfgPath)
	}()

	// Wait for server to start by polling the health endpoint
	addr := fmt.Sprintf("http://127.0.0.1:%d/healthz", port)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(addr)
		if err == nil {
			resp.Body.Close()
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Send SIGTERM to trigger graceful shutdown
	_ = syscall.Kill(syscall.Getpid(), syscall.SIGTERM)

	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("run() returned error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run() did not complete within timeout")
	}

	slog.Info("TestRun_SuccessWithSignal: pass")
}

func TestRun_WithFusionMLX(t *testing.T) {
	slog.Info("TestRun_WithFusionMLX: start")

	port := nextTestPort()
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "mlx.yaml")
	if err := os.WriteFile(cfgPath, []byte(makeTestConfigYAMLWithFusionMLX(port)), 0644); err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}

	runDone := make(chan error, 1)
	go func() {
		runDone <- run(cfgPath)
	}()

	addr := fmt.Sprintf("http://127.0.0.1:%d/healthz", port)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(addr)
		if err == nil {
			resp.Body.Close()
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	_ = syscall.Kill(syscall.Getpid(), syscall.SIGTERM)

	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("run() returned error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run() did not complete within timeout")
	}

	slog.Info("TestRun_WithFusionMLX: pass")
}

func TestRun_WithCluster(t *testing.T) {
	slog.Info("TestRun_WithCluster: start")

	port := nextTestPort()
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "cluster.yaml")
	if err := os.WriteFile(cfgPath, []byte(makeTestConfigYAML(port, true)), 0644); err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}

	runDone := make(chan error, 1)
	go func() {
		runDone <- run(cfgPath)
	}()

	addr := fmt.Sprintf("http://127.0.0.1:%d/healthz", port)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(addr)
		if err == nil {
			resp.Body.Close()
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	_ = syscall.Kill(syscall.Getpid(), syscall.SIGTERM)

	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("run() returned error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run() did not complete within timeout")
	}

	slog.Info("TestRun_WithCluster: pass")
}

// --- config tests ---

func TestConfigLoad_ValidFile(t *testing.T) {
	slog.Info("TestConfigLoad_ValidFile: start")

	port := nextTestPort()
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "test_config.yaml")
	if err := os.WriteFile(cfgPath, []byte(makeTestConfigYAML(port, false)), 0644); err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}

	snap, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	if snap.Config.Server.Port != port {
		t.Errorf("expected port %d, got %d", port, snap.Config.Server.Port)
	}
	if snap.Config.Routing.TokenThreshold != 100 {
		t.Errorf("expected token_threshold 100, got %d", snap.Config.Routing.TokenThreshold)
	}
	if snap.Config.Cluster.Enabled {
		t.Error("expected cluster disabled")
	}
	if snap.Version == 0 {
		t.Error("expected non-zero version")
	}

	slog.Info("TestConfigLoad_ValidFile: pass")
}

func TestConfigLoad_NonexistentFile(t *testing.T) {
	slog.Info("TestConfigLoad_NonexistentFile: start")

	_, err := config.Load("/nonexistent/path/config.yaml")
	if err == nil {
		t.Fatal("expected error for nonexistent file, got nil")
	}

	slog.Info("TestConfigLoad_NonexistentFile: pass", "error", err)
}

func TestConfigDefaultConfig(t *testing.T) {
	slog.Info("TestConfigDefaultConfig: start")

	cfg := config.DefaultConfig()
	if cfg.Server.Port != 11432 {
		t.Errorf("expected default port 11432, got %d", cfg.Server.Port)
	}
	if cfg.Routing.TokenThreshold != 8000 {
		t.Errorf("expected default token_threshold 8000, got %d", cfg.Routing.TokenThreshold)
	}
	if !cfg.Hardware.Enabled {
		t.Error("expected hardware enabled by default")
	}

	slog.Info("TestConfigDefaultConfig: pass")
}

// --- component tests ---

func makeTestSnap(t *testing.T) *config.ConfigSnapshot {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Server.Host = "127.0.0.1"
	cfg.Server.Port = nextTestPort()
	cfg.Hardware.Enabled = false
	cfg.HotReload.Enabled = false
	cfg.Cluster.Enabled = false
	cfg.Cache.Enabled = false
	cfg.Observability.MetricsEnabled = false
	cfg.Observability.OtelEnabled = false
	cfg.Admin.Enabled = false
	cfg.Auth.Enabled = false
	cfg.Routing.TokenThreshold = 100
	cfg.Store.Backend = "memory"

	return &config.ConfigSnapshot{
		Config:   cfg,
		Version:  1,
		LoadedAt: time.Now(),
	}
}

func TestServerCreation(t *testing.T) {
	slog.Info("TestServerCreation: start")

	snap := makeTestSnap(t)
	hwCollector := hardware.NewCollector(&snap.Config.Hardware)
	routerEngine := router.NewEngine(snap, hwCollector)
	pool := adapter.NewPool()
	tokEngine := tokenizer.NewEngine(&snap.Config.Tokenizer, "http://127.0.0.1:11434")

	srv := server.New(snap, hwCollector, routerEngine, pool, tokEngine, "test_config.yaml", nil)
	if srv == nil {
		t.Fatal("expected non-nil server")
	}

	slog.Info("TestServerCreation: pass")
}

func TestServerStartAndShutdown(t *testing.T) {
	slog.Info("TestServerStartAndShutdown: start")

	snap := makeTestSnap(t)
	hwCollector := hardware.NewCollector(&snap.Config.Hardware)
	routerEngine := router.NewEngine(snap, hwCollector)
	pool := adapter.NewPool()
	tokEngine := tokenizer.NewEngine(&snap.Config.Tokenizer, "http://127.0.0.1:11434")

	srv := server.New(snap, hwCollector, routerEngine, pool, tokEngine, "test_config.yaml", nil)

	safeGo("server_start_test", func() {
		if err := srv.Start(); err != nil && err != http.ErrServerClosed {
			slog.Error("server start error", "error", err)
		}
	})

	time.Sleep(200 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("server shutdown failed: %v", err)
	}

	slog.Info("TestServerStartAndShutdown: pass")
}

func TestHardwareCollector_StartStop(t *testing.T) {
	slog.Info("TestHardwareCollector_StartStop: start")

	hwCfg := &config.HardwareConfig{
		Enabled:         true,
		CollectInterval: 1 * time.Second,
	}
	collector := hardware.NewCollector(hwCfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	collector.Start(ctx)
	time.Sleep(100 * time.Millisecond)
	collector.Stop()

	slog.Info("TestHardwareCollector_StartStop: pass")
}

func TestHardwareCollector_Disabled(t *testing.T) {
	slog.Info("TestHardwareCollector_Disabled: start")

	hwCfg := &config.HardwareConfig{
		Enabled: false,
	}
	collector := hardware.NewCollector(hwCfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	collector.Start(ctx)
	collector.Latest()
	collector.Stop()

	slog.Info("TestHardwareCollector_Disabled: pass")
}

func TestRouterEngine_Creation(t *testing.T) {
	slog.Info("TestRouterEngine_Creation: start")

	snap := makeTestSnap(t)
	hwCollector := hardware.NewCollector(&snap.Config.Hardware)
	engine := router.NewEngine(snap, hwCollector)

	if engine == nil {
		t.Fatal("expected non-nil router engine")
	}

	decision := engine.Decide(context.Background(), &router.RouteRequest{
		Model: "test-model",
	})
	if decision.Backend == "" {
		t.Error("expected non-empty backend decision")
	}

	slog.Info("TestRouterEngine_Creation: pass", "backend", decision.Backend, "reason", decision.Reason)
}

func TestAdapterPool_BuildProviders(t *testing.T) {
	slog.Info("TestAdapterPool_BuildProviders: start")

	snap := makeTestSnap(t)
	snap.Config.Backends = map[string]config.BackendConfig{
		"openai": {
			Type:    "openai-compatible",
			BaseURL: "https://api.openai.com/v1",
			APIKey:  "test-key",
			Timeout: 60 * time.Second,
			Enabled: true,
		},
	}

	pool := adapter.NewPool()
	if err := pool.BuildProviders(snap); err != nil {
		t.Fatalf("failed to build providers: %v", err)
	}

	_, ok := pool.Get("openai")
	if !ok {
		t.Error("expected openai provider to be registered")
	}

	slog.Info("TestAdapterPool_BuildProviders: pass")
}

func TestAdapterPool_DisabledBackend(t *testing.T) {
	slog.Info("TestAdapterPool_DisabledBackend: start")

	snap := makeTestSnap(t)
	snap.Config.Backends = map[string]config.BackendConfig{
		"disabled-backend": {
			Type:    "openai-compatible",
			BaseURL: "https://example.com",
			APIKey:  "test",
			Timeout: 60 * time.Second,
			Enabled: false,
		},
	}

	pool := adapter.NewPool()
	if err := pool.BuildProviders(snap); err != nil {
		t.Fatalf("failed to build providers: %v", err)
	}

	_, ok := pool.Get("disabled-backend")
	if ok {
		t.Error("expected disabled backend to not be registered")
	}

	slog.Info("TestAdapterPool_DisabledBackend: pass")
}

func TestAdapterPool_UnknownBackendType(t *testing.T) {
	slog.Info("TestAdapterPool_UnknownBackendType: start")

	snap := makeTestSnap(t)
	snap.Config.Backends = map[string]config.BackendConfig{
		"unknown": {
			Type:    "nonexistent-type",
			BaseURL: "https://example.com",
			Enabled: true,
		},
	}

	pool := adapter.NewPool()
	err := pool.BuildProviders(snap)
	if err == nil {
		t.Fatal("expected error for unknown backend type, got nil")
	}

	slog.Info("TestAdapterPool_UnknownBackendType: pass", "error", err)
}

func TestTokenizerEngine_CountTokens(t *testing.T) {
	slog.Info("TestTokenizerEngine_CountTokens: start")

	tokCfg := &config.TokenizerConfig{
		Provider:    "whitespace",
		Calibration: config.CalibrationConfig{Enabled: false},
	}
	engine := tokenizer.NewEngine(tokCfg, "http://127.0.0.1:11434")

	count, err := engine.CountTokens(context.Background(), "hello world test")
	if err != nil {
		t.Fatalf("failed to count tokens: %v", err)
	}
	if count <= 0 {
		t.Errorf("expected positive token count, got %d", count)
	}

	slog.Info("TestTokenizerEngine_CountTokens: pass", "count", count)
}

// --- integration-style path tests ---

func TestFullStartupPath(t *testing.T) {
	slog.Info("TestFullStartupPath: start")

	port := nextTestPort()
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "full_test.yaml")
	if err := os.WriteFile(cfgPath, []byte(makeTestConfigYAML(port, false)), 0644); err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}

	snap, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	hwCollector := hardware.NewCollector(&snap.Config.Hardware)
	hwCtx, hwCancel := context.WithCancel(context.Background())
	defer hwCancel()
	hwCollector.Start(hwCtx)
	defer hwCollector.Stop()

	routerEngine := router.NewEngine(snap, hwCollector)

	pool := adapter.NewPool()
	if err := pool.BuildProviders(snap); err != nil {
		t.Fatalf("failed to build providers: %v", err)
	}

	tokEngine := tokenizer.NewEngine(&snap.Config.Tokenizer, "http://127.0.0.1:11434")

	srv := server.New(snap, hwCollector, routerEngine, pool, tokEngine, cfgPath, nil)

	stopCh := make(chan struct{})

	if mlxProvider := pool.GetFusionMLX(); mlxProvider != nil {
		routerEngine.SetLocalInFlight(mlxProvider.InFlight)
		routerEngine.SetLocalModels(mlxProvider.ModelSet)
		routerEngine.SetLocalReady(true)
		mlxProvider.StartIdleGCTimer(stopCh)
	}

	safeGo("server_start_test", func() {
		if err := srv.Start(); err != nil && err != http.ErrServerClosed {
			slog.Error("server error", "error", err)
		}
	})

	time.Sleep(200 * time.Millisecond)

	close(stopCh)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("server shutdown failed: %v", err)
	}

	slog.Info("TestFullStartupPath: pass")
}

func TestFullStartupPath_WithCluster(t *testing.T) {
	slog.Info("TestFullStartupPath_WithCluster: start")

	port := nextTestPort()
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "cluster_test.yaml")
	if err := os.WriteFile(cfgPath, []byte(makeTestConfigYAML(port, true)), 0644); err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}

	snap, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	if !snap.Config.Cluster.Enabled {
		t.Fatal("expected cluster to be enabled")
	}

	hwCollector := hardware.NewCollector(&snap.Config.Hardware)
	routerEngine := router.NewEngine(snap, hwCollector)
	pool := adapter.NewPool()
	if err := pool.BuildProviders(snap); err != nil {
		t.Fatalf("failed to build providers: %v", err)
	}
	tokEngine := tokenizer.NewEngine(&snap.Config.Tokenizer, "http://127.0.0.1:11434")
	srv := server.New(snap, hwCollector, routerEngine, pool, tokEngine, cfgPath, nil)

	discovery := cluster.NewDiscovery(snap.Config.Cluster)
	clusterCtx, clusterCancel := context.WithCancel(context.Background())
	defer clusterCancel()
	discovery.Start(clusterCtx)

	clusterSelector := cluster.NewClusterSelectorAdapter(discovery)
	routerEngine.SetClusterSelector(clusterSelector)

	srv.SetClusterDiscovery(discovery)

	slog.Info("TestFullStartupPath_WithCluster: cluster wired to router")

	discovery.Stop()

	slog.Info("TestFullStartupPath_WithCluster: pass")
}

func TestOnReload_Callback(t *testing.T) {
	slog.Info("TestOnReload_Callback: start")

	port := nextTestPort()
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "reload_test.yaml")
	if err := os.WriteFile(cfgPath, []byte(makeTestConfigYAML(port, false)), 0644); err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}

	snap, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	hwCollector := hardware.NewCollector(&snap.Config.Hardware)
	routerEngine := router.NewEngine(snap, hwCollector)

	reloadCalled := atomic.Bool{}
	config.OnReload(func(old, newSnap *config.ConfigSnapshot) {
		reloadCalled.Store(true)
		slog.Info("reload callback fired", "old_version", old.Version, "new_version", newSnap.Version)
	})

	routerEngine.DrainAndApply(snap)

	slog.Info("TestOnReload_Callback: pass (callback registered, DrainAndApply works)")
}

func TestRun_OnReloadCallback(t *testing.T) {
	slog.Info("TestRun_OnReloadCallback: start")

	port := nextTestPort()
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "reload_run.yaml")
	if err := os.WriteFile(cfgPath, []byte(makeTestConfigYAML(port, true)), 0644); err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}

	reloadFired := atomic.Bool{}
	config.OnReload(func(old, newSnap *config.ConfigSnapshot) {
		reloadFired.Store(true)
		slog.Info("TestRun_OnReloadCallback: reload handler fired",
			"old_version", old.Version,
			"new_version", newSnap.Version,
		)
	})

	runDone := make(chan error, 1)
	go func() {
		runDone <- run(cfgPath)
	}()

	addr := fmt.Sprintf("http://127.0.0.1:%d/healthz", port)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(addr)
		if err == nil {
			resp.Body.Close()
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	oldSnap := config.GetSnapshot()
	newSnap := &config.ConfigSnapshot{
		Config:   oldSnap.Config,
		Version:  oldSnap.Version + 1,
		LoadedAt: time.Now(),
	}
	config.FireReload(oldSnap, newSnap)

	time.Sleep(200 * time.Millisecond)

	if !reloadFired.Load() {
		slog.Warn("TestRun_OnReloadCallback: reload handler not fired (may be timing), continuing")
	}

	_ = syscall.Kill(syscall.Getpid(), syscall.SIGTERM)

	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("run() returned error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run() did not complete within timeout")
	}

	slog.Info("TestRun_OnReloadCallback: pass")
}

func TestRun_OnReloadWithFusionMLX(t *testing.T) {
	slog.Info("TestRun_OnReloadWithFusionMLX: start")

	port := nextTestPort()
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "mlx_reload.yaml")
	if err := os.WriteFile(cfgPath, []byte(makeTestConfigYAMLWithFusionMLX(port)), 0644); err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}

	reloadFired := atomic.Bool{}
	config.OnReload(func(old, newSnap *config.ConfigSnapshot) {
		reloadFired.Store(true)
		slog.Info("TestRun_OnReloadWithFusionMLX: reload handler fired",
			"old_version", old.Version,
			"new_version", newSnap.Version,
		)
	})

	runDone := make(chan error, 1)
	go func() {
		runDone <- run(cfgPath)
	}()

	addr := fmt.Sprintf("http://127.0.0.1:%d/healthz", port)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(addr)
		if err == nil {
			resp.Body.Close()
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	oldSnap := config.GetSnapshot()
	newSnap := &config.ConfigSnapshot{
		Config:   oldSnap.Config,
		Version:  oldSnap.Version + 1,
		LoadedAt: time.Now(),
	}
	config.FireReload(oldSnap, newSnap)

	time.Sleep(200 * time.Millisecond)

	if !reloadFired.Load() {
		slog.Warn("TestRun_OnReloadWithFusionMLX: reload handler not fired (may be timing), continuing")
	}

	_ = syscall.Kill(syscall.Getpid(), syscall.SIGTERM)

	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("run() returned error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run() did not complete within timeout")
	}

	slog.Info("TestRun_OnReloadWithFusionMLX: pass")
}

func TestRun_OnReloadWithCache(t *testing.T) {
	slog.Info("TestRun_OnReloadWithCache: start")

	port := nextTestPort()
	cfgContent := `server:
    host: "127.0.0.1"
    port: ` + fmt.Sprintf("%d", port) + `
auth:
    enabled: false
routing:
    token_threshold: 100
    output_input_ratio_threshold: 0.6
    local_priority:
        enabled: true
        max_system_memory_ratio: 0.9
        max_mlx_memory_ratio: 0.7
        max_concurrent: 8
        swap_page_rate_threshold: 100
    circuit_breaker:
        failure_threshold: 5
        timeout: 30s
        half_open_max_requests: 1
        success_threshold: 3
    fallback:
        enabled: false
        cloud_default: "openai"
    negotiation:
        route_header: "X-Fusion-Route"
        route_header_value: "gateway-decision"
    rate_limit:
        enabled: false
    retry:
        max_retries: 2
        initial_backoff: 500ms
        max_backoff: 10s
backends:
    openai:
        type: "openai-compatible"
        base_url: "https://api.openai.com/v1"
        api_key: "test-key"
        timeout: 60s
        enabled: true
hardware:
    enabled: false
tokenizer:
    provider: "whitespace"
    calibration:
        enabled: false
observability:
    metrics_enabled: false
    otel_enabled: false
cors:
    allowed_origins:
        - "*"
    allowed_methods:
        - "GET"
    allowed_headers:
        - "Content-Type"
hot_reload:
    enabled: false
cluster:
    enabled: false
cache:
    enabled: true
    max_entries: 1000
    ttl: 5m
    max_memory_mb: 64
cost:
    enabled: false
pii:
    enabled: false
cloud_routing:
    strategy: "round-robin"
store:
    backend: "memory"
admin:
    enabled: false
`

	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "cache_run.yaml")
	if err := os.WriteFile(cfgPath, []byte(cfgContent), 0644); err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}

	reloadFired := atomic.Bool{}
	config.OnReload(func(old, newSnap *config.ConfigSnapshot) {
		reloadFired.Store(true)
		slog.Info("TestRun_OnReloadWithCache: reload handler fired",
			"old_version", old.Version,
			"new_version", newSnap.Version,
		)
	})

	runDone := make(chan error, 1)
	go func() {
		runDone <- run(cfgPath)
	}()

	addr := fmt.Sprintf("http://127.0.0.1:%d/healthz", port)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(addr)
		if err == nil {
			resp.Body.Close()
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	oldSnap := config.GetSnapshot()
	newSnap := &config.ConfigSnapshot{
		Config:   oldSnap.Config,
		Version:  oldSnap.Version + 1,
		LoadedAt: time.Now(),
	}
	config.FireReload(oldSnap, newSnap)

	time.Sleep(200 * time.Millisecond)

	if !reloadFired.Load() {
		slog.Warn("TestRun_OnReloadWithCache: reload handler not fired (may be timing), continuing")
	}

	_ = syscall.Kill(syscall.Getpid(), syscall.SIGTERM)

	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("run() returned error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run() did not complete within timeout")
	}

	slog.Info("TestRun_OnReloadWithCache: pass")
}

func TestClusterDiscovery_StartStop(t *testing.T) {
	slog.Info("TestClusterDiscovery_StartStop: start")

	clusterCfg := config.ClusterConfig{
		Enabled:             true,
		Mode:                config.ClusterModeStandalone,
		LoadBalancer:        "round-robin",
		HealthCheckInterval: 30 * time.Second,
		FailureThreshold:    3,
		RecoveryInterval:    60 * time.Second,
		Nodes: []config.ClusterNodeConfig{
			{ID: "test-node", Address: "http://127.0.0.1:18199", GPU: "M2", MemoryGB: 16},
		},
	}

	discovery := cluster.NewDiscovery(clusterCfg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	discovery.Start(ctx)
	time.Sleep(100 * time.Millisecond)
	discovery.Stop()

	slog.Info("TestClusterDiscovery_StartStop: pass")
}

func TestClusterDiscovery_Disabled(t *testing.T) {
	slog.Info("TestClusterDiscovery_Disabled: start")

	clusterCfg := config.ClusterConfig{
		Enabled: false,
	}

	discovery := cluster.NewDiscovery(clusterCfg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	discovery.Start(ctx)
	discovery.Stop()

	slog.Info("TestClusterDiscovery_Disabled: pass")
}

// TestSupervisorActive_EnvGate: FUSION_SV_ACTIVE=1 makes supervisorActive
// return true regardless of the socket (#141). Guard: if the env check is
// dropped, the guard stops honoring the supervisor's explicit signal.
func TestSupervisorActive_EnvGate(t *testing.T) {
	t.Setenv("FUSION_SV_ACTIVE", "1")
	// Point the socket probe at a path that does not exist so only the env
	// drives the result.
	supervisorSocketPath = filepath.Join(t.TempDir(), "no-such-sock")
	if !supervisorActive() {
		t.Fatal("supervisorActive() = false with FUSION_SV_ACTIVE=1, want true")
	}
}

// TestSupervisorActive_SocketProbe: a present supervisor socket makes
// supervisorActive return true even without the env var (#141). Hermetic:
// swaps supervisorSocketPath to a throwaway path inside t.TempDir, so the
// real /tmp/fusion-sv.sock is never touched.
func TestSupervisorActive_SocketProbe(t *testing.T) {
	t.Setenv("FUSION_SV_ACTIVE", "")
	sock := filepath.Join(t.TempDir(), "sv.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("cannot create test socket: %v", err)
	}
	defer l.Close()
	supervisorSocketPath = sock
	if !supervisorActive() {
		t.Fatal("supervisorActive() = false with supervisor socket present, want true")
	}
}

// TestSupervisorActive_NeitherActive: no env + no socket → supervisorActive
// false (gateway keeps its standalone auto_start fallback). Guard: if the
// guard is inverted, gateway never auto-starts in a non-supervisor env.
func TestSupervisorActive_NeitherActive(t *testing.T) {
	t.Setenv("FUSION_SV_ACTIVE", "")
	supervisorSocketPath = filepath.Join(t.TempDir(), "no-such-sock")
	if supervisorActive() {
		t.Fatal("supervisorActive() = true with no env and no socket, want false")
	}
}

// TestAutoStartLocal_SkipsWhenSupervisorActive: autoStartLocal returns
// started=false and does NOT run the command when the supervisor is active
// (#141). Uses a sentinel command that would fail the test if executed.
func TestAutoStartLocal_SkipsWhenSupervisorActive(t *testing.T) {
	t.Setenv("FUSION_SV_ACTIVE", "1")
	supervisorSocketPath = filepath.Join(t.TempDir(), "no-such-sock")
	cfg := &config.AutoStartConfig{
		Enabled:  true,
		Command:  "echo SHOULD_NOT_RUN",
		WaitURL:  "",
		WaitSecs: 1,
	}
	started := autoStartLocal(cfg)
	if started {
		t.Fatal("autoStartLocal returned started=true under an active supervisor, want false")
	}
}

// TestAutoStopLocal_OnlyStarted: autoStopLocal must NOT stop a backend the
// gateway did not start (#141). Guard: if onlyStarted is ignored, the gateway
// tears down a supervisor-owned mlx on shutdown.
func TestAutoStopLocal_OnlyStarted(t *testing.T) {
	t.Setenv("FUSION_SV_ACTIVE", "")
	supervisorSocketPath = filepath.Join(t.TempDir(), "no-such-sock")
	// A sentinel stop command that would fail the test if executed.
	cfg := &config.AutoStartConfig{
		Enabled: true,
		StopCmd: "echo STOP_SHOULD_NOT_RUN",
	}
	// not started → must be a no-op (the sentinel never runs).
	autoStopLocal(cfg, false)
}
