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

// newLayaTestServer returns an httptest server that responds to
// /v1/laya/decide with the given answers JSON.
func newLayaTestServer(t *testing.T, answersJSON string, status int) *httptest.Server {
    t.Helper()
    return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path != "/v1/laya/decide" {
            w.WriteHeader(http.StatusNotFound)
            return
        }
        w.Header().Set("Content-Type", "application/json")
        w.WriteHeader(status)
        _, _ = w.Write([]byte(answersJSON))
    }))
}

func newLayaClassifierForTest(endpoint string) *LayaClassifier {
    return NewLayaClassifier(config.IntentClassifierConfig{
        Enabled:       true,
        Type:          "laya",
        Endpoint:      endpoint,
        Timeout:       100 * time.Millisecond,
        MinConfidence: 0.7,
    })
}

// #191: domain=code → IntentLightweight.
func TestLayaClassifierDomainCode(t *testing.T) {
    resp := `{"answers":{"domain":{"type":"choice","choice":"code","confidence":0.95},"difficulty":{"type":"score","score":1.5,"confidence":0.9},"needs_tools":{"type":"noul","noul":0.1,"confidence":0.9},"is_sensitive":{"type":"noul","noul":0.05,"confidence":0.9}},"usage":{"input_tokens":128,"latency_ms":8.3}}`
    srv := newLayaTestServer(t, resp, http.StatusOK)
    defer srv.Close()

    c := newLayaClassifierForTest(srv.URL)
    res, err := c.Classify(context.Background(), &RouteRequest{Text: "write a function"})
    if err != nil {
        t.Fatalf("classify: %v", err)
    }
    if res.Intent != IntentLightweight {
        t.Fatalf("expected IntentLightweight, got %s", res.Intent)
    }
    if res.Confidence != 0.95 {
        t.Fatalf("expected confidence 0.95, got %f", res.Confidence)
    }
    if res.Params["domain"] != "code" {
        t.Fatalf("expected domain=code, got %s", res.Params["domain"])
    }
    if res.Params["_source"] != "laya" {
        t.Fatalf("expected _source=laya, got %s", res.Params["_source"])
    }
}

// #191: domain=writing → IntentHeavyModel.
func TestLayaClassifierDomainWriting(t *testing.T) {
    resp := `{"answers":{"domain":{"type":"choice","choice":"writing","confidence":0.88},"difficulty":{"type":"score","score":2.0,"confidence":0.85},"needs_tools":{"type":"noul","noul":0.0,"confidence":0.9},"is_sensitive":{"type":"noul","noul":0.0,"confidence":0.9}},"usage":{"input_tokens":128,"latency_ms":7.1}}`
    srv := newLayaTestServer(t, resp, http.StatusOK)
    defer srv.Close()

    c := newLayaClassifierForTest(srv.URL)
    res, err := c.Classify(context.Background(), &RouteRequest{Text: "write an essay"})
    if err != nil {
        t.Fatalf("classify: %v", err)
    }
    if res.Intent != IntentHeavyModel {
        t.Fatalf("expected IntentHeavyModel, got %s", res.Intent)
    }
}

// #191: high difficulty (>=3.5) overrides domain → IntentHeavyModel.
func TestLayaClassifierDifficultyOverride(t *testing.T) {
    resp := `{"answers":{"domain":{"type":"choice","choice":"chitchat","confidence":0.95},"difficulty":{"type":"score","score":4.0,"confidence":0.9},"needs_tools":{"type":"noul","noul":0.0,"confidence":0.9},"is_sensitive":{"type":"noul","noul":0.0,"confidence":0.9}},"usage":{"input_tokens":128,"latency_ms":8.0}}`
    srv := newLayaTestServer(t, resp, http.StatusOK)
    defer srv.Close()

    c := newLayaClassifierForTest(srv.URL)
    res, err := c.Classify(context.Background(), &RouteRequest{Text: "hi"})
    if err != nil {
        t.Fatalf("classify: %v", err)
    }
    if res.Intent != IntentHeavyModel {
        t.Fatalf("expected IntentHeavyModel (difficulty override), got %s", res.Intent)
    }
    if res.Params["difficulty_override"] != "heavy_model" {
        t.Fatal("expected difficulty_override flag")
    }
}

