package server

import (
    "log/slog"
    "strings"

    "github.com/fusion-gateway/fusion-gateway/internal/adapter"
    "github.com/fusion-gateway/fusion-gateway/internal/router"
)

// multimodalDecisionFor returns the route decision for a multimodal request
// (image/audio content) plus the model the request should be rewritten to (empty
// when unchanged). It is shared by /v1/chat/completions and /v1/messages so both
// endpoints apply the SAME logic: local vision model if loaded, cloud VLM
// fallback if configured, otherwise the multimodal_unconfigured sentinel the
// caller turns into a clear 400. Protocol-specific block-type detection
// (chatRequestHasImage / anthropicRequestHasImage) stays in each handler — only
// the decision is unified here (RC4). resolvedModel is the vision model id the
// handler should set on its request struct before forwarding; an empty value
// means keep the original model (used by the unconfigured sentinel path).
func (s *Server) multimodalDecisionFor(model string) (*router.RouteDecision, string) {
    mm := s.cfg.Config.Routing.Multimodal
    if vlModel := strings.TrimSpace(mm.LocalModel); vlModel != "" {
        if s.localVisionModelLoaded(vlModel) {
            slog.Info("multimodal request forced to local vision model",
                "client_model", model, "vision_model", vlModel)
            return &router.RouteDecision{Backend: router.LocalBackend, Reason: "multimodal_local_vision"}, vlModel
        }
        slog.Info("multimodal local vision model not loaded, trying cloud fallback",
            "vision_model", vlModel)
    }
    cloudBackend := strings.TrimSpace(mm.CloudBackend)
    cloudModel := strings.TrimSpace(mm.CloudModel)
    if cloudBackend != "" && cloudModel != "" {
        slog.Info("multimodal request routed to cloud VLM",
            "client_model", model, "cloud_backend", cloudBackend, "cloud_model", cloudModel)
        return &router.RouteDecision{Backend: router.CloudBackend, Reason: "multimodal_cloud_vlm", CloudTarget: cloudBackend}, cloudModel
    }
    return &router.RouteDecision{Backend: router.CloudBackend, Reason: "multimodal_unconfigured"}, ""
}

// multimodalRouteDecision is the /v1/chat/completions entry point: it detects an
// OpenAI-shape image block, then delegates the unified decision (RC4). It rewrites
// req.Model in place to the resolved vision model when one is available. Returns
// nil for a text-only request (caller routes normally).
func (s *Server) multimodalRouteDecision(req *adapter.ChatRequest) *router.RouteDecision {
    if !chatRequestHasImage(req) {
        return nil
    }
    decision, resolved := s.multimodalDecisionFor(req.Model)
    if resolved != "" {
        req.Model = resolved
    }
    return decision
}

// localVisionModelLoaded reports whether the given vision model id is loaded on
// the local fusion-mlx node. Returns false when no fusion-mlx provider is
// configured or the model is absent from its loaded model set. The model set is
// refreshed periodically (cmd/gateway/main.go safeGo loop) from fusion-mlx
// /v1/models, so this reflects the live loaded state, not just config.
func (s *Server) localVisionModelLoaded(model string) bool {
    mlx := s.pool.GetFusionMLX()
    if mlx == nil {
        return false
    }
    loaded := mlx.ModelSet()
    return loaded != nil && loaded[model]
}

// multimodalUnconfigured reports whether a decision is the sentinel that means
// "multimodal request but no local vision model and no cloud VLM configured" —
// the caller must reject with a clear 400 rather than forward.
func multimodalUnconfigured(d *router.RouteDecision) bool {
    return d != nil && d.Reason == "multimodal_unconfigured"
}

// multimodalDivertDecision is the shared guard for every downstream divert of
// an already-decided local/cluster route (#389). The entry guard rewrites a
// multimodal request's Model to the LOCAL vision id; every later divert site
// (overflow cooldown pre-divert, RR4 slot-full, stream/non-stream cloud
// overflow) that replaced that decision with a plain cloud decision sent the
// local VLM id to the text-only cloud_default — which rejects it with 400
// (glm-5 is text-only) and the gateway surfaces 502. Intermittency came from
// routing.fallback.overflow.cooldown: an UNRELATED local failure arms the
// cooldown window, so the NEXT multimodal request is pre-diverted at site 1
// without any multimodal-specific signal.
//
// Three states:
//   - (nil, true)  — text-only request: divert rules unchanged.
//   - (d, true)    — multimodal request and routing.multimodal.cloud_backend +
//     cloud_model are configured: the caller may divert, but MUST use d
//     (CloudBackend target, multimodal_cloud_vlm_divert reason); req.Model is
//     rewritten to the cloud VLM id so the text-only default never sees a
//     local vision id.
//   - (nil, false) — multimodal request with no cloud VLM configured: the
//     divert is REFUSED (fail-closed). Site semantics differ: the cooldown
//     pre-divert simply keeps the local decision (cooldown is an
//     optimization), slot-full returns a clear 503, and the overflow paths
//     surface the local error exactly like the disabled-overflow policy.
func (s *Server) multimodalDivertDecision(req *adapter.ChatRequest) (*router.RouteDecision, bool) {
    if !chatRequestHasImage(req) {
        return nil, true
    }
    mm := s.cfg.Config.Routing.Multimodal
    cloudBackend := strings.TrimSpace(mm.CloudBackend)
    cloudModel := strings.TrimSpace(mm.CloudModel)
    if cloudBackend == "" || cloudModel == "" {
        slog.Info("multimodal divert refused: no cloud VLM configured",
            "model", req.Model,
            "hint", "set routing.multimodal.cloud_backend + cloud_model to allow cloud diversion of multimodal requests")
        return nil, false
    }
    slog.Info("multimodal divert retargeted to cloud VLM",
        "model", req.Model, "cloud_backend", cloudBackend, "cloud_model", cloudModel)
    req.Model = cloudModel
    return &router.RouteDecision{
        Backend:     router.CloudBackend,
        Reason:      "multimodal_cloud_vlm_divert",
        CloudTarget: cloudBackend,
    }, true
}
