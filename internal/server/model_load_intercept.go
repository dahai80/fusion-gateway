package server

import (
    "encoding/json"
    "log/slog"
    "net/http"
    "strings"
)

func (s *Server) handleModelLoadUnload(w http.ResponseWriter, r *http.Request) {
    path := r.URL.Path
    modelID, action := parseModelLoadPath(path)
    if modelID == "" || (action != "load" && action != "unload") {
        http.NotFound(w, r)
        return
    }

    slog.Info("model load/unload intercepted, redirecting to model-hub",
        "model", modelID, "action", action, "method", r.Method, "remote", r.RemoteAddr)

    hub := s.pool.GetModelHub()
    if hub == nil {
        slog.Warn("model load/unload intercepted but model-hub not configured",
            "model", modelID, "action", action)
        w.Header().Set("Content-Type", "application/json")
        w.WriteHeader(http.StatusServiceUnavailable)
        if err := json.NewEncoder(w).Encode(map[string]interface{}{
            "error": map[string]string{
                "message": "model-hub backend not configured, cannot serve model load/unload request",
                "type":    "upstream_error",
                "code":    "model_hub_unavailable",
            },
        }); err != nil {
            slog.Error("failed to encode model-hub unavailable response", "error", err)
        }
        return
    }

    r.URL.Path = "/api/v1/models/" + modelID + "/serve"
    r.Method = http.MethodPost
    slog.Info("forwarding model serve request to model-hub",
        "model", modelID, "original_action", action, "target_path", r.URL.Path)

    hub.ReverseProxy().ServeHTTP(w, r)
}

func parseModelLoadPath(path string) (modelID, action string) {
    if !strings.HasPrefix(path, "/v1/models/") {
        return "", ""
    }
    rest := strings.TrimPrefix(path, "/v1/models/")
    parts := strings.SplitN(rest, "/", 2)
    if len(parts) < 2 {
        return parts[0], ""
    }
    return parts[0], parts[1]
}
