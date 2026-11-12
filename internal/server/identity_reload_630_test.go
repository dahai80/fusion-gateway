package server

// #630: the reload path closed the old identity gRPC client INLINE and built
// the new chain BEFORE swapping the client — so the fresh chain captured the
// old (about-to-close) client and in-flight requests held a closing channel
// ("grpc: the client connection is closing" on every post-reload request).
// Fix under test: swap client FIRST, rebuild chain, close the old channel
// only after a grace window.

import (
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/identity"
)



func identityCfg630() config.IdentityConfig {
    return config.IdentityConfig{
        Enabled:          true,
        Endpoint:         "127.0.0.1:1",
        DeadlineMS:       100,
        KeepAliveSec:     30,
        BreakerThreshold: 3,
        BreakerOpenSec:   10,
    }
}

func Test630_ReloadSwapsIdentityClient_OldChannelSurvivesGrace(t *testing.T) {
    s := newTestServer()
    s.cfg.Config.Identity = identityCfg630()
    s.identityClient = identity.NewClient(
        s.cfg.Config.Identity.Endpoint,
        s.cfg.Config.Identity.DeadlineMS,
        s.cfg.Config.Identity.KeepAliveSec,
        s.cfg.Config.Identity.BreakerThreshold,
        s.cfg.Config.Identity.BreakerOpenSec,
        s.cfg.Config.Identity.FallbackToLocal,
        s.cfg.Config.Identity.ServiceTokenFile,
    )
    if s.identityClient == nil {
        t.Fatal("identity client not built for enabled config")
    }
    old := s.identityClient

    identityReloadGraceCloseDelay.Store(int64(30 * time.Millisecond))
    t.Cleanup(func() { identityReloadGraceCloseDelay.Store(int64(60 * time.Second)) })

    // Prod wiring: main.go registers RebuildMiddlewareChain as an OnReload
    // handler; newTestServer does not, so register it here (faithful repro).
    config.OnReload(func(old, new *config.ConfigSnapshot) {
        s.RebuildMiddlewareChain(new)
    })

    reload623(t, s, "")

    if s.identityClient == old {
        t.Fatal("identity client not swapped on reload")
    }
    if old.IsClosed() {
        t.Fatal("old client channel closed inline — in-flight requests would fail with 'client connection is closing'")
    }
    deadline := time.Now().Add(2 * time.Second)
    for time.Now().Before(deadline) && !old.IsClosed() {
        time.Sleep(10 * time.Millisecond)
    }
    if !old.IsClosed() {
        t.Fatal("old client not closed after grace window (channel leak)")
    }
}