// #191: all 6 laya domains map correctly.
func TestLayaClassifierDomainMapping(t *testing.T) {
    cases := []struct {
        domain   string
        expected Intent
    }{
        {"code", IntentLightweight},
        {"math_or_logic", IntentLightweight},
        {"writing", IntentHeavyModel},
        {"factual_lookup", IntentLightweight},
        {"data_analysis", IntentLightweight},
        {"chitchat", IntentLightweight},
    }
    for _, tc := range cases {
        t.Run(tc.domain, func(t *testing.T) {
            resp := `{"answers":{"domain":{"type":"choice","choice":"` + tc.domain + `","confidence":0.9},"difficulty":{"type":"score","score":1.0,"confidence":0.85},"needs_tools":{"type":"noul","noul":0.0,"confidence":0.9},"is_sensitive":{"type":"noul","noul":0.0,"confidence":0.9}},"usage":{"input_tokens":64,"latency_ms":7.0}}`
            srv := newLayaTestServer(t, resp, http.StatusOK)
            defer srv.Close()

            c := newLayaClassifierForTest(srv.URL)
            res, err := c.Classify(context.Background(), &RouteRequest{Text: "test"})
            if err != nil {
                t.Fatalf("classify: %v", err)
            }
            if res.Intent != tc.expected {
                t.Fatalf("domain %s: expected %s, got %s", tc.domain, tc.expected, res.Intent)
            }
        })
    }
}

// #191: low confidence (< min_confidence) → fallback to RouterLight if set.
func TestLayaClassifierLowConfidenceFallback(t *testing.T) {
    resp := `{"answers":{"domain":{"type":"choice","choice":"code","confidence":0.3},"difficulty":{"type":"score","score":1.0,"confidence":0.3},"needs_tools":{"type":"noul","noul":0.0,"confidence":0.9},"is_sensitive":{"type":"noul","noul":0.0,"confidence":0.9}},"usage":{"input_tokens":64,"latency_ms":7.0}}`
    srv := newLayaTestServer(t, resp, http.StatusOK)
    defer srv.Close()

    c := newLayaClassifierForTest(srv.URL)
    c.SetFallback(NoopClassifier{})
    res, err := c.Classify(context.Background(), &RouteRequest{Text: "test"})
    if err != nil {
        t.Fatalf("classify: %v", err)
    }
    // NoopClassifier returns IntentUnknown
    if res.Intent != IntentUnknown {
        t.Fatalf("expected IntentUnknown from fallback, got %s", res.Intent)
    }
    if res.Params["_source"] != "laya_fallback" {
        t.Fatalf("expected _source=laya_fallback, got %s", res.Params["_source"])
    }
}

// #191: laya endpoint returns 503 → fallback.
func TestLayaClassifierEndpoint503Fallback(t *testing.T) {
    srv := newLayaTestServer(t, `{"error":"laya model not loaded"}`, http.StatusServiceUnavailable)
    defer srv.Close()

    c := newLayaClassifierForTest(srv.URL)
    c.SetFallback(NoopClassifier{})
    res, err := c.Classify(context.Background(), &RouteRequest{Text: "test"})
    if err != nil {
        t.Fatalf("classify should not error on fallback: %v", err)
    }
    if res.Intent != IntentUnknown {
        t.Fatalf("expected IntentUnknown from fallback, got %s", res.Intent)
    }
    if res.Params["_source"] != "laya_fallback" {
        t.Fatalf("expected _source=laya_fallback, got %s", res.Params["_source"])
    }
}

// #191: laya endpoint timeout → fallback. Uses a server that sleeps longer
// than the classifier timeout (50ms default).
func TestLayaClassifierTimeoutFallback(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        time.Sleep(200 * time.Millisecond)
        w.WriteHeader(http.StatusOK)
    }))
    defer srv.Close()

    c := newLayaClassifierForTest(srv.URL)
    c.SetFallback(NoopClassifier{})
    res, err := c.Classify(context.Background(), &RouteRequest{Text: "test"})
    if err != nil {
        t.Fatalf("classify should not error on timeout fallback: %v", err)
    }
    if res.Intent != IntentUnknown {
        t.Fatalf("expected IntentUnknown from timeout fallback, got %s", res.Intent)
    }
}

