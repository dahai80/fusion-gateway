package server

import (
    "bytes"
    "encoding/json"
    "io"
    "log/slog"
    "net/http"

    "github.com/fusion-gateway/fusion-gateway/internal/adapter"
    "github.com/fusion-gateway/fusion-gateway/internal/middleware"
)

// handleSolveStream implements #203: a dedicated K-12 BNUP endpoint at
// /v1/solve-stream. It accepts a standard OpenAI chat-completions body
// (messages + optional model/grammar) and forces BNUP semantics — model
// remapped to routing.bnup_model, grammar set to routing.bnup_grammar —
// regardless of the global routing.bnup_mode flag, then delegates to the
// normal chat-completions streaming path.
//
// This lets K-12 clients POST a math problem to one well-known URL without
// knowing the "bnup-*" alias convention or toggling bnup_mode. The response
// is always SSE (stream=true is forced); a non-stream caller gets a 400.
func (s *Server) handleSolveStream(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        http.Error(w, `{"error":{"message":"Method not allowed","type":"invalid_request"}}`, http.StatusMethodNotAllowed)
        return
    }

    maxBodySize := int64(s.cfg.Config.Server.MaxRequestBodySize)
    if maxBodySize <= 0 {
        maxBodySize = 5 << 20
    }
    body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodySize))
    if err != nil {
        slog.Warn("solve-stream: failed to read request", "error", err)
        http.Error(w, `{"error":{"message":"Failed to read request","type":"invalid_request"}}`, http.StatusBadRequest)
        return
    }
    r.Body.Close()

    var req adapter.ChatRequest
    if err := json.Unmarshal(body, &req); err != nil {
        slog.Error("solve-stream: invalid json", "error", err)
        http.Error(w, `{"error":{"message":"Invalid JSON","type":"invalid_request"}}`, http.StatusBadRequest)
        return
    }

    // Force streaming — the endpoint contract is SSE.
    req.Stream = true

    // Force BNUP model remap + grammar attach (bypasses bnup_mode gate).
    s.applyBnupMode(&req, true)
    slog.Info("solve-stream dispatched",
        "model", req.Model,
        "grammar", req.Grammar,
        "config_version", s.cfg.Version,
    )

    // Re-encode and delegate to the standard chat-completions handler so
    // routing, PII, token-budget, guard, and SSE pipeline all apply.
    modified, err := json.Marshal(req)
    if err != nil {
        slog.Error("solve-stream: failed to re-marshal", "error", err)
        http.Error(w, `{"error":{"message":"Internal error","type":"server_error"}}`, http.StatusInternalServerError)
        return
    }
    r.Body = io.NopCloser(bytes.NewReader(modified))
    r.ContentLength = int64(len(modified))
    // The tee already captured the original body; skip the finalize drain or
    // an early exit in handleChatCompletions would append the re-marshaled
    // body on top of it.
    middleware.MarkRequestBodyCaptured(r.Context())
    s.handleChatCompletions(w, r)
}
