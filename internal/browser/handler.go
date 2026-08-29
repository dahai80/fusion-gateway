package browser

import (
    "encoding/json"
    "log/slog"
    "net/http"
    "time"
)

// Handler is the HTTP ingress for the browser subsystem. It translates REST
// requests under /v1/browser/* and /admin/api/browser/* into Proxy calls and
// serializes the results. Routes are registered via RegisterRoutes, which
// takes the server's auth-wrap functions so the same key/admin auth that
// guards /v1/* and /admin/* guards the browser routes (no parallel auth path).
type Handler struct {
    proxy *Proxy
}

// NewHandler binds an HTTP handler to a proxy. The proxy is nil-safe disabled
// via RegisterRoutes being a no-op when the proxy is nil (server.go gates the
// call on browser.enabled).
func NewHandler(proxy *Proxy) *Handler {
    return &Handler{proxy: proxy}
}

// WrapFunc is the shape of Server.withMiddleware / Server.withAdminOnly: a
// decorator that returns an auth-wrapped handler. Passing these in (rather
// than importing the server package) keeps browser/ from depending on server
// (ownership invariant: browser/ imports only config, lifecycle, safego,
// observability, stdlib).
type WrapFunc func(http.HandlerFunc) http.HandlerFunc

// RegisterRoutes wires the five browser routes onto the mux, wrapped with the
// supplied auth functions. Mirrors the MCP RegisterRoutesWithGate pattern.
// Create/Execute/Close go through the key-auth wrap (withMiddleware); the
// admin node map + metrics go through the admin-role wrap (withAdminOnly).
// Uses Go 1.22 path patterns {id} for the session-id path params.
func (h *Handler) RegisterRoutes(mux *http.ServeMux, withMiddleware, withAdminOnly WrapFunc) {
    if h == nil || h.proxy == nil {
        return
    }
    mux.HandleFunc("POST /v1/browser/sessions", withMiddleware(h.handleCreate))
    mux.HandleFunc("POST /v1/browser/sessions/{id}/actions", withMiddleware(h.handleExecute))
    mux.HandleFunc("DELETE /v1/browser/sessions/{id}", withMiddleware(h.handleClose))
    mux.HandleFunc("GET /v1/browser/nodes", withAdminOnly(h.handleNodes))
    mux.HandleFunc("GET /v1/browser/metrics", withAdminOnly(h.handleMetrics))
}

// maxBrowserBody bounds the create/execute request body so a client cannot
// OOM the gateway with a huge JSON payload. Smaller than the frame cap: this
// is the REST ingress bound, the frame cap bounds the UDS forward.
const maxBrowserBody = 1 << 20 // 1 MiB

// handleCreate: POST /v1/browser/sessions. Decodes CreateSessionRequest,
// runs placement + forward via Proxy.Create, returns the pinned session as
// 201. Errors map to 503 (quota/headroom/node) or 400 (malformed body).
func (h *Handler) handleCreate(w http.ResponseWriter, r *http.Request) {
    body := http.MaxBytesReader(w, r.Body, maxBrowserBody)
    var req CreateSessionRequest
    if err := json.NewDecoder(body).Decode(&req); err != nil {
        writeBrowserError(w, 400, "invalid_request", "malformed create_session body: "+err.Error(), false)
        return
    }
    if req.Mode != WebModeHeadless && req.Mode != WebModeHeaded {
        // Default to headless when omitted/empty (matches Swift default).
        if req.Mode == "" {
            req.Mode = WebModeHeadless
        } else {
            writeBrowserError(w, 400, "invalid_request", "mode must be headless or headed, got "+string(req.Mode), false)
            return
        }
    }
    res, err := h.proxy.Create(r.Context(), &req)
    if err != nil {
        writeRelayError(w, err)
        return
    }
    writeJSON(w, 201, res)
}

// handleExecute: POST /v1/browser/sessions/{id}/actions. Path param id is the
// session id; decodes BrowserActionRequest, forwards via Proxy.Execute, relays
// the state response verbatim (200). Errors: 404 pin miss, 503 dead node,
// 503 node error, 400 malformed body.
func (h *Handler) handleExecute(w http.ResponseWriter, r *http.Request) {
    sessionID := r.PathValue("id")
    if sessionID == "" {
        writeBrowserError(w, 400, "invalid_request", "missing session id in path", false)
        return
    }
    body := http.MaxBytesReader(w, r.Body, maxBrowserBody)
    var req BrowserActionRequest
    if err := json.NewDecoder(body).Decode(&req); err != nil {
        writeBrowserError(w, 400, "invalid_request", "malformed action body: "+err.Error(), false)
        return
    }
    req.SessionID = sessionID // path param is authoritative over body field
    res, err := h.proxy.Execute(r.Context(), &req)
    if err != nil {
        writeRelayError(w, err)
        return
    }
    // Relay the state payload verbatim — no re-encode (schema-drift safe).
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(200)
    if _, err := w.Write(res.Payload); err != nil {
        slog.Debug("browser execute: write response failed", "session", sessionID, "error", err)
    }
}