// #191: no fallback set → IntentUnknown on error.
func TestLayaClassifierNoFallback(t *testing.T) {
    srv := newLayaTestServer(t, `{"error":"not loaded"}`, http.StatusServiceUnavailable)
    defer srv.Close()

    c := newLayaClassifierForTest(srv.URL)
    // no SetFallback
    res, err := c.Classify(context.Background(), &RouteRequest{Text: "test"})
    if err != nil {
        t.Fatalf("classify should not error: %v", err)
    }
    if res.Intent != IntentUnknown {
        t.Fatalf("expected IntentUnknown, got %s", res.Intent)
    }
}

// #191: empty query → IntentUnknown without HTTP call.
func TestLayaClassifierEmptyQuery(t *testing.T) {
    srv := newLayaTestServer(t, `{}`, http.StatusOK)
    defer srv.Close()

    c := newLayaClassifierForTest(srv.URL)
    res, err := c.Classify(context.Background(), &RouteRequest{Text: ""})
    if err != nil {
        t.Fatalf("classify: %v", err)
    }
    if res.Intent != IntentUnknown {
        t.Fatalf("expected IntentUnknown for empty query, got %s", res.Intent)
    }
}

// #191: needs_tools > 0.5 on unknown domain → IntentLightweight.
func TestLayaClassifierNeedsToolsFlag(t *testing.T) {
    resp := `{"answers":{"domain":{"type":"choice","choice":"unknown_domain","confidence":0.9},"difficulty":{"type":"score","score":1.0,"confidence":0.85},"needs_tools":{"type":"noul","noul":0.8,"confidence":0.9},"is_sensitive":{"type":"noul","noul":0.0,"confidence":0.9}},"usage":{"input_tokens":64,"latency_ms":7.0}}`
    srv := newLayaTestServer(t, resp, http.StatusOK)
    defer srv.Close()

    c := newLayaClassifierForTest(srv.URL)
    res, err := c.Classify(context.Background(), &RouteRequest{Text: "test"})
    if err != nil {
        t.Fatalf("classify: %v", err)
    }
    // Confidence 0.9 >= min 0.7, domain unknown but needs_tools flags lightweight
    if res.Intent != IntentLightweight {
        t.Fatalf("expected IntentLightweight (needs_tools), got %s", res.Intent)
    }
    if res.Params["needs_tools_flag"] != "true" {
        t.Fatal("expected needs_tools_flag=true")
    }
}

// #191: NewLayaClassifier defaults endpoint + timeout when config empty.
func TestNewLayaClassifierDefaults(t *testing.T) {
    c := NewLayaClassifier(config.IntentClassifierConfig{})
    if c == nil {
        t.Fatal("expected non-nil classifier")
    }
    if c.decideURL != "http://127.0.0.1:11434/v1/laya/decide" {
        t.Fatalf("unexpected decideURL: %s", c.decideURL)
    }
    if c.minConfidence != 0.7 {
        t.Fatalf("expected default minConfidence 0.7, got %f", c.minConfidence)
    }
}

// #191: when intent_classifier.api_key is empty, NewLayaClassifier falls back
// to FUSION_MLX_API_KEY env so laya auth works without a redundant config
// field (same pattern as the fusion-mlx backend).
func TestNewLayaClassifierApiKeyEnvFallback(t *testing.T) {
    t.Setenv("FUSION_MLX_API_KEY", "fg-test-key-123")
    c := NewLayaClassifier(config.IntentClassifierConfig{})
    if c.apiKey != "fg-test-key-123" {
        t.Fatalf("expected apiKey from FUSION_MLX_API_KEY env, got %q", c.apiKey)
    }
    // Explicit config api_key wins over env.
    c2 := NewLayaClassifier(config.IntentClassifierConfig{APIKey: "explicit-key"})
    if c2.apiKey != "explicit-key" {
        t.Fatalf("explicit config api_key should win, got %q", c2.apiKey)
    }
}

