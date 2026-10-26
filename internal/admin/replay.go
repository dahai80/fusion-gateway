package admin

import (
    "bytes"
    "encoding/json"
    "fmt"
    "io"
    "log/slog"
    "net/http"
    "strconv"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/httpx"
)

// replayTimeout bounds the whole replay round-trip. Non-stream is forced, so
// a local engine computes the ENTIRE generation before the first response
// byte — the loopback transport's local header-wait default (300s, #208)
// matches this.
const replayTimeout = 300 * time.Second

// replayMaxBody caps the re-sent request body. Captured bodies are already
// capped by persistence capture.max_body_bytes; this is a second guard.
const replayMaxBody = 4 << 20

// replayResponse is the /admin/api/insights/logs/{id}/replay payload.
type replayResponse struct {
    ID       string          `json:"id"`
    Endpoint string          `json:"endpoint"`
    Status   int             `json:"status"`
    Response json.RawMessage `json:"response"`
}

// handleInsightsLogReplay — POST /admin/api/insights/logs/{id}/replay
// (plan §8): server-side replay. Re-sends the STORED request body to this
// gateway's own /v1 endpoint with stream forced off (a replay must return a
// complete answer, not a live SSE the admin API would have to re-pump), via
// loopback HTTP so the full production chain (auth → routing → adapter →
// logging) executes — the replay itself shows up in the Explorer as a new
// row, which is the desired evidence trail.
func (h *Handler) handleInsightsLogReplay(w http.ResponseWriter, r *http.Request, id string) {
    if h.insights == nil {
        h.insightsUnavailable(w)
        return
    }
    if r.Method != http.MethodPost {
        writeError(w, http.StatusMethodNotAllowed, "method not allowed")
        return
    }
    p, err := h.insights.Payload(r.Context(), id)
    if err != nil {
        slog.Warn("replay payload fetch failed", "id", id, "error", err)
        writeError(w, http.StatusNotFound, err.Error())
        return
    }
    body := []byte(p.ReqBody)
    if len(body) == 0 {
        body = []byte(p.ReqRaw)
    }
    if len(body) == 0 {
        writeError(w, http.StatusConflict, "no captured request body to replay")
        return
    }
    if len(body) > replayMaxBody {
        writeError(w, http.StatusRequestEntityTooLarge, "captured body exceeds replay cap")
        return
    }
    body, endpoint, err := replayNormalize(body)
    if err != nil {
        writeError(w, http.StatusUnprocessableEntity, err.Error())
        return
    }
    snap := config.GetSnapshot()
    masterKey := snap.Config.Auth.MasterKey
    if masterKey == "" {
        writeError(w, http.StatusServiceUnavailable, "no master key configured for replay auth")
        return
    }
    host := snap.Config.Server.Host
    if host == "" || host == "0.0.0.0" || host == "::" {
        host = "127.0.0.1"
    }
    target := "http://" + host + ":" + strconv.Itoa(snap.Config.Server.Port) + endpoint

    client := &http.Client{
        Timeout:   replayTimeout,
        Transport: httpx.TransportForBackend(config.BackendConfig{BaseURL: "http://" + host}),
    }
    req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, target, bytes.NewReader(body))
    if err != nil {
        writeError(w, http.StatusInternalServerError, err.Error())
        return
    }
    req.Header.Set("Content-Type", "application/json")
    req.Header.Set("Authorization", "Bearer "+masterKey)
    resp, err := client.Do(req)
    if err != nil {
        slog.Warn("replay loopback request failed", "id", id, "target", target, "error", err)
        writeError(w, http.StatusBadGateway, "replay request failed: "+err.Error())
        return
    }
    defer resp.Body.Close()
    respBody, err := io.ReadAll(io.LimitReader(resp.Body, replayMaxBody))
    if err != nil {
        slog.Warn("replay response read failed", "id", id, "error", err)
        writeError(w, http.StatusBadGateway, "replay response read failed: "+err.Error())
        return
    }
    actor := "admin"
    if claims := GetAdminClaims(r.Context()); claims != nil && claims.Username != "" {
        actor = claims.Username
    }
    h.auditEvent("info", "log_replayed", map[string]any{
        "id": id, "endpoint": endpoint, "status": resp.StatusCode, "by": actor,
    })
    slog.Info("log replayed", "id", id, "endpoint", endpoint, "status", resp.StatusCode, "by", actor)
    writeJSON(w, http.StatusOK, replayResponse{
        ID:       id,
        Endpoint: endpoint,
        Status:   resp.StatusCode,
        Response: json.RawMessage(respBody),
    })
}

// replayNormalize forces stream=false, strips credential-shaped fields, and
// picks the /v1 endpoint matching the stored body shape.
func replayNormalize(body []byte) ([]byte, string, error) {
    var m map[string]any
    if err := json.Unmarshal(body, &m); err != nil {
        return nil, "", err
    }
    delete(m, "stream")
    m["stream"] = false
    for _, k := range []string{"api_key", "apiKey", "authorization", "Authorization", "password"} {
        delete(m, k)
    }
    endpoint := "/v1/chat/completions"
    if _, ok := m["messages"]; !ok {
        if _, ok := m["prompt"]; ok {
            endpoint = "/v1/completions"
        } else if _, ok := m["input"]; ok {
            endpoint = "/v1/embeddings"
        } else {
            return nil, "", fmt.Errorf("body has none of messages/prompt/input")
        }
    }
    out, err := json.Marshal(m)
    if err != nil {
        return nil, "", err
    }
    return out, endpoint, nil
}
