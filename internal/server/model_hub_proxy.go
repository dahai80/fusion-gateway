package server

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/fusion-gateway/fusion-gateway/internal/middleware"
)

func (s *Server) setupModelHubRoutes(mux *http.ServeMux) {
	hub := s.pool.GetModelHub()
	if hub == nil {
		slog.Info("model-hub backend not configured, skipping proxy routes")
		return
	}

	slog.Info("registering model-hub proxy routes", "base_url", hub.BaseURL())

	mux.HandleFunc("/api/v1/", s.withMiddleware(s.handleModelHubProxy))
}

func (s *Server) handleModelHubProxy(w http.ResponseWriter, r *http.Request) {
	hub := s.pool.GetModelHub()
	if hub == nil {
		slog.Error("model-hub proxy: backend not available")
		http.Error(w, "model-hub backend not configured", http.StatusServiceUnavailable)
		return
	}

	// Enforce model_module permission if principal has restrictions
	principal := middleware.PrincipalFromContext(r.Context())
	if principal != nil && !principal.IsMaster && len(principal.ModelModules) > 0 {
		module := r.Header.Get("X-Fusion-Module")
		if module == "" {
			module = inferModuleFromPath(r.URL.Path)
		}
		if module != "" && !middleware.CheckModelModuleAccess(r, module) {
			slog.Warn("model-hub proxy: module access denied",
				"key", principal.AuthMethod, "module", module, "allowed", principal.ModelModules)
			http.Error(w, "module access denied: "+module, http.StatusForbidden)
			return
		}
		if r.Header.Get("X-Fusion-Module") == "" && module != "" {
			r.Header.Set("X-Fusion-Module", module)
		}
	}

	slog.Debug("model-hub proxy forwarding", "method", r.Method, "path", r.URL.Path)
	hub.ReverseProxy().ServeHTTP(w, r)
}

func inferModuleFromPath(path string) string {
	parts := strings.Split(strings.TrimPrefix(path, "/api/v1/"), "/")
	if len(parts) == 0 {
		return ""
	}
	segment := parts[0]
	if middleware.ValidModule(segment) {
		return segment
	}
	if segment == "inference" || segment == "chat" {
		return "chat"
	}
	return ""
}

// handleMLXAdminProxy forwards /admin/api/fine-tune/* to the fusion-mlx backend
// (#30), transparently proxying method/path/query/body and SSE streams. The
// fusion-mlx provider's ReverseProxy injects Authorization + X-Fusion-Route so
// fusion-mlx's route_guard admits the request; clients authenticate to the
// gateway with their fg-key (same chain as /v1/*) and send nothing extra.
func (s *Server) handleMLXAdminProxy(w http.ResponseWriter, r *http.Request) {
	mlx := s.pool.GetFusionMLX()
	if mlx == nil {
		slog.Error("fusion-mlx admin proxy: backend not configured", "path", r.URL.Path)
		http.Error(w, "fusion-mlx backend not configured", http.StatusServiceUnavailable)
		return
	}
	slog.Debug("fusion-mlx admin proxy forwarding", "method", r.Method, "path", r.URL.Path)
	mlx.ReverseProxy().ServeHTTP(w, r)
}
