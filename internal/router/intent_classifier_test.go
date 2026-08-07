package router

import (
    "context"
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

func newTestClassifierServer(t *testing.T, label string, status int) (*httptest.Server, *classifyRequest) {
    t.Helper()
    var captured classifyRequest
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path != "/v1/chat/completions" {
            http.NotFound(w, r)
            return
        }
        _ = json.NewDecoder(r.Body).Decode(&captured)
        if status != http.StatusOK {
            http.Error(w, "boom", status)
            return
        }
        w.Header().Set("Content-Type", "application/json")
        _ = json.NewEncoder(w).Encode(classifyResponse{
            Choices: []struct {
                Message struct {
                    Content string `json:"content"`
                } `json:"message"`
            }{{Message: struct {
                Content string `json:"content"`
            }{Content: label}}},
        })
    }))
    t.Cleanup(srv.Close)
    return srv, &captured
}

func TestNewRouterLightClassifier_MissingBaseModel(t *testing.T) {
    c := NewRouterLightClassifier(config.IntentClassifierConfig{
        Enabled:   true,
        Endpoint:  "http://127.0.0.1:11434",
        BaseModel: "",
    })
    if c != nil {
        t.Fatalf("expected nil classifier when base_model missing, got %+v", c)
    }
}

func TestNewRouterLightClassifier_ModelAliasOverridesBase(t *testing.T) {
    c := NewRouterLightClassifier(config.IntentClassifierConfig{
        Enabled:   true,
        Model:     "alias-model",
        BaseModel: "base-model",
    })
    if c == nil || c.baseModel != "alias-model" {
        t.Fatalf("expected Model alias to override BaseModel, got %+v", c)
    }
}

func TestRouterLightClassifier_KnownLabel(t *testing.T) {
    srv, captured := newTestClassifierServer(t, "code", http.StatusOK)
    c := NewRouterLightClassifier(config.IntentClassifierConfig{
        Enabled:   true,
        Endpoint:  srv.URL,
        BaseModel: "mlx-community/Llama-3.2-1B-Instruct-4bit",
        Adapter:   "/tmp/adapter",
        Timeout:   time.Second,
    })
    res, err := c.Classify(context.Background(), &RouteRequest{Model: "m", Text: "write a quicksort"})
    if err != nil {
        t.Fatalf("classify errored: %v", err)
    }
    if res.Intent != IntentLightweight {
        t.Fatalf("expected lightweight intent for code label, got %s", res.Intent)
    }
    if res.Confidence != 1.0 {
        t.Fatalf("expected confidence 1.0, got %f", res.Confidence)
    }
    if res.Params["task_type"] != "code" {
        t.Fatalf("expected task_type=code, got %q", res.Params["task_type"])
    }
    if captured.Adapters != "/tmp/adapter" {
        t.Fatalf("expected adapters=/tmp/adapter, got %q", captured.Adapters)
    }
    if captured.Temperature != 0.0 {
        t.Fatalf("expected greedy temperature 0, got %f", captured.Temperature)
    }
    if captured.Stream {
        t.Fatalf("expected non-streaming classify request")
    }
    if len(captured.Messages) != 1 || captured.Messages[0].Role != "user" {
        t.Fatalf("expected single user message, got %+v", captured.Messages)
    }
    if captured.Messages[0].Content == "" {
        t.Fatalf("expected non-empty classify prompt")
    }
}

func TestRouterLightClassifier_NormalizesLabel(t *testing.T) {
    cases := []string{"Code", "code.", "code\n", "Intent: chat", "  MATH  ", "translate."}
    for _, label := range cases {
        srv, _ := newTestClassifierServer(t, label, http.StatusOK)
        c := NewRouterLightClassifier(config.IntentClassifierConfig{
            Enabled:   true,
            Endpoint:  srv.URL,
            BaseModel: "base",
            Timeout:   time.Second,
        })
        res, err := c.Classify(context.Background(), &RouteRequest{Model: "m", Text: "q"})
        if err != nil {
            t.Fatalf("label %q: classify errored: %v", label, err)
        }
        if res.Intent != IntentLightweight {
            t.Fatalf("label %q: expected lightweight, got %s", label, res.Intent)
        }
    }
}

