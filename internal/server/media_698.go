package server

import (
    "bytes"
    "encoding/json"
    "io"
    "log/slog"
    "net/http"

    "github.com/fusion-gateway/fusion-gateway/internal/adapter"
    "github.com/fusion-gateway/fusion-gateway/internal/router"
)

// #698: gateway passthrough for the fusion-mlx media surfaces the drama
// pipeline calls directly at :11434 today (video-dub.ts notes the gap).
// Without these routes the "all downstream traffic through :11432"
// governance directive is violated and the 11434 dual-path auth gap
// persists. All four are fusion-mlx-specific endpoints (mflux/SeedVR2 and
// the video engine have no cloud equivalent), so they route LOCAL-ONLY:
// the router still decides (breaker admission, #429 rejection surfacing,
// backend access checks apply), but a cloud decision is a 400 rather than
// a silent cloud fallthrough — there is no cloud backend that speaks
// /v1/videos/generate.
//
// Response bodies (audio/video base64, PNG bytes) stream through the
// ReverseProxy unbuffered; request bodies are capped by wrapProxyBody
// (ProxyMaxBodySize, 256 MiB default) — a video i2v request embeds a
// base64 frame, so the 5 MiB chat-plane cap would reject legit calls.

// mediaJSONHandler routes a JSON media request: extract the model for the
// router, admit through the breaker, and forward the buffered body verbatim
// to fusion-mlx on a local decision. Mirrors handleImages (#366) minus the
// cloud-provider branch.
func (s *Server) mediaJSONHandler(surface string) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        if r.Method != http.MethodPost {
            http.Error(w, `{"error":{"message":"Method not allowed","type":"invalid_request"}}`, http.StatusMethodNotAllowed)
            return
        }
        body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, s.proxyBodyCap()))
        if err != nil {
            slog.Error("media request body over cap", "surface", surface, "error", err)
            http.Error(w, `{"error":{"message":"Request body too large","type":"invalid_request"}}`, http.StatusRequestEntityTooLarge)
            return
        }
        defer r.Body.Close()

        var req struct {
            Model string `json:"model"`
        }
        if err := json.Unmarshal(body, &req); err != nil {
            http.Error(w, `{"error":{"message":"Invalid JSON","type":"invalid_request"}}`, http.StatusBadRequest)
            return
        }

        ctx := adapter.WithFusionHeaders(r.Context(), r)
        decision := s.router.Decide(ctx, &router.RouteRequest{Model: req.Model})
        if decision.Rejected {
            slog.Warn("media request blocked by route rejection", "surface", surface, "model", req.Model, "reason", decision.Reason)
            writeRouteRejection(w, decision)
            return
        }
        if !s.checkBackendAccess(w, r, string(decision.Backend)) {
            return
        }
        if decision.Backend != router.LocalBackend {
            slog.Warn("media surface is local-only, refusing cloud decision", "surface", surface, "backend", decision.Backend)
            http.Error(w, `{"error":{"message":"This media endpoint is served by the local fusion-mlx backend only","type":"invalid_request"}}`, http.StatusBadRequest)
            return
        }
        slog.Info("media request routed to local fusion-mlx backend", "surface", surface, "model", req.Model, "reason", decision.Reason)
        s.proxyLocalWithBody(w, r, body, surface)
    }
}

// mediaMultipartHandler routes a multipart media request (image edits,
// super-resolution): the model rides a form field, so the buffered body is
// parsed once for routing, then re-armed verbatim for the ReverseProxy hop.
func (s *Server) mediaMultipartHandler(surface string) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        if r.Method != http.MethodPost {
            http.Error(w, `{"error":{"message":"Method not allowed","type":"invalid_request"}}`, http.StatusMethodNotAllowed)
            return
        }
        raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, s.proxyBodyCap()))
        if err != nil {
            slog.Error("media multipart body over cap", "surface", surface, "error", err)
            http.Error(w, `{"error":{"message":"Request body too large","type":"invalid_request"}}`, http.StatusRequestEntityTooLarge)
            return
        }
        defer r.Body.Close()

        model := r.URL.Query().Get("model")
        if model == "" {
            // Parse a throwaway copy for the model field only; the original
            // buffered bytes stay untouched for the upstream forward.
            probe := &http.Request{Header: r.Header.Clone(), Method: r.Method, URL: r.URL}
            probe.Body = io.NopCloser(bytes.NewReader(raw))
            probe.ContentLength = int64(len(raw))
            if err := probe.ParseMultipartForm(32 << 20); err == nil && probe.MultipartForm != nil {
                _ = probe.MultipartForm.RemoveAll()
                model = probe.FormValue("model")
            }
        }

        ctx := adapter.WithFusionHeaders(r.Context(), r)
        decision := s.router.Decide(ctx, &router.RouteRequest{Model: model})
        if decision.Rejected {
            slog.Warn("media request blocked by route rejection", "surface", surface, "model", model, "reason", decision.Reason)
            writeRouteRejection(w, decision)
            return
        }
        if !s.checkBackendAccess(w, r, string(decision.Backend)) {
            return
        }
        if decision.Backend != router.LocalBackend {
            slog.Warn("media surface is local-only, refusing cloud decision", "surface", surface, "backend", decision.Backend)
            http.Error(w, `{"error":{"message":"This media endpoint is served by the local fusion-mlx backend only","type":"invalid_request"}}`, http.StatusBadRequest)
            return
        }
        slog.Info("media request routed to local fusion-mlx backend", "surface", surface, "model", model, "reason", decision.Reason)
        s.proxyLocalWithBody(w, r, raw, surface)
    }
}

// handleAudioVoices forwards GET /v1/audio/voices (voice capability
// metadata, mlx #1085) to the local backend verbatim. No model rides the
// request, so the router has nothing to decide — same auth chain
// (withMiddleware) and body cap as the #340 tool-surface passthrough.
func (s *Server) handleAudioVoices(w http.ResponseWriter, r *http.Request) {
    s.proxyToFusionMLX(w, r, "audio-voices")
}
