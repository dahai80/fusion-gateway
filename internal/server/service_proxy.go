package server

// service_proxy.go — config-driven platform-service routes under /svc/<name>/
// (config-conventions §1/§5, TASK-CONFIG-2026-10-28): business apps reach
// platform services (rag / identity / drawing-parser) exclusively through the
// gateway, authenticated with their FUSION_CLIENT_KEY via the standard
// middleware chain. The gateway owns the per-service credentials (upstream
// tokens) and forwards user identity where the service enforces it.

import (
	"log/slog"
	"os"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/fusion-gateway/fusion-gateway/internal/config"
	"github.com/fusion-gateway/fusion-gateway/internal/shell"
)

const userTokenHeader = "X-Fusion-User-Token"

// handleServiceRoute returns the proxy handler for one /svc/<name>/ route.
// stripPrefix is "/svc/<name>" for standard routes (upstream sees its own
// paths) or "" for a Prefix alias mount (#370) where the upstream serves the
// caller's path verbatim.
func (s *Server) handleServiceRoute(name string, route config.ServiceRouteConfig, stripPrefix string) http.HandlerFunc {
	target, err := url.Parse(route.URL)
	if err != nil || target.Scheme == "" || target.Host == "" {
		slog.Error("service route disabled: invalid upstream url", "service", name, "url", route.URL)
		return func(w http.ResponseWriter, r *http.Request) {
			shell.WriteEnvelope(w, http.StatusServiceUnavailable, "service_misconfigured",
			"service route '"+name+"' has an invalid upstream url", name, false)
		}
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	// #370: a dead platform service gets the structured A5 degradation
	// envelope (retryable), never a bare 502 with a raw proxy error.
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		slog.Warn("service proxy upstream error", "service", name, "path", r.URL.Path, "error", err)
		shell.WriteEnvelope(w, http.StatusServiceUnavailable, "upstream_unavailable",
			"platform service '"+name+"' is unreachable; retry shortly", name, true)
	}
	origDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		origDirector(req)
		// Strip the mount prefix — upstreams see their own paths. A Prefix
		// alias mount (#370) strips nothing: the upstream serves the same path
		// surface the caller dialed.
		if stripPrefix != "" {
			req.URL.Path = strings.TrimPrefix(req.URL.Path, stripPrefix)
			if !strings.HasPrefix(req.URL.Path, "/") {
				req.URL.Path = "/" + req.URL.Path
			}
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
		// #370: upstream_token_file — 0600 secret file (tilde-expanded) read
		// once here; the inline upstream_token wins when both are set. A
		// missing/unreadable file skips the route (fail-closed) rather than
		// forwarding without the credential.
		if route.UpstreamAuth == "inject" && route.UpstreamToken == "" && route.UpstreamTokenFile != "" {
			secret, err := os.ReadFile(config.ExpandPath(route.UpstreamTokenFile))
			if err != nil {
				slog.Error("service route skipped: upstream_token_file unreadable", "service", name, "file", route.UpstreamTokenFile, "error", err)
				continue
			}
			route.UpstreamToken = strings.TrimSpace(string(secret))
		}
		if route.UpstreamAuth == "inject" && route.UpstreamToken == "" {
			slog.Warn("service route skipped: upstream_auth=inject without upstream_token", "service", name)
			continue
		}
		// #370: an optional Prefix mounts the service at an alias path
		// (e.g. /v1/memory for fusion-memory) with NO path stripping — the
		// upstream serves the caller's path verbatim. Standard routes keep
		// the /svc/<name>/ mount and strip the prefix.
		mount, strip := "/svc/"+name, "/svc/"+name
		if route.Prefix != "" {
			if !strings.HasPrefix(route.Prefix, "/") {
				slog.Warn("service route skipped: prefix must start with \"/\"", "service", name, "prefix", route.Prefix)
				continue
			}
			mount, strip = route.Prefix, ""
		}
		mux.HandleFunc(mount+"/", s.withMiddleware(s.handleServiceRoute(name, route, strip)))
		slog.Info("service route registered", "service", name, "prefix", mount+"/", "upstream", route.URL)
	}
}