func TestRouterLightClassifier_UnknownLabel(t *testing.T) {
    srv, _ := newTestClassifierServer(t, "weather", http.StatusOK)
    c := NewRouterLightClassifier(config.IntentClassifierConfig{
        Enabled:   true,
        Endpoint:  srv.URL,
        BaseModel: "base",
        Timeout:   time.Second,
    })
    res, err := c.Classify(context.Background(), &RouteRequest{Model: "m", Text: "q"})
    if err != nil {
        t.Fatalf("classify errored: %v", err)
    }
    if res.Intent != IntentUnknown {
        t.Fatalf("expected unknown for unrecognized label, got %s", res.Intent)
    }
    if res.Confidence != 0 {
        t.Fatalf("expected confidence 0 for unknown, got %f", res.Confidence)
    }
}

func TestRouterLightClassifier_EmptyTextSkipsModel(t *testing.T) {
    srv, _ := newTestClassifierServer(t, "code", http.StatusOK)
    c := NewRouterLightClassifier(config.IntentClassifierConfig{
        Enabled:   true,
        Endpoint:  srv.URL,
        BaseModel: "base",
        Timeout:   time.Second,
    })
    res, err := c.Classify(context.Background(), &RouteRequest{Model: "m", Text: "   "})
    if err != nil {
        t.Fatalf("classify errored: %v", err)
    }
    if res.Intent != IntentUnknown {
        t.Fatalf("expected unknown for empty text, got %s", res.Intent)
    }
}

func TestRouterLightClassifier_HttpErrorFailsOpen(t *testing.T) {
    srv, _ := newTestClassifierServer(t, "", http.StatusInternalServerError)
    c := NewRouterLightClassifier(config.IntentClassifierConfig{
        Enabled:   true,
        Endpoint:  srv.URL,
        BaseModel: "base",
        Timeout:   time.Second,
    })
    _, err := c.Classify(context.Background(), &RouteRequest{Model: "m", Text: "q"})
    if err == nil {
        t.Fatalf("expected error on HTTP 500 so classifyAndLog fails open")
    }
}

func TestRouterLightClassifier_TimeoutFailsOpen(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        time.Sleep(200 * time.Millisecond)
        w.WriteHeader(http.StatusOK)
    }))
    t.Cleanup(srv.Close)
    c := NewRouterLightClassifier(config.IntentClassifierConfig{
        Enabled:   true,
        Endpoint:  srv.URL,
        BaseModel: "base",
        Timeout:   50 * time.Millisecond,
    })
    _, err := c.Classify(context.Background(), &RouteRequest{Model: "m", Text: "q"})
    if err == nil {
        t.Fatalf("expected timeout error so classifyAndLog fails open to rule chain")
    }
}

func TestRouterLightClassifier_NoChoices(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("Content-Type", "application/json")
        _ = json.NewEncoder(w).Encode(classifyResponse{})
    }))
    t.Cleanup(srv.Close)
    c := NewRouterLightClassifier(config.IntentClassifierConfig{
        Enabled:   true,
        Endpoint:  srv.URL,
        BaseModel: "base",
        Timeout:   time.Second,
    })
    res, err := c.Classify(context.Background(), &RouteRequest{Model: "m", Text: "q"})
    if err != nil {
        t.Fatalf("classify errored: %v", err)
    }
    if res.Intent != IntentUnknown {
        t.Fatalf("expected unknown when no choices, got %s", res.Intent)
    }
}

func TestNormalizeLabel(t *testing.T) {
    cases := map[string]string{
        "Code":         "code",
        "code.":        "code",
        "code\n":       "code",
        "Intent: chat": "chat",
        "  MATH  ":     "math",
        "translate。":   "translate",
        "summary：":     "summary",
    }
    for in, want := range cases {
        if got := normalizeLabel(in); got != want {
            t.Fatalf("normalizeLabel(%q) = %q, want %q", in, got, want)
        }
    }
}
