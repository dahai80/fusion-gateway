package adapter

import (
    "context"
    "io"
    "net/http"
    "net/http/httptest"
    "strings"
    "testing"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

func azureTestBackendCfg(tsURL string) config.BackendConfig {
    return config.BackendConfig{
        Type:        "azure-openai",
        BaseURL:     tsURL,
        APIKey:      "az-secret",
        Enabled:     true,
        Models:      []string{"gpt-4o-exam"},
        APIVersion:  "2024-10-21",
        Deployments: map[string]string{"gpt-4o-exam": "exam-gpt4o"},
    }
}

// #324: chat must hit the deployment URL convention with api-version query
// and api-key header, and surface the effective model_version.
func TestAzureOpenAIProvider_ChatDeploymentURLAndModelVersion(t *testing.T) {
    var gotPath, gotQuery, gotAPIKey, gotAuth string
    ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        gotPath = r.URL.Path
        gotQuery = r.URL.RawQuery
        gotAPIKey = r.Header.Get("api-key")
        gotAuth = r.Header.Get("Authorization")
        w.WriteHeader(http.StatusOK)
        _, _ = io.WriteString(w, `{"id":"c1","object":"chat.completion","created":1,"model":"gpt-4o-2024-08-06","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`)
    }))
    defer ts.Close()

    p := NewAzureOpenAIProvider("azure", azureTestBackendCfg(ts.URL))
    resp, err := p.Chat(context.Background(), &ChatRequest{Model: "gpt-4o-exam", Messages: []ChatMessage{{Role: "user", Content: "hi"}}})
    if err != nil {
        t.Fatalf("chat failed: %v", err)
    }
    if gotPath != "/openai/deployments/exam-gpt4o/chat/completions" {
        t.Errorf("path = %q, want /openai/deployments/exam-gpt4o/chat/completions", gotPath)
    }
    if !strings.Contains(gotQuery, "api-version=2024-10-21") {
        t.Errorf("query = %q, want api-version=2024-10-21", gotQuery)
    }
    if gotAPIKey != "az-secret" {
        t.Errorf("api-key header = %q, want az-secret", gotAPIKey)
    }
    if gotAuth != "" {
        t.Errorf("Authorization must stay empty on azure-openai, got %q", gotAuth)
    }
    if resp.Model != "gpt-4o-exam" {
        t.Errorf("model = %q, want requested gpt-4o-exam", resp.Model)
    }
    if resp.ModelVersion != "gpt-4o-2024-08-06" {
        t.Errorf("model_version = %q, want gpt-4o-2024-08-06", resp.ModelVersion)
    }
}

