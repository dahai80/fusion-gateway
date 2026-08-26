package server

import (
    "context"
    "encoding/json"
    "fmt"
    "io"
    "log/slog"
    "net/http"

    "github.com/fusion-gateway/fusion-gateway/internal/adapter"
    "github.com/fusion-gateway/fusion-gateway/internal/observability"
    "github.com/fusion-gateway/fusion-gateway/internal/router"
)

func (s *Server) handleRealtime(w http.ResponseWriter, r *http.Request) {
    if !s.cfg.Config.Realtime.Enabled || s.realtimeProxy == nil {
        http.Error(w, `{"error":{"message":"Realtime API not enabled","type":"invalid_request"}}`, http.StatusNotFound)
        return
    }

    backendURL := s.cfg.Config.Realtime.BackendURL
    if backendURL == "" {
        http.Error(w, `{"error":{"message":"Realtime backend not configured","type":"server_error"}}`, http.StatusServiceUnavailable)
        return
    }

    apiKey := s.cfg.Config.Realtime.APIKey

    slog.Info("realtime: incoming connection",
        "client", r.RemoteAddr,
        "backend", backendURL,
    )

    observability.RecordRouteDecision("realtime", "websocket_proxy")
    s.realtimeProxy.UpgradeAndProxy(w, r, backendURL, apiKey)
}

func (s *Server) handleTranscriptions(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        http.Error(w, `{"error":{"message":"Method not allowed","type":"invalid_request"}}`, http.StatusMethodNotAllowed)
        return
    }
    if err := r.ParseMultipartForm(32 << 20); err != nil {
        slog.Error("failed to parse multipart form", "error", err)
        http.Error(w, `{"error":{"message":"Failed to parse multipart form","type":"invalid_request"}}`, http.StatusBadRequest)
        return
    }
    defer func() { _ = r.MultipartForm.RemoveAll() }()

    model := r.FormValue("model")
    if model == "" { model = "whisper-1" }

    ctx := adapter.WithFusionHeaders(r.Context(), r)
    decision := s.router.Decide(ctx, &router.RouteRequest{Model: model})

    if !s.checkBackendAccess(w, r, string(decision.Backend)) {
        return
    }

    provider := s.resolveCloudProvider(decision, nil, w)
    if provider == nil { return }

    type TranscriptionProvider interface {
        Transcription(ctx context.Context, r *http.Request) (json.RawMessage, error)
    }
    if tp, ok := provider.(TranscriptionProvider); ok {
        result, err := tp.Transcription(ctx, r)
        if err != nil {
            slog.Error("transcription failed", "error", err)
            http.Error(w, fmt.Sprintf(`{"error":{"message":"%s","type":"api_error"}}`, err.Error()), http.StatusBadGateway)
            return
        }
        w.Header().Set("Content-Type", "application/json")
        _, _ = w.Write(result)
        return
    }
    http.Error(w, `{"error":{"message":"Provider does not support transcription","type":"invalid_request"}}`, http.StatusBadRequest)
}

func (s *Server) handleSpeech(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        http.Error(w, `{"error":{"message":"Method not allowed","type":"invalid_request"}}`, http.StatusMethodNotAllowed)
        return
    }
    // R2 fix: use MaxBytesReader to limit request body size
    maxBodySize := int64(s.cfg.Config.Server.MaxRequestBodySize)
    if maxBodySize <= 0 {
        maxBodySize = 5 << 20
    }
    body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodySize))
    if err != nil {
        http.Error(w, `{"error":{"message":"Failed to read request","type":"invalid_request"}}`, http.StatusBadRequest)
        return
    }
    defer r.Body.Close()

    var req struct {
        Model string `json:"model"`
        Input string `json:"input"`
        Voice string `json:"voice"`
    }
    if err := json.Unmarshal(body, &req); err != nil {
        http.Error(w, `{"error":{"message":"Invalid JSON","type":"invalid_request"}}`, http.StatusBadRequest)
        return
    }

    ctx := adapter.WithFusionHeaders(r.Context(), r)
    decision := s.router.Decide(ctx, &router.RouteRequest{Model: req.Model})

    if !s.checkBackendAccess(w, r, string(decision.Backend)) {
        return
    }

    provider := s.resolveCloudProvider(decision, nil, w)
    if provider == nil { return }

    type SpeechProvider interface {
        Speech(ctx context.Context, reqBody []byte) ([]byte, string, error)
    }
    if sp, ok := provider.(SpeechProvider); ok {
        audioData, contentType, err := sp.Speech(ctx, body)
        if err != nil {
            slog.Error("speech synthesis failed", "error", err)
            http.Error(w, fmt.Sprintf(`{"error":{"message":"%s","type":"api_error"}}`, err.Error()), http.StatusBadGateway)
            return
        }
        w.Header().Set("Content-Type", contentType)
        _, _ = w.Write(audioData)
        return
    }
    http.Error(w, `{"error":{"message":"Provider does not support speech","type":"invalid_request"}}`, http.StatusBadRequest)
}

func (s *Server) handleModeration(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        http.Error(w, `{"error":{"message":"Method not allowed","type":"invalid_request"}}`, http.StatusMethodNotAllowed)
        return
    }
    // R2 fix: use MaxBytesReader to limit request body size
    maxBodySize := int64(s.cfg.Config.Server.MaxRequestBodySize)
    if maxBodySize <= 0 {
        maxBodySize = 5 << 20
    }
    body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodySize))
    if err != nil {
        http.Error(w, `{"error":{"message":"Failed to read request","type":"invalid_request"}}`, http.StatusBadRequest)
        return
    }
    defer r.Body.Close()

    var req struct {
        Model string      `json:"model,omitempty"`
        Input interface{} `json:"input"`
    }
    if err := json.Unmarshal(body, &req); err != nil {
        http.Error(w, `{"error":{"message":"Invalid JSON","type":"invalid_request"}}`, http.StatusBadRequest)
        return
    }

    ctx := adapter.WithFusionHeaders(r.Context(), r)
    decision := s.router.Decide(ctx, &router.RouteRequest{Model: req.Model})

    if !s.checkBackendAccess(w, r, string(decision.Backend)) {
        return
    }

    provider := s.resolveCloudProvider(decision, nil, w)
    if provider == nil { return }

    type ModerationProvider interface {
        Moderation(ctx context.Context, reqBody []byte) (json.RawMessage, error)
    }
    if mp, ok := provider.(ModerationProvider); ok {
        result, err := mp.Moderation(ctx, body)
        if err != nil {
            slog.Error("moderation failed", "error", err)
            http.Error(w, fmt.Sprintf(`{"error":{"message":"%s","type":"api_error"}}`, err.Error()), http.StatusBadGateway)
            return
        }
        w.Header().Set("Content-Type", "application/json")
        _, _ = w.Write(result)
        return
    }
    http.Error(w, `{"error":{"message":"Provider does not support moderation","type":"invalid_request"}}`, http.StatusBadRequest)
}