// handleClose: DELETE /v1/browser/sessions/{id}. Idempotent: a pin miss
// returns 204 (session already gone from the gateway's view). On success the
// pin is evicted by the proxy.
func (h *Handler) handleClose(w http.ResponseWriter, r *http.Request) {
    sessionID := r.PathValue("id")
    if sessionID == "" {
        writeBrowserError(w, 400, "invalid_request", "missing session id in path", false)
        return
    }
    if err := h.proxy.Close(r.Context(), sessionID); err != nil {
        // Close is best-effort; a mapped error is logged but still 204 (the
        // session is gone from the gateway's view either way).
        status, code, msg, _ := relayError(err)
        slog.Warn("browser close returned error (still 204, idempotent)",
            "session", sessionID, "status", status, "code", code, "message", msg)
    }
    w.WriteHeader(204)
}

// handleNodes: GET /v1/browser/nodes (admin-only). Returns the operator node
// map — one JSON object per node with id/node_id/socket/state/live/max/
// free_mem/last_poll. This is the #130 acceptance artifact.
func (h *Handler) handleNodes(w http.ResponseWriter, r *http.Request) {
    views := h.proxy.registry.Snapshot()
    out := make([]nodeMapEntry, 0, len(views))
    for _, v := range views {
        entry := nodeMapEntry{
            ID:         v.NodeID,
            SocketPath: v.SocketPath,
            State:      string(v.State),
            Source:     string(v.Source),
            Failures:   v.Failures,
            LastPoll:   v.LastPoll.UTC().Format(time.RFC3339Nano),
        }
        if v.Capacity != nil {
            entry.NodeID = v.Capacity.NodeID
            entry.LiveSessions = v.Capacity.LiveSessions
            entry.MaxSessions = v.Capacity.MaxSessions
            entry.FreeMemoryMB = v.Capacity.FreeMemoryMB
            entry.MaxTotalMemoryMB = v.Capacity.MaxTotalMemoryMB
            entry.RamGB = v.Capacity.RamGB
        }
        out = append(out, entry)
    }
    writeJSON(w, 200, map[string]any{"nodes": out, "count": len(out)})
}

// handleMetrics: GET /v1/browser/metrics?node=<id> (admin-only). Forwards a
// metrics query to the chosen node (or the first live node when node is
// omitted) and relays the opaque counters/latency verbatim.
func (h *Handler) handleMetrics(w http.ResponseWriter, r *http.Request) {
    nodeID := r.URL.Query().Get("node")
    res, err := h.proxy.Metrics(r.Context(), nodeID)
    if err != nil {
        writeRelayError(w, err)
        return
    }
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(200)
    if _, err := w.Write(res.Payload); err != nil {
        slog.Debug("browser metrics: write response failed", "node", nodeID, "error", err)
    }
}

// nodeMapEntry is one row of the admin node map. node_id is the per-process id
// from the capacity poll (may be empty before the first poll); id is the
// stable config label the registry keys on. last_poll is RFC3339Nano UTC.
type nodeMapEntry struct {
    ID               string `json:"id"`
    NodeID           string `json:"node_id,omitempty"`
    SocketPath       string `json:"socket_path"`
    State            string `json:"state"`
    Source           string `json:"source"`
    LiveSessions     int    `json:"live_sessions"`
    MaxSessions      int    `json:"max_sessions"`
    FreeMemoryMB     int    `json:"free_memory_mb"`
    MaxTotalMemoryMB int    `json:"max_total_memory_mb,omitempty"`
    RamGB            int    `json:"ram_gb,omitempty"`
    Failures         int    `json:"failures"`
    LastPoll         string `json:"last_poll"`
}

// writeJSON serializes obj as the response body with the given status.
func writeJSON(w http.ResponseWriter, status int, obj any) {
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(status)
    if err := json.NewEncoder(w).Encode(obj); err != nil {
        slog.Debug("browser: json encode response failed", "error", err)
    }
}

// writeBrowserError writes a gateway-originated error body {code, message,
// retryable} with the given status. Shape matches FBError so fusion-cowork
// parses one error schema for both node-origin and gateway-origin errors.
func writeBrowserError(w http.ResponseWriter, status int, code, message string, retryable bool) {
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(status)
    _ = json.NewEncoder(w).Encode(map[string]any{
        "code":      code,
        "message":   message,
        "retryable": retryable,
    })
}

// writeRelayError maps a proxy error to its HTTP status + relayed code/message
// via relayError, then writes the FBError-shaped body. Never coerces to 502
// (RC1: masked errors hide root cause).
func writeRelayError(w http.ResponseWriter, err error) {
    status, code, message, retryable := relayError(err)
    slog.Info("browser request relayed error",
        "status", status, "code", code, "retryable", retryable, "error", err)
    writeBrowserError(w, status, code, message, retryable)
}
