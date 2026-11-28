package server

// Artifacts JSON-RPC proxy (fusion-web-shell#196 leg 1): the shell Files
// surface reaches fusion-artifacts-engine (:11451) through this route so
// the engine API key never reaches a browser. Session + admin role
// required: artifact.list_all/list_recycle are tenant-wide on the engine
// side (no owner filter yet — upstream artifacts-engine issue pending),
// so exposure is limited to shell admins; non-admin callers get 403.
//
// Identity stamping: caller_user_id / owner_user_id are overwritten from
// the session claims before proxying, so the engine-side owner
// enforcement (docs/acl-semantics.md) always acts on the authenticated
// principal and client-sent values cannot spoof another tenant.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/fusion-gateway/fusion-gateway/internal/shell"
)

const (
	artifactsDefaultURL = "http://127.0.0.1:11451"
	artifactsBodyLimit  = 1 << 20 // 1 MiB, matches maxShellBody
	artifactsTimeout    = 30 * time.Second
)

// artifactsSettings resolves upstream URL + API key: env first (runtime
// rotation, mirroring the mlx backend env bindings), then config values,
// then the registered loopback default. The key never reaches a browser
// and is never logged.
func (s *Server) artifactsSettings() (string, string) {
	url := ""
	key := ""
	if s.cfg != nil {
		url = strings.TrimSpace(s.cfg.Config.ArtifactsEngine.URL)
		key = strings.TrimSpace(s.cfg.Config.ArtifactsEngine.APIKey)
	}
	if v := strings.TrimSpace(os.Getenv("FUSION_ARTIFACTS_URL")); v != "" {
		url = v
	}
	if v := strings.TrimSpace(os.Getenv("FUSION_ARTIFACTS_API_KEY")); v != "" {
		key = v
	}
	if url == "" {
		url = artifactsDefaultURL
	}
	return url, key
}

type artifactsRpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  map[string]any  `json:"params"`
}

// handleArtifactsRpc: POST /api/v1/artifacts — single JSON-RPC hop.
func (s *Server) handleArtifactsRpc(w http.ResponseWriter, r *http.Request) {
	claims := shellClaimsFrom(r.Context())
	var req artifactsRpcRequest
	body := http.MaxBytesReader(w, r.Body, artifactsBodyLimit)
	if err := json.NewDecoder(body).Decode(&req); err != nil {
		shell.WriteEnvelope(w, http.StatusBadRequest, "invalid_request", "body must be a JSON-RPC request", "", false)
		return
	}
	if req.Method == "" {
		shell.WriteEnvelope(w, http.StatusBadRequest, "invalid_request", "method is required", "", false)
		return
	}
	if req.Params == nil {
		req.Params = map[string]any{}
	}
	req.Params["caller_user_id"] = claims.Sub
	req.Params["owner_user_id"] = claims.Sub

	payload, err := json.Marshal(req)
	if err != nil {
		shell.WriteEnvelope(w, http.StatusBadRequest, "invalid_request", "request is not valid JSON-RPC", "", false)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), artifactsTimeout)
	defer cancel()
	upstreamURL, apiKey := s.artifactsSettings()
	upstreamURL = strings.TrimSuffix(upstreamURL, "/") + "/"
	upstream, err := http.NewRequestWithContext(ctx, http.MethodPost, upstreamURL, bytes.NewReader(payload))
	if err != nil {
		shell.WriteEnvelope(w, http.StatusBadGateway, "upstream_error", "artifacts upstream request build failed", "", false)
		return
	}
	upstream.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		upstream.Header.Set("X-API-Key", apiKey)
	}

	resp, err := http.DefaultClient.Do(upstream)
	if err != nil {
		slog.Warn("artifacts proxy upstream failed", "method", req.Method, "user", claims.Sub, "error", err)
		shell.WriteEnvelope(w, http.StatusBadGateway, "upstream_error", "artifacts engine unreachable", "", false)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, artifactsBodyLimit))
	if err != nil {
		slog.Warn("artifacts proxy upstream read failed", "method", req.Method, "error", err)
		shell.WriteEnvelope(w, http.StatusBadGateway, "upstream_error", "artifacts engine response read failed", "", false)
		return
	}
	if resp.StatusCode != http.StatusOK {
		slog.Warn("artifacts proxy upstream rejected", "method", req.Method, "user", claims.Sub, "status", resp.StatusCode)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(respBody)
}
