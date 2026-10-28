package server

import (
    "bytes"
    "io"
    "log/slog"
    "net/http"
)

// registerMLXToolRoutes wires the #340 routes on the shared mux, behind
// withMiddleware so they share the v1 surface's auth/rate-limit chain.
func (s *Server) registerMLXToolRoutes(mux *http.ServeMux) {
    // Gap 1: fusion-mlx tool-surface passthrough — fusion-model-hub (and
    // other trusted clients) point ALL traffic at the gateway instead of
    // keeping a direct fusion-mlx side channel for the most privileged
    // operations (quantize/convert/analyze).
    mux.HandleFunc("/v1/hardware", s.withMiddleware(s.handleMLXToolProxy))
    mux.HandleFunc("/v1/recommend", s.withMiddleware(s.handleMLXToolProxy))
    mux.HandleFunc("/v1/analyze", s.withMiddleware(s.handleMLXToolProxy))
    mux.HandleFunc("/v1/analyze-batch", s.withMiddleware(s.handleMLXToolProxy))
    mux.HandleFunc("/v1/migration-level", s.withMiddleware(s.handleMLXToolProxy))
    mux.HandleFunc("/v1/convert", s.withMiddleware(s.handleMLXToolProxy))
    mux.HandleFunc("/v1/quantize", s.withMiddleware(s.handleMLXToolProxy))
    mux.HandleFunc("/v1/quantize/", s.withMiddleware(s.handleMLXToolProxy))
    mux.HandleFunc("/v1/benchmarks", s.withMiddleware(s.handleMLXToolProxy))
    // Gap 2: distinct management route for trusted hub callers —
    // /v1/admin/models/{name}/load|unload proxies straight to fusion-mlx,
    // bypassing the hub-registry rewrite on /v1/models/{id}/load.
    mux.HandleFunc("/v1/admin/models/", s.withMiddleware(s.handleMLXModelManage))
}

// handleMLXToolProxy forwards the fusion-mlx tool surface to the local
// backend (#340 Gap 1): /v1/hardware, /v1/recommend, /v1/analyze,
// /v1/analyze-batch, /v1/migration-level, /v1/convert, /v1/quantize*,
// /v1/benchmarks. Method/path/query/body are proxied verbatim (same chain as
// handleMLXStats); the fusion-mlx ReverseProxy injects Authorization +
// X-Fusion-Route so route_guard admits the request. Clients authenticate to
// the gateway with their fg-key — these are the most privileged operations
// (quantize/convert), so routing them through the gateway keeps them on the
// audit/rate-limit surface instead of a direct-to-mlx side channel.
func (s *Server) handleMLXToolProxy(w http.ResponseWriter, r *http.Request) {
    s.proxyToFusionMLX(w, r, "tool")
}

// handleMLXModelManage forwards /v1/admin/models/* to fusion-mlx verbatim
// (#340 Gap 2): a distinct management route so trusted callers (fusion-model-
// hub) can drive load/unload/merge-adapter through the gateway WITHOUT the
// /v1/models/{id}/load hub-registry rewrite in handleModelLoadUnload. The
// public /v1/models/{id}/load route keeps its hub semantics — hub model IDs
// only, MLX repo names loop back to the hub and 404 there.
func (s *Server) handleMLXModelManage(w http.ResponseWriter, r *http.Request) {
    s.proxyToFusionMLX(w, r, "model-manage")
}

// proxyToFusionMLX is the shared verbatim forward: nil backend = 503, body
// capped (E5), credentials injected by the provider's ReverseProxy.
func (s *Server) proxyToFusionMLX(w http.ResponseWriter, r *http.Request, surface string) {
    mlx := s.pool.GetFusionMLX()
    if mlx == nil {
        slog.Error("fusion-mlx proxy: backend not configured", "surface", surface, "path", r.URL.Path)
        http.Error(w, "fusion-mlx backend not configured", http.StatusServiceUnavailable)
        return
    }
    slog.Debug("fusion-mlx proxy forwarding", "surface", surface, "method", r.Method, "path", r.URL.Path)
    // E5 (audit): cap the proxied body — tool/management paths accept job
    // definitions, and an unbounded body OOMs fusion-mlx's single process.
    r = s.wrapProxyBody(w, r)
    mlx.ReverseProxy().ServeHTTP(w, r)
}

// proxyLocalWithBody re-arms the buffered request body and forwards it to the
// local fusion-mlx backend verbatim (#366): handlers that already read the
// body to extract the model field (speech, images) rebuild it here so the
// ReverseProxy hop sees the original JSON. The upstream path is identical to
// the inbound path (/v1/audio/speech, /v1/images/generations).
func (s *Server) proxyLocalWithBody(w http.ResponseWriter, r *http.Request, body []byte, surface string) {
    mlx := s.pool.GetFusionMLX()
    if mlx == nil {
        slog.Error("local media proxy: fusion-mlx backend not configured", "surface", surface, "path", r.URL.Path)
        http.Error(w, `{"error":{"message":"Local backend not available","type":"server_error"}}`, http.StatusServiceUnavailable)
        return
    }
    r.Body = io.NopCloser(bytes.NewReader(body))
    r.ContentLength = int64(len(body))
    slog.Info("forwarding request to local fusion-mlx backend", "surface", surface, "method", r.Method, "path", r.URL.Path)
    r = s.wrapProxyBody(w, r)
    mlx.ReverseProxy().ServeHTTP(w, r)
}
