package server

import (
    "encoding/json"
    "log/slog"
    "net/http"
)

// #449: graceful-drain blind spot. During the shutdown drain (up to
// server.graceful_shutdown_timeout, 10m by default) the process stays alive
// and briefly keeps answering, so every liveness prober — start.sh get_pid
// (kill -0), operator curls, supervisor probes — saw a healthy gateway that
// was actually seconds away from releasing the port. SetDraining flips the
// flag from the SIGTERM/SIGINT handler in main.go, before srv.Shutdown; from
// that moment /healthz and /readyz answer 503 {"status":"draining"} so
// callers can tell "serving" from "draining". /livez deliberately stays 200:
// the process IS alive.

// SetDraining marks the server as shutting down. Idempotent.
func (s *Server) SetDraining() {
    if s.draining.Swap(true) {
        return
    }
    slog.Warn("gateway draining: /healthz and /readyz now answer 503 until shutdown completes")
}

// IsDraining reports whether shutdown has begun.
func (s *Server) IsDraining() bool {
    return s.draining.Load()
}

// writeDraining is the shared 503 body for the liveness/readiness probes.
func writeDraining(w http.ResponseWriter) {
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(http.StatusServiceUnavailable)
    _ = json.NewEncoder(w).Encode(map[string]interface{}{
        "status": "draining",
    })
}
