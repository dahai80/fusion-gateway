package server

import (
    "encoding/json"
    "log/slog"
    "net/http"
    "strconv"
    "strings"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/tokenizer"
)

// enforceModelContextLimit rejects a request whose estimated total tokens exceed the
// configured per-model context limit (routing.model_context_limit). Returns true if
// it wrote a 400 and the caller must stop. An honest 400 instead of sending an
// oversized request to a cloud model that 400s upstream and gets masked as a gateway
// 502 (RC3, 11x in the logs). A model absent from the map is uncapped
// (backward-compatible); an empty budget (TotalBudget==0) is never rejected.
func (s *Server) enforceModelContextLimit(w http.ResponseWriter, r *http.Request, model string, budget tokenizer.TokenBudget) bool {
    if budget.TotalBudget == 0 {
        return false
    }
    snap := config.SnapshotFromContext(r.Context())
    if snap == nil {
        return false
    }
    limit, ok := snap.Config.Routing.ModelContextLimit[model]
    if !ok || limit <= 0 {
        return false
    }
    if budget.TotalBudget <= limit {
        return false
    }
    slog.Warn("context limit exceeded, rejecting before upstream",
        "model", model,
        "total_tokens", budget.TotalBudget,
        "limit", limit,
    )
    body, _ := json.Marshal(struct {
        Error struct {
            Message string `json:"message"`
            Type    string `json:"type"`
        } `json:"error"`
    }{
        Error: struct {
            Message string `json:"message"`
            Type    string `json:"type"`
        }{
            Message: "context length " + strconv.Itoa(budget.TotalBudget) + " exceeds model " + model + " limit " + strconv.Itoa(limit),
            Type:    "context_length_exceeded",
        },
    })
    w.Header().Set("Content-Type", "application/json")
    http.Error(w, string(body), http.StatusBadRequest)
    return true
}

// isContextLengthError matches upstream context-window-exceeded error strings so the
// gateway surfaces an honest 400 instead of masking the upstream error as a 502
// (RC3). Covers the glm5.2 "ContextWindowExceededError" and the common OpenAI-style
// "maximum context length" phrasing; conservative substring match, lowercase.
func isContextLengthError(err error) bool {
    if err == nil {
        return false
    }
    s := strings.ToLower(err.Error())
    for _, m := range []string{
        "context_length",
        "context window",
        "contextwindowexceeded",
        "maximum context length",
        "context length",
    } {
        if strings.Contains(s, m) {
            return true
        }
    }
    return false
}
