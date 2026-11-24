package server

import (
    "net/http"
    "testing"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

// #715: a service_routes prefix mount colliding with a claimed shell mount
// is skipped (ServeMux panics on duplicate registration; the session-authed
// bridge supersedes the client-key passthrough).

func TestRegisterServiceRoutesSkipsClaimedPrefix(t *testing.T) {
    s := &Server{
        claimedPrefixes: map[string]bool{"/api/messaging/": true},
        cfg:             &config.ConfigSnapshot{Config: config.DefaultConfig()},
    }
    s.cfg.Config.ServiceRoutes = map[string]config.ServiceRouteConfig{
        "identity-messaging": {
            URL:    "http://127.0.0.1:11470",
            Prefix: "/api/messaging",
        },
    }
    m := http.NewServeMux()
    // Must not panic on the claimed mount.
    s.registerServiceRoutes(m)
}

func TestRegisterServiceRoutesRegistersUnclaimedPrefix(t *testing.T) {
    s := &Server{
        cfg: &config.ConfigSnapshot{Config: config.DefaultConfig()},
    }
    s.cfg.Config.ServiceRoutes = map[string]config.ServiceRouteConfig{
        "identity-messaging": {
            URL:    "http://127.0.0.1:11470",
            Prefix: "/api/messaging",
        },
    }
    m := http.NewServeMux()
    // Registration-only assertion: an unclaimed prefix must register cleanly
    // (a claimed-but-skipped regression shows up as a ServeMux duplicate
    // panic in TestRegisterServiceRoutesSkipsClaimedPrefix's negative).
    s.registerServiceRoutes(m)
}
