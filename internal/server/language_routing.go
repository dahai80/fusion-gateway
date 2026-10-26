package server

import (
    "log/slog"
    "net/http"
    "sort"
    "strings"

    "github.com/fusion-gateway/fusion-gateway/internal/adapter"
    "github.com/fusion-gateway/fusion-gateway/internal/middleware"
)

// LanguageHintHeader carries the request's prompt-language hint (#330):
// a lowercase ISO 639-1 code ("ar", "zh") matching the fusion-mlx
// `languages` metadata vocabulary (fusion-mlx#1416). v1 is client-declared;
// automatic detection can layer on later. Absent header = no rewrite.
const LanguageHintHeader = "X-Fusion-Language"

// rewriteForLanguage (#330) applies language-aware model routing: when the
// client hints the request language and the requested model does not declare
// that language in its fusion-mlx metadata, rewrite to a loaded local model
// that does. Falls back to the requested model when no candidate matches, no
// metadata exists, or the key's model allowlist rejects the candidate —
// never hard-fails (issue #330). Single-model deployments are unaffected:
// with no other candidate the requested model wins by definition.
func (s *Server) rewriteForLanguage(r *http.Request, model string) string {
    lang := strings.ToLower(strings.TrimSpace(r.Header.Get(LanguageHintHeader)))
    if lang == "" || model == "" {
        return model
    }
    mlx := s.pool.GetFusionMLX()
    if mlx == nil {
        return model
    }
    infos := mlx.ModelInfos()
    if len(infos) == 0 {
        slog.Debug("language routing: no model metadata yet, keeping requested",
            "language", lang, "model", model)
        return model
    }
    if modelSupportsLanguage(infos[model], lang) {
        return model
    }
    var candidates []string
    for id, info := range infos {
        if id == model || !info.Loaded {
            continue
        }
        if modelSupportsLanguage(info, lang) {
            candidates = append(candidates, id)
        }
    }
    if len(candidates) == 0 {
        slog.Info("language routing: no loaded model declares language, keeping requested",
            "language", lang, "model", model)
        return model
    }
    // Deterministic pick: map iteration order is random (List() flake trap).
    sort.Strings(candidates)
    chosen := candidates[0]
    if !middleware.CheckModelAllowlist(r, chosen) {
        slog.Warn("language routing: candidate rejected by key model allowlist, keeping requested",
            "language", lang, "model", model, "candidate", chosen)
        return model
    }
    slog.Info("language routing: rewrote model for language",
        "language", lang, "requested", model, "routed", chosen, "candidates", len(candidates))
    // Record the client-facing requested model on the log row; the handler
    // stamps entry.Model with the routed id later, so ModelMapped keeps the
    // disambiguation the same way qualified-id resolution does (§13.2).
    if entry := middleware.GetRequestLog(r.Context()); entry != nil && entry.ModelMapped == "" {
        entry.ModelMapped = model
    }
    return chosen
}

// modelSupportsLanguage reports whether a model's metadata declares lang
// (case-insensitive ISO code match). Empty metadata = no declaration.
func modelSupportsLanguage(info adapter.ModelInfo, lang string) bool {
    for _, l := range info.Languages {
        if strings.EqualFold(l, lang) {
            return true
        }
    }
    return false
}