// #191: wireIntentClassifier type=laya installs LayaClassifier with fallback.
func TestWireIntentClassifierLayaType(t *testing.T) {
    cfg := config.IntentClassifierConfig{
        Enabled:  true,
        Type:     "laya",
        Endpoint: "http://127.0.0.1:11434",
        BaseModel: "mlx-community/Llama-3.2-1B-Instruct-4bit",
    }
    // We can't call wireIntentClassifier directly (it needs *router.Engine),
    // but we can verify the construction path: NewLayaClassifier + fallback.
    laya := NewLayaClassifier(cfg)
    if laya == nil {
        t.Fatal("expected non-nil laya classifier")
    }
    rl := NewRouterLightClassifier(cfg)
    if rl != nil {
        laya.SetFallback(rl)
    }
    // Verify it implements IntentClassifier
    var _ IntentClassifier = laya
}

// #191: response JSON parses correctly.
func TestLayaResponseParsing(t *testing.T) {
    raw := `{"answers":{"domain":{"type":"choice","choice":"math_or_logic","confidence":0.92},"difficulty":{"type":"score","score":2.5,"confidence":0.88},"needs_tools":{"type":"noul","noul":0.15,"confidence":0.92},"is_sensitive":{"type":"noul","noul":0.02,"confidence":0.95}},"usage":{"input_tokens":128,"state_tokens":64,"latency_ms":8.3}}`
    var lr layaDecideResponse
    if err := json.Unmarshal([]byte(raw), &lr); err != nil {
        t.Fatalf("unmarshal: %v", err)
    }
    if lr.Answers.Domain.Choice != "math_or_logic" {
        t.Fatalf("expected domain=math_or_logic, got %s", lr.Answers.Domain.Choice)
    }
    if lr.Answers.Difficulty.Score != 2.5 {
        t.Fatalf("expected difficulty=2.5, got %f", lr.Answers.Difficulty.Score)
    }
    if lr.Answers.NeedsTools.Noul != 0.15 {
        t.Fatalf("expected needs_tools=0.15, got %f", lr.Answers.NeedsTools.Noul)
    }
}

// #191: HTTP request construction — correct URL path, body fields, preset.
func TestLayaClassifierRequestConstruction(t *testing.T) {
    var capturedBody layaDecideRequest
    var capturedPath string
    var capturedMethod string
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        capturedPath = r.URL.Path
        capturedMethod = r.Method
        _ = json.NewDecoder(r.Body).Decode(&capturedBody)
        w.Header().Set("Content-Type", "application/json")
        _ = json.NewEncoder(w).Encode(layaDecideResponse{
            Answers: layaAnswers{
                Domain:     layaChoiceAnswer{Choice: "code", Confidence: 0.9},
                Difficulty: layaScoreAnswer{Score: 1.0, Confidence: 0.85},
                NeedsTools: layaNoulAnswer{Noul: 0.0, Confidence: 0.9},
                IsSensitive: layaNoulAnswer{Noul: 0.0, Confidence: 0.9},
            },
        })
    }))
    defer srv.Close()

    c := newLayaClassifierForTest(srv.URL)
    _, err := c.Classify(context.Background(), &RouteRequest{Text: "write a sort function"})
    if err != nil {
        t.Fatalf("classify: %v", err)
    }
    if capturedMethod != http.MethodPost {
        t.Fatalf("expected POST, got %s", capturedMethod)
    }
    if capturedPath != "/v1/laya/decide" {
        t.Fatalf("expected path /v1/laya/decide, got %s", capturedPath)
    }
    if capturedBody.Preset != "router" {
        t.Fatalf("expected preset=router, got %s", capturedBody.Preset)
    }
    if capturedBody.Prompt != "write a sort function" {
        t.Fatalf("expected prompt='write a sort function', got %s", capturedBody.Prompt)
    }
}

// #191: backward compat — RouterLightClassifier still works unchanged.
func TestLayaClassifier_BackwardCompatRouterLight(t *testing.T) {
    cfg := config.IntentClassifierConfig{
        Enabled:       true,
        Type:          "router_light",
        Endpoint:      "",
        BaseModel:     "mlx-community/Llama-3.2-1B-Instruct-4bit",
        Timeout:       100 * time.Millisecond,
        MinConfidence: 0.7,
    }
    c := NewRouterLightClassifier(cfg)
    if c == nil {
        t.Fatal("expected non-nil RouterLightClassifier")
    }
    var _ IntentClassifier = c
}
