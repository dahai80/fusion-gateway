package main

import (
    "context"
    "flag"
    "log/slog"
    "os"
    "os/signal"
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

func main() {
    configPath := flag.String("config", "config.yaml", "path to config file")
    flag.Parse()

    slog.Info("loading config", "path", *configPath)

    snap, err := config.Load(*configPath)
    if err != nil {
        slog.Error("failed to load config", "error", err)
        os.Exit(1)
    }

    slog.Info("config loaded",
        "port", snap.Config.Server.Port,
        "token_threshold", snap.Config.Routing.TokenThreshold,
        "cluster_enabled", snap.Config.Cluster.Enabled,
        "version", snap.Version,
    )

    hwCollector := hardware.NewCollector(&snap.Config.Hardware)
    hwCtx, hwCancel := context.WithCancel(context.Background())
    defer hwCancel()
    hwCollector.Start(hwCtx)
    defer hwCollector.Stop()

    routerEngine := router.NewEngine(snap, hwCollector)

    pool := adapter.NewPool()
    if err := pool.BuildProviders(snap); err != nil {
        slog.Error("failed to build providers", "error", err)
        os.Exit(1)
    }

    tokEngine := tokenizer.NewEngine(&snap.Config.Tokenizer, "http://127.0.0.1:11434")

    srv := server.New(snap, hwCollector, routerEngine, pool, tokEngine)

    stopCh := make(chan struct{})

    // Wire local provider to router
    if mlxProvider := pool.GetFusionMLX(); mlxProvider != nil {
        routerEngine.SetLocalInFlight(mlxProvider.InFlight)
        routerEngine.SetLocalModels(mlxProvider.ModelSet)
        routerEngine.SetLocalReady(true)
        slog.Info("wired local provider to router engine")

        modelCtx, modelCancel := context.WithCancel(context.Background())
        defer modelCancel()
        go func() {
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
        }()

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
    })

    go config.WatchAndReload(*configPath)

    quit := make(chan os.Signal, 1)
    signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

    go func() {
        if err := srv.Start(); err != nil {
            slog.Error("server error", "error", err)
            quit <- syscall.SIGTERM
        }
    }()

    sig := <-quit
    slog.Info("shutting down", "signal", sig.String())
    close(stopCh)

    if discovery != nil {
        discovery.Stop()
    }

    ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
    defer cancel()

    if err := srv.Shutdown(ctx); err != nil {
        slog.Error("shutdown error", "error", err)
        os.Exit(1)
    }

    slog.Info("server stopped")
}
