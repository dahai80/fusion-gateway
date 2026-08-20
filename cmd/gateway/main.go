package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/fusion-gateway/fusion-gateway/internal/adapter"
	"github.com/fusion-gateway/fusion-gateway/internal/cluster"
	"github.com/fusion-gateway/fusion-gateway/internal/config"
	"github.com/fusion-gateway/fusion-gateway/internal/hardware"
	"github.com/fusion-gateway/fusion-gateway/internal/observability"
	"github.com/fusion-gateway/fusion-gateway/internal/router"
	"github.com/fusion-gateway/fusion-gateway/internal/server"
	"github.com/fusion-gateway/fusion-gateway/internal/tokenizer"
)

// safeGo launches a goroutine with panic recovery to prevent process crash
func safeGo(name string, fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("goroutine panic recovered",
					"goroutine", name,
					"panic", r,
					"stack", string(debug.Stack()),
				)
			}
		}()
		fn()
	}()
}

// autoStartLocal launches the local inference backend and waits for it to become healthy.
func autoStartLocal(cfg *config.AutoStartConfig) {
	if cfg == nil || !cfg.Enabled || cfg.Command == "" {
		slog.Info("auto_start disabled or not configured")
		return
	}
	slog.Info("auto_start: launching local backend", "command", cfg.Command)
	shell := exec.Command("sh", "-c", cfg.Command)
	shell.Stdout = os.Stdout
	shell.Stderr = os.Stderr
	if err := shell.Start(); err != nil {
		slog.Error("auto_start: failed to launch command", "error", err)
		return
	}
	slog.Info("auto_start: process started", "pid", shell.Process.Pid)

	if cfg.WaitURL == "" {
		return
	}
	waitSecs := cfg.WaitSecs
	if waitSecs <= 0 {
		waitSecs = 120
	}
	deadline := time.Now().Add(time.Duration(waitSecs) * time.Second)
	client := &http.Client{Timeout: 2 * time.Second}
	for time.Now().Before(deadline) {
		resp, err := client.Get(cfg.WaitURL)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode < 500 {
				slog.Info("auto_start: local backend is healthy", "url", cfg.WaitURL)
				return
			}
		}
		slog.Info("auto_start: waiting for local backend", "url", cfg.WaitURL)
		time.Sleep(2 * time.Second)
	}
	slog.Warn("auto_start: timed out waiting for local backend", "url", cfg.WaitURL, "wait_secs", waitSecs)
}

// autoStopLocal stops the local inference backend on shutdown.
func autoStopLocal(cfg *config.AutoStartConfig) {
	if cfg == nil || !cfg.Enabled || cfg.StopCmd == "" {
		return
	}
	slog.Info("auto_start: stopping local backend", "command", cfg.StopCmd)
	shell := exec.Command("sh", "-c", cfg.StopCmd)
	shell.Stdout = os.Stdout
	shell.Stderr = os.Stderr
	if err := shell.Run(); err != nil {
		slog.Error("auto_start: failed to stop local backend", "error", err)
	}
}

// wireIntentClassifier wires the D4 fusion-router-light intent classifier
// (issue #22) into the router engine when enabled. When disabled or when the
// config is incomplete (no base model), it installs NoopClassifier so the
// semantic layer is a no-op and the P0-P7 rule chain decides routing unchanged.
func wireIntentClassifier(e *router.Engine, cfg config.IntentClassifierConfig) {
	if !cfg.Enabled {
		e.SetIntentClassifier(router.NoopClassifier{})
		slog.Info("intent classifier disabled, using noop")
		return
	}
	c := router.NewRouterLightClassifier(cfg)
	if c == nil {
		e.SetIntentClassifier(router.NoopClassifier{})
		slog.Warn("intent classifier enabled but misconfigured, using noop")
		return
	}
	e.SetIntentClassifier(c)
	slog.Info("intent classifier wired",
		"endpoint", cfg.Endpoint,
		"base_model", cfg.BaseModel,
		"adapter", cfg.Adapter,
		"min_confidence", cfg.MinConfidence,
	)
}

// wireHeuristicClassifier wires the in-process sub-ms heuristic intent
// classifier into the router engine when enabled (latency lever for <20ms
// gateway end-to-end overhead, replaces the sync LLM classifier on the code
// path). When disabled it installs nil so the heuristic layer is a no-op and
// routing falls through to the LLM classifier (if enabled) then the rule chain.
func wireHeuristicClassifier(e *router.Engine, cfg config.HeuristicClassifierConfig) {
	if !cfg.Enabled {
		e.SetHeuristicClassifier(nil)
		slog.Info("heuristic classifier disabled, falling through to classifier/rule chain")
		return
	}
	c := router.NewHeuristicClassifier(cfg)
	e.SetHeuristicClassifier(c)
	slog.Info("heuristic classifier wired",
		"code_adapter", cfg.CodeAdapter,
		"min_confidence", cfg.MinConfidence,
		"cache_size", cfg.CacheSize,
	)
}

