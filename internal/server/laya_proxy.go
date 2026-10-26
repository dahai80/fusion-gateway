package server

import (
    "crypto/subtle"
    "log/slog"
    "net/http"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/httpx"
    "github.com/fusion-gateway/fusion-gateway/internal/middleware"
)

// handleLayaProxy forwards /v1/laya/* to the fusion-mlx backend verbatim
// (#327), so the gateway is a complete entry point for laya-mlx surface routes
// (GET /v1/laya/status, POST /v1/laya/decide). The LayaClassifier (#191)
// already calls fusion-mlx internally; this pass-through serves external
// clients — fusion-bench gateway-default mode previously 404'd and auto-skipped
// laya dims 1-4. Same transparent proxy + credential injection as
// handleMLXStats: clients authenticate to the gateway with their fg-key and
// send nothing extra.
func (s *Server) handleLayaProxy(w http.ResponseWriter, r *http.Request) {
    mlx := s.pool.GetFusionMLX()
    if mlx == nil {
        slog.Error("fusion-mlx laya proxy: backend not configured", "path", r.URL.Path)
        http.Error(w, "fusion-mlx backend not configured", http.StatusServiceUnavailable)
        return
    }
    slog.Debug("fusion-mlx laya proxy forwarding", "method", r.Method, "path", r.URL.Path)
    // E5 (audit): cap the proxied body — /v1/laya/decide POSTs a prompt;
    // defense in depth so a rogue client cannot OOM fusion-mlx.
    r = s.wrapProxyBody(w, r)
    mlx.ReverseProxy().ServeHTTP(w, r)
}

// backendKeyMatcher builds the #327 opt-in credential matcher: when
// auth.accept_backend_keys is enabled, a Bearer credential equal to a local
// backend's api_key (e.g. fusion-mlx's fg-admin-key) authenticates as a
// full-access key named "backend:<name>". Only LOCAL backends qualify — cloud
// provider keys are third-party secrets and must never double as gateway
// credentials. Snapshot at build time; buildMiddlewareChain is rebuilt on
// config reload so backend key changes take effect without a restart.
func (s *Server) backendKeyMatcher() middleware.BackendKeyMatcher {
    auth := &s.cfg.Config.Auth
    if !auth.AcceptBackendKeys {
        return nil
    }
    type backendKey struct {
        name string
        key  string
    }
    var keys []backendKey
    for name, bc := range s.cfg.Config.Backends {
        if bc.APIKey == "" || !bc.Enabled {
            continue
        }
        if !httpx.IsLocalBackendConfig(bc) {
            continue
        }
        keys = append(keys, backendKey{name: name, key: bc.APIKey})
    }
    if len(keys) == 0 {
        slog.Warn("auth.accept_backend_keys enabled but no local backend carries an api_key", "backends", len(s.cfg.Config.Backends))
        return nil
    }
    slog.Info("auth.accept_backend_keys enabled", "local_backends_with_keys", len(keys))
    return func(key string) *config.AuthKeyConfig {
        for _, bk := range keys {
            if subtle.ConstantTimeCompare([]byte(bk.key), []byte(key)) == 1 {
                return &config.AuthKeyConfig{
                    Key:  key,
                    Name: "backend:" + bk.name,
                }
            }
        }
        return nil
    }
}