// Unmapped model: the request model id itself is the deployment name.
func TestAzureOpenAIProvider_UnmappedModelUsesModelAsDeployment(t *testing.T) {
    var gotPath string
    ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        gotPath = r.URL.Path
        w.WriteHeader(http.StatusOK)
        _, _ = io.WriteString(w, `{"id":"c1","object":"chat.completion","created":1,"model":"gpt-4o-mini-2024-07-18","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
    }))
    defer ts.Close()

    p := NewAzureOpenAIProvider("azure", azureTestBackendCfg(ts.URL))
    _, err := p.Chat(context.Background(), &ChatRequest{Model: "my-deploy", Messages: []ChatMessage{{Role: "user", Content: "hi"}}})
    if err != nil {
        t.Fatalf("chat failed: %v", err)
    }
    if gotPath != "/openai/deployments/my-deploy/chat/completions" {
        t.Errorf("path = %q, want deployment named after model", gotPath)
    }
}

// Stream: every chunk carries model_version from the upstream model field,
// and Model stays the requested id.
func TestAzureOpenAIProvider_StreamStampsModelVersion(t *testing.T) {
    ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("Content-Type", "text/event-stream")
        _, _ = io.WriteString(w, "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"gpt-4o-2024-08-06\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"he\"}}]}\n\n")
        _, _ = io.WriteString(w, "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"gpt-4o-2024-08-06\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"llo\"}}]}\n\n")
        _, _ = io.WriteString(w, "data: [DONE]\n\n")
    }))
    defer ts.Close()

    p := NewAzureOpenAIProvider("azure", azureTestBackendCfg(ts.URL))
    ch, err := p.StreamChat(context.Background(), &ChatRequest{Model: "gpt-4o-exam", Messages: []ChatMessage{{Role: "user", Content: "hi"}}})
    if err != nil {
        t.Fatalf("stream failed: %v", err)
    }
    var n int
    for chunk := range ch {
        // #363 F3: the terminal [DONE] sentinel is a bare Raw frame with no
        // model fields — skip it, only real data chunks carry the stamp.
        if string(chunk.Raw) == "[DONE]" {
            continue
        }
        n++
        if chunk.ModelVersion != "gpt-4o-2024-08-06" {
            t.Errorf("chunk %d model_version = %q, want gpt-4o-2024-08-06", n, chunk.ModelVersion)
        }
        if chunk.Model != "gpt-4o-exam" {
            t.Errorf("chunk %d model = %q, want requested gpt-4o-exam", n, chunk.Model)
        }
    }
    if n < 2 {
        t.Errorf("expected >=2 chunks, got %d", n)
    }
}

// Factory registration: the azure-openai backend type must resolve.
func TestAzureOpenAIProvider_FactoryRegistered(t *testing.T) {
    _, err := LookupProviderFactory("azure-openai")
    if err != nil {
        t.Fatalf("azure-openai factory missing: %v", err)
    }
}

// Embedding goes to the deployment embeddings path.
func TestAzureOpenAIProvider_EmbeddingDeploymentPath(t *testing.T) {
    var gotPath string
    ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        gotPath = r.URL.Path
        w.WriteHeader(http.StatusOK)
        _, _ = io.WriteString(w, `{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2]}],"model":"text-embedding-3-small","usage":{"prompt_tokens":2,"total_tokens":2}}`)
    }))
    defer ts.Close()

    p := NewAzureOpenAIProvider("azure", azureTestBackendCfg(ts.URL))
    _, err := p.Embedding(context.Background(), &EmbeddingRequest{Model: "gpt-4o-exam", Input: []string{"hi"}})
    if err != nil {
        t.Fatalf("embedding failed: %v", err)
    }
    if gotPath != "/openai/deployments/exam-gpt4o/embeddings" {
        t.Errorf("path = %q, want /openai/deployments/exam-gpt4o/embeddings", gotPath)
    }
}

// HealthCheck fails without a key, succeeds against a 200 models endpoint.
func TestAzureOpenAIProvider_HealthCheck(t *testing.T) {
    p := NewAzureOpenAIProvider("azure", config.BackendConfig{Type: "azure-openai", BaseURL: "https://x", Enabled: true})
    if err := p.HealthCheck(context.Background()); err == nil {
        t.Fatal("health check must fail without api_key")
    }

    ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path != "/openai/models" {
            t.Errorf("health path = %q, want /openai/models", r.URL.Path)
        }
        w.WriteHeader(http.StatusOK)
    }))
    defer ts.Close()
    p2 := NewAzureOpenAIProvider("azure", azureTestBackendCfg(ts.URL))
    if err := p2.HealthCheck(context.Background()); err != nil {
        t.Errorf("health check should succeed: %v", err)
    }
}

// ListModels echoes the configured model list.
func TestAzureOpenAIProvider_ListModels(t *testing.T) {
    p := NewAzureOpenAIProvider("azure", azureTestBackendCfg("https://x"))
    models, err := p.ListModels(context.Background())
    if err != nil {
        t.Fatalf("list models failed: %v", err)
    }
    if len(models) != 1 || models[0].ID != "gpt-4o-exam" {
        t.Errorf("models = %+v, want [gpt-4o-exam]", models)
    }
}

// BaseURL with a trailing slash must not double-slash the deployment path.
func TestAzureOpenAIProvider_TrailingSlashBaseURL(t *testing.T) {
    var gotPath string
    ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        gotPath = r.URL.Path
        w.WriteHeader(http.StatusOK)
        _, _ = io.WriteString(w, `{"id":"c1","object":"chat.completion","created":1,"model":"gpt-4o-2024-08-06","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
    }))
    defer ts.Close()

    cfg := azureTestBackendCfg(ts.URL + "/")
    p := NewAzureOpenAIProvider("azure", cfg)
    _, err := p.Chat(context.Background(), &ChatRequest{Model: "gpt-4o-exam", Messages: []ChatMessage{{Role: "user", Content: "hi"}}})
    if err != nil {
        t.Fatalf("chat failed: %v", err)
    }
    if gotPath != "/openai/deployments/exam-gpt4o/chat/completions" {
        t.Errorf("path = %q, want clean deployment path", gotPath)
    }
}