// fusionMLXBackendCfg returns the BackendConfig for the fusion-mlx backend from
// a snapshot, or a zero value if none is configured/enabled. Used to build the
// LoRA AdapterIndex (Stream D) from the same base_url/api_key/socket_path the
// inference provider uses, so the index fetch rides the same transport.
func fusionMLXBackendCfg(snap *config.ConfigSnapshot) config.BackendConfig {
	if snap == nil {
		return config.BackendConfig{}
	}
	for _, bc := range snap.Config.Backends {
		if bc.Type == "fusion-mlx" && bc.Enabled {
			return bc
		}
	}
	return config.BackendConfig{}
}

// adapterIndexRefresherShim adapts *adapter.AdapterIndex (Refresh(ctx)) to the
// server.adapterIndexRefresher interface (Refresh() error) used by the inbound
// model-hub webhook receiver. The webhook triggers a refresh after responding
// 200, so no request context is propagated; a background context is fine (the
// index's own httpClient Timeout bounds the call).
type adapterIndexRefresherShim struct {
	idx *adapter.AdapterIndex
}

func (s adapterIndexRefresherShim) Refresh() error {
	return s.idx.Refresh(context.Background())
}

func main() {
	configPath := flag.String("config", "config.yaml", "path to config file")
	flag.Parse()

	if err := run(*configPath); err != nil {
		slog.Error("fatal error", "error", err)
		os.Exit(1)
	}
}

