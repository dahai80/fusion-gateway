package adapter

import (
    "context"
    "io"
    "net/http"
    "net/http/httptest"
    "testing"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

// #700: base_url is the vendor-documented versioned prefix. The admin-UI
// presets end in /v1 (zhipu: /api/paas/v4), so building a provider from a
// preset must NOT double-append /v1 upstream. Table = the issue's
// reproduction matrix: configured base_url -> upstream request path.

func chatPath700(t *testing.T, mk func(base string) Provider) string {
    t.Helper()
    var got string
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        b, _ := io.ReadAll(r.Body)
        _ = b
        got = r.URL.Path
        w.Header().Set("Content-Type", "application/json")
        _, _ = w.Write([]byte(`{"id":"x","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
    }))
    defer srv.Close()
    p := mk(srv.URL)
    if _, err := p.Chat(context.Background(), &ChatRequest{Model: "m", Messages: []ChatMessage{{Role: "user", Content: "hi"}}}); err != nil {
        t.Fatalf("chat: %v", err)
    }
    return got
}

func bc700(base string) config.BackendConfig {
    return config.BackendConfig{Type: "openai-compatible", Enabled: true, BaseURL: base, APIKey: "k"}
}

func Test700_PresetBaseURLsProduceVersionedUpstreamPaths(t *testing.T) {
    cases := []struct {
        name string
        mk   func(base string) Provider
        want string
    }{
        // deepseek preset .../v1 (admin UI + vendorDefaults): no double /v1.
        {"deepseek_preset", func(b string) Provider { return NewDeepSeekProvider("t", bc700(b+"/v1")) }, "/v1/chat/completions"},
        // Legacy config without /v1 still works.
        {"deepseek_legacy", func(b string) Provider { return NewDeepSeekProvider("t", bc700(b)) }, "/v1/chat/completions"},
        {"deepseek_trailing_slash", func(b string) Provider { return NewDeepSeekProvider("t", bc700(b+"/v1/")) }, "/v1/chat/completions"},
        // zhipu preset .../api/paas/v4: previously produced /api/paas/v4/v1/...
        {"zhipu_preset", func(b string) Provider { return NewZhipuProvider("t", bc700(b+"/api/paas/v4")) }, "/api/paas/v4/chat/completions"},
        {"zhipu_bare_host", func(b string) Provider { return NewZhipuProvider("t", bc700(b)) }, "/api/paas/v4/chat/completions"},
        // openai-compatible preset .../v1.
        {"openai_compatible_preset", func(b string) Provider { return NewOpenAICompatibleProvider("t", bc700(b+"/v1")) }, "/v1/chat/completions"},
        // openrouter preset .../api/v1.
        {"openrouter_preset", func(b string) Provider { return NewOpenRouterProvider("t", bc700(b+"/api/v1")) }, "/api/v1/chat/completions"},
        // volcengine ark .../api/v3.
        {"volcengine_preset", func(b string) Provider { return NewVolcengineProvider("t", bc700(b+"/api/v3")) }, "/api/v3/chat/completions"},
        // dashscope compatible-mode/v1.
        {"dashscope_preset", func(b string) Provider { return NewDashScopeProvider("t", bc700(b+"/compatible-mode/v1")) }, "/compatible-mode/v1/chat/completions"},
    }
    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            got := chatPath700(t, tc.mk)
            if got != tc.want {
                t.Fatalf("upstream path %s, want %s", got, tc.want)
            }
        })
    }
}

func Test700_NonChatEndpointsStayUnversionedSuffixes(t *testing.T) {
    var paths []string
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        paths = append(paths, r.URL.Path)
        w.Header().Set("Content-Type", "application/json")
        switch r.URL.Path {
        case "/v1/models":
            _, _ = w.Write([]byte(`{"data":[]}`))
        case "/v1/embeddings":
            _, _ = w.Write([]byte(`{"data":[],"model":"m","usage":{"prompt_tokens":0,"total_tokens":0}}`))
        default:
            _, _ = w.Write([]byte(`{}`))
        }
    }))
    defer srv.Close()

    p := NewOpenAICompatibleProvider("t", bc700(srv.URL+"/v1"))
    if _, err := p.ListModels(context.Background()); err != nil {
        t.Fatalf("list models: %v", err)
    }
    if _, err := p.Embedding(context.Background(), &EmbeddingRequest{Model: "m", Input: []string{"x"}}); err != nil {
        t.Fatalf("embedding: %v", err)
    }
    if len(paths) != 2 || paths[0] != "/v1/models" || paths[1] != "/v1/embeddings" {
        t.Fatalf("upstream paths %v", paths)
    }
}

func Test700_NormalizeVersionedBaseURL(t *testing.T) {
    cases := []struct{ in, vp, want string }{
        {"https://api.deepseek.com/v1", "/v1", "https://api.deepseek.com/v1"},
        {"https://api.deepseek.com/v1/", "/v1", "https://api.deepseek.com/v1"},
        {"https://api.deepseek.com", "/v1", "https://api.deepseek.com/v1"},
        {"https://open.bigmodel.cn/api/paas/v4", "/api/paas/v4", "https://open.bigmodel.cn/api/paas/v4"},
        {"https://open.bigmodel.cn", "/api/paas/v4", "https://open.bigmodel.cn/api/paas/v4"},
        {"https://ark.cn-beijing.volces.com/api/v3", "/api/v3", "https://ark.cn-beijing.volces.com/api/v3"},
        {"", "/v1", ""},
    }
    for _, tc := range cases {
        if got := normalizeVersionedBaseURL(tc.in, tc.vp); got != tc.want {
            t.Fatalf("normalize(%q, %q) = %q, want %q", tc.in, tc.vp, got, tc.want)
        }
    }
}
