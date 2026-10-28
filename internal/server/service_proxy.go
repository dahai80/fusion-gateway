package server

// service_proxy.go — config-driven platform-service routes under /svc/<name>/
// (config-conventions §1/§5, TASK-CONFIG-2026-10-28): business apps reach
// platform services (rag / identity / drawing-parser) exclusively through the
// gateway, authenticated with their FUSION_CLIENT_KEY via the standard
// middleware chain. The gateway owns the per-service credentials (upstream
// tokens) and forwards user identity where the service enforces it.

import (
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/fusion-gateway/fusion-gateway/internal/config"
)

const userTokenHeader = "X-Fusion-User-Token"

// handleServiceRoute returns the proxy handler for one /svc/<name>/ route.
func (s *Server) handleServiceRoute(name string, route config.ServiceRouteConfig) http.HandlerFunc {
	target, err := url.Parse(route.URL)
	if err != nil || target.Scheme == "" || target.Host == "" {
		slog.Error("service route disabled: invalid upstream url", "service", name, "url", route.URL)
		return func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "service route misconfigured", http.StatusServiceUnavailable)
		}
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	origDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		origDirector(req)
		// Strip the /svc/<name> prefix — upstreams see their own paths.
		req.URL.Path = strings.TrimPrefix(req.URL.Path, "/svc/"+name)
		if !strings.HasPrefix(req.URL.Path, "/") {
			req.URL.Path = "/" + req.URL.Path
		}
		req.Host = target.Host
		switch route.UpstreamAuth {
		case "user":
			// Caller identity wins when present (user JWT passthrough);
			// otherwise the incoming Authorization (client key) is kept.
			if tok := req.Header.Get(userTokenHeader); tok != "" {
				req.Header.Set("Authorization", "Bearer "+tok)
			}
		case "inject":
			// Gateway-held service credential (e.g. identity service token).
			if route.UpstreamToken != "" {
				req.Header.Set("Authorization", "Bearer "+route.UpstreamToken)
			}
		default:
			// passthrough — keep incoming Authorization as-is.
		}
		// Internal routing header never reaches platform services.
		req.Header.Del(userTokenHeader)
	}
	return func(w http.ResponseWriter, r *http.Request) {
		r = s.wrapProxyBody(w, r)
		slog.Debug("service proxy forwarding", "service", name, "path", r.URL.Path, "method", r.Method)
		proxy.ServeHTTP(w, r)
	}
}

// registerServiceRoutes mounts /svc/<name>/ for every configured route.
// Auth: the standard middleware chain (client keys / admin keys), mirroring
// the /v1/* LLM surface. Unconfigured names 404 via ServeMux.
func (s *Server) registerServiceRoutes(mux *http.ServeMux) {
	for name, route := range s.cfg.Config.ServiceRoutes {
		if route.URL == "" {
			slog.Warn("service route skipped: empty url", "service", name)
			continue
		}
		if route.UpstreamAuth == "inject" && route.UpstreamToken == "" {
			slog.Warn("service route skipped: upstream_auth=inject without upstream_token", "service", name)
			continue
		}
		mux.HandleFunc("/svc/"+name+"/", s.withMiddleware(s.handleServiceRoute(name, route)))
		slog.Info("service route registered", "service", name, "prefix", "/svc/"+name+"/", "upstream", route.URL)
	}
}
