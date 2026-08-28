package server

import (
    "errors"
    "net/http"
    "net/http/httptest"
    "strings"
    "testing"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/tokenizer"
)

func TestRC3_PreemptiveCapRejects400(t *testing.T) {
    s := newTestServer()
    s.cfg.Config.Routing.ModelContextLimit = map[string]int{"m": 100}

    budget := tokenizer.TokenBudget{InputTokens: 150, PredictOutputTokens: 50, TotalBudget: 200}
    req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
    req = req.WithContext(config.WithSnapshot(req.Context(), s.cfg))
    rec := httptest.NewRecorder()

    rejected := s.enforceModelContextLimit(rec, req, "m", budget)
    if !rejected {
        t.Fatalf("expected rejection (total %d > limit 100), got accepted", budget.TotalBudget)
    }
    if rec.Code != http.StatusBadRequest {
        t.Fatalf("expected 400, got %d", rec.Code)
    }
    body := rec.Body.String()
    if !strings.Contains(body, "exceeds model") {
        t.Fatalf("expected body to mention 'exceeds model', got: %s", body)
    }
    if !strings.Contains(body, "context_length_exceeded") {
        t.Fatalf("expected error type 'context_length_exceeded', got: %s", body)
    }
}

func TestRC3_PreemptiveCapPassesUnderLimit(t *testing.T) {
    s := newTestServer()
    s.cfg.Config.Routing.ModelContextLimit = map[string]int{"m": 100}

    budget := tokenizer.TokenBudget{InputTokens: 30, PredictOutputTokens: 20, TotalBudget: 50}
    req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
    req = req.WithContext(config.WithSnapshot(req.Context(), s.cfg))
    rec := httptest.NewRecorder()

    if s.enforceModelContextLimit(rec, req, "m", budget) {
        t.Fatalf("expected pass (total %d <= limit 100), got rejected", budget.TotalBudget)
    }
    if rec.Code != http.StatusOK {
        t.Fatalf("expected no write (200 default), got %d", rec.Code)
    }
}

func TestRC3_PreemptiveCapNoMapEntry(t *testing.T) {
    s := newTestServer()
    s.cfg.Config.Routing.ModelContextLimit = map[string]int{"other": 100}

    budget := tokenizer.TokenBudget{InputTokens: 150, PredictOutputTokens: 50, TotalBudget: 200}
    req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
    req = req.WithContext(config.WithSnapshot(req.Context(), s.cfg))
    rec := httptest.NewRecorder()

    if s.enforceModelContextLimit(rec, req, "uncapped-model", budget) {
        t.Fatalf("expected pass (model absent from map = uncapped), got rejected")
    }
}

func TestRC3_PreemptiveCapZeroBudget(t *testing.T) {
    s := newTestServer()
    s.cfg.Config.Routing.ModelContextLimit = map[string]int{"m": 100}

    budget := tokenizer.TokenBudget{}
    req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
    req = req.WithContext(config.WithSnapshot(req.Context(), s.cfg))
    rec := httptest.NewRecorder()

    if s.enforceModelContextLimit(rec, req, "m", budget) {
        t.Fatalf("expected pass (TotalBudget==0 = never reject), got rejected")
    }
}

func TestRC3_IsContextLengthError(t *testing.T) {
    cases := []struct {
        err  error
        want bool
    }{
        {errors.New("ContextWindowExceededError: input too long"), true},
        {errors.New("maximum context length exceeded by 1234 tokens"), true},
        {errors.New("context_length_exceeded"), true},
        {errors.New("this model has a context window of 8192"), true},
        {errors.New("connection refused"), false},
        {errors.New("upstream returned 502"), false},
        {nil, false},
    }
    for _, c := range cases {
        got := isContextLengthError(c.err)
        if got != c.want {
            if c.err != nil {
                t.Errorf("isContextLengthError(%q) = %v, want %v", c.err.Error(), got, c.want)
            } else {
                t.Errorf("isContextLengthError(nil) = %v, want %v", got, c.want)
            }
        }
    }
}
