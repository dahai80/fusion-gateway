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
		if err := pool.BuildProviders(newSnap); err != nil {
			slog.Error("failed to rebuild providers after reload", "error", err)
		}
		if mlxProvider := pool.GetFusionMLX(); mlxProvider != nil {
			routerEngine.SetLocalInFlight(mlxProvider.InFlight)
			routerEngine.SetLocalModels(mlxProvider.ModelSet)
			routerEngine.SetLocalReady(true)
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
