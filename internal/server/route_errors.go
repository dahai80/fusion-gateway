package server

import (
    "log/slog"
    "time"
)

// routeErrEntry is the #438 /health surface for a backend whose process is
// alive but whose requests are failing (the wedge signature): the most recent
// provider error with its UTC timestamp. Message capped at 512 chars, the
// same budget as writeChatFailedError's response detail. The entry is kept
// after recovery — the timestamp carries the age so a stale error reads as
// history, not a live fault.
type routeErrEntry struct {
    Message string    `json:"message"`
    At      time.Time `json:"at"`
}

// noteRouteError records the most recent per-provider route failure so
// handleHealth can surface it as last_route_error. Keyed by provider name —
// the same keys as the /health backends map. nil-safe on a zero-value Server
// (the map is created lazily); nil errors are ignored so success paths never
// poison the signal.
func (s *Server) noteRouteError(providerName string, err error) {
    if err == nil || providerName == "" {
        return
    }
    msg := err.Error()
    if len(msg) > 512 {
        msg = msg[:512]
    }
    s.routeErrMu.Lock()
    if s.routeErrs == nil {
        s.routeErrs = make(map[string]routeErrEntry)
    }
    s.routeErrs[providerName] = routeErrEntry{Message: msg, At: time.Now().UTC()}
    s.routeErrMu.Unlock()
    slog.Warn("route error recorded for /health surfacing",
        "provider", providerName, "error", msg)
}

// lastRouteError returns the most recent recorded route error for a provider.
func (s *Server) lastRouteError(providerName string) (routeErrEntry, bool) {
    s.routeErrMu.Lock()
    defer s.routeErrMu.Unlock()
    e, ok := s.routeErrs[providerName]
    return e, ok
}