func run(configPath string) error {
	slog.Info("loading config", "path", configPath)

	snap, err := config.Load(configPath)
	if err != nil {
		return err
	}

	slog.Info("config loaded",
		"port", snap.Config.Server.Port,
		"token_threshold", snap.Config.Routing.TokenThreshold,
		"cluster_enabled", snap.Config.Cluster.Enabled,
		"version", snap.Version,
	)

	// Auto-start local inference backend (fusion-mlx) if configured
	as := snap.Config.Server.AutoStart
	if as != nil {
		slog.Info("auto_start config loaded", "enabled", as.Enabled, "command", as.Command, "wait_url", as.WaitURL)
	} else {
		slog.Warn("auto_start config is nil — not configured")
	}
	autoStartLocal(as)

	hwCollector := hardware.NewCollector(&snap.Config.Hardware)
	hwCtx, hwCancel := context.WithCancel(context.Background())
	defer hwCancel()
	hwCollector.Start(hwCtx)
	defer hwCollector.Stop()

	routerEngine := router.NewEngine(snap, hwCollector)

	// D4 semantic intent layer (issue #22): wire the real fusion-router-light
	// classifier when intent_classifier is enabled. Falls back to NoopClassifier
	// (set by NewEngine) when disabled or misconfigured.
	wireIntentClassifier(routerEngine, snap.Config.Routing.IntentClassifier)
	// Heuristic classifier (<20ms latency lever): runs before the LLM
	// classifier on every request; recognizes coding intent and dispatches to
	// LocalBackend + LoRA hot-swap. No-op when disabled (nil install).
	wireHeuristicClassifier(routerEngine, snap.Config.Routing.HeuristicClassifier)

	pool := adapter.NewPool()
	if err := pool.BuildProviders(snap); err != nil {
		return err
	}

	tokEngine := tokenizer.NewEngine(&snap.Config.Tokenizer, "http://127.0.0.1:11434")

	srv := server.New(snap, hwCollector, routerEngine, pool, tokEngine, configPath)

	stopCh := make(chan struct{})

	// Wire local provider to router
	if mlxProvider := pool.GetFusionMLX(); mlxProvider != nil {
		routerEngine.SetLocalInFlight(mlxProvider.InFlight)
		routerEngine.SetLocalModels(mlxProvider.ModelSet)
		routerEngine.SetLocalReady(true)
		slog.Info("wired local provider to router engine")

		modelCtx, modelCancel := context.WithCancel(context.Background())
		defer modelCancel()
		// M2 fix: use safeGo for panic recovery on background goroutines
		safeGo("refresh_model_set", func() {
			mlxProvider.RefreshModelSet(modelCtx)
			ticker := time.NewTicker(60 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					mlxProvider.RefreshModelSet(modelCtx)
				case <-modelCtx.Done():
					return
				}
			}
		})

		mlxProvider.StartIdleGCTimer(stopCh)
	}

	// Stream D: LoRA adapter index. Build from the fusion-mlx backend config,
	// wire it into the router for best-effort code_adapter validation, and
	// refresh on a 60s cadence (mirrors the refresh_model_set safeGo pattern).
	// Only construct when a fusion-mlx backend is configured; absent or disabled
	// means the heuristic code path runs without adapter validation (log-only).
	// Declared in run() scope so OnReload can rebuild it when the backend config
	// changes (the index pins base_url/api_key at construction).
	var adapterIndex *adapter.AdapterIndex
	if mlxBackendCfg := fusionMLXBackendCfg(snap); mlxBackendCfg.BaseURL != "" {
		adapterIndex = adapter.NewAdapterIndex(mlxBackendCfg)
		routerEngine.SetAdapterLookup(adapterIndex)
		// Wire the index as the webhook receiver's refresh callback so an
		// inbound adapter.* event from fusion-model-hub triggers an immediate
		// refresh (no request context is propagated — the webhook handler has
		// already returned 200 by the time this runs on the hot path; use a
		// background context with the same timeout as the index's own refresh).
		srv.SetAdapterIndexRefresher(adapterIndexRefresherShim{idx: adapterIndex})
		slog.Info("wired adapter index to router engine", "base_url", mlxBackendCfg.BaseURL)

		indexCtx, indexCancel := context.WithCancel(context.Background())
		defer indexCancel()
		safeGo("lora_index_refresh", func() {
			// Initial fetch so validation has data before the first ticker tick.
			if err := adapterIndex.Refresh(indexCtx); err != nil {
				slog.Warn("adapter index initial refresh failed", "error", err)
			}
			ticker := time.NewTicker(60 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					if err := adapterIndex.Refresh(indexCtx); err != nil {
						slog.Warn("adapter index refresh failed", "error", err)
					}
				case <-indexCtx.Done():
					return
				}
			}
		})
	} else {
		slog.Info("fusion-mlx backend not configured, adapter index disabled")
	}

	// Wire cluster discovery to router
	var discovery *cluster.Discovery
	if snap.Config.Cluster.Enabled {
		discovery = cluster.NewDiscovery(snap.Config.Cluster)
		clusterCtx, clusterCancel := context.WithCancel(context.Background())
		defer clusterCancel()
		discovery.Start(clusterCtx)

		clusterSelector := cluster.NewClusterSelectorAdapter(discovery)
		routerEngine.SetClusterSelector(clusterSelector)
		slog.Info("wired cluster discovery to router engine",
			"node_count", len(snap.Config.Cluster.Nodes),
			"load_balancer", snap.Config.Cluster.LoadBalancer,
		)

		srv.SetClusterDiscovery(discovery)
	}

	config.OnReload(func(old, newSnap *config.ConfigSnapshot) {
		routerEngine.DrainAndApply(newSnap)
		observability.UpdateConfigVersion(newSnap.Version)
		// D4: re-wire intent classifier on hot reload so enabling/disabling the
		// semantic layer takes effect without a restart (issue #22).
		wireIntentClassifier(routerEngine, newSnap.Config.Routing.IntentClassifier)
		// Re-wire the heuristic classifier too so the <20ms code path can be
		// toggled via config without a restart.
		wireHeuristicClassifier(routerEngine, newSnap.Config.Routing.HeuristicClassifier)
		if err := pool.BuildProviders(newSnap); err != nil {
			slog.Error("failed to rebuild providers after reload", "error", err)
		}
		if mlxProvider := pool.GetFusionMLX(); mlxProvider != nil {
			routerEngine.SetLocalInFlight(mlxProvider.InFlight)
			routerEngine.SetLocalModels(mlxProvider.ModelSet)
			routerEngine.SetLocalReady(true)
		}
		// Stream D: rebuild the adapter index if the fusion-mlx backend config
		// changed (base_url/api_key/socket_path are pinned at construction), and
		// trigger an immediate refresh so newly published LoRA adapters are
		// picked up without waiting for the next 60s tick.
		if newCfg := fusionMLXBackendCfg(newSnap); newCfg.BaseURL != "" {
			if adapterIndex == nil {
				adapterIndex = adapter.NewAdapterIndex(newCfg)
				routerEngine.SetAdapterLookup(adapterIndex)
				slog.Info("adapter index wired on reload", "base_url", newCfg.BaseURL)
			}
			if err := adapterIndex.Refresh(context.Background()); err != nil {
				slog.Warn("adapter index refresh on reload failed", "error", err)
			}
		} else if adapterIndex != nil {
			// fusion-mlx backend removed on reload: drop the lookup.
			routerEngine.SetAdapterLookup(nil)
			adapterIndex = nil
			slog.Info("adapter index disabled on reload (fusion-mlx backend removed)")
		}
		if discovery != nil && newSnap.Config.Cluster.Enabled {
			discovery.UpdateConfig(newSnap.Config.Cluster)
		}
		// 硬伤2 fix: update cache config on reload
		if srv.Cache() != nil {
			srv.Cache().UpdateConfig(newSnap.Config.Cache)
		}
		// M1 fix: rebuild middleware chain on reload
		srv.RebuildMiddlewareChain(newSnap)
	})

	safeGo("config_watch_reload", func() { config.WatchAndReload(configPath) })

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	// M2 fix: use safeGo for server goroutine
	safeGo("server_start", func() {
		if err := srv.Start(); err != nil {
			slog.Error("server error", "error", err)
			quit <- syscall.SIGTERM
		}
	})

	sig := <-quit
	slog.Info("shutting down", "signal", sig.String())
	close(stopCh)

	// Auto-stop local inference backend if it was auto-started
	autoStopLocal(snap.Config.Server.AutoStart)

	if discovery != nil {
		discovery.Stop()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		return err
	}

	slog.Info("server stopped")
	return nil
}
