package adapter

import (
    "context"
    "net/http"
    "sync/atomic"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

// EI7 guard: proves the converted vendor shims route StreamChat through the
// baseOpenAICompatible baseStream — which carries the RR8 ctx-watcher. Before
// EI7 each shim had its own standalone StreamChat with NO watcher, so a stalled
// upstream (body.Read blocked, ctx only checked on the send arm) leaked the
// goroutine + connection + slot indefinitely.
//
// This drives a shim's StreamChat with a fake RoundTripper returning a
// hangingReader body — NOT Go's real transport — so the test isolates the
// watcher from transport-level ctx propagation (which would mask the gap). On
// the BUG (standalone shim, no watcher) the pump blocks in body.Read forever
// after cancel and this test times out. On the FIX (baseStream watcher) cancel
// closes the body, Read errors, the pump exits, the channel closes.
//
// Run against deepseek as the representative converted shim — they all share
// baseStream, so one proves the wiring for the class.

type ei7FakeTransport struct {
    body *hangingReader
}

func (t *ei7FakeTransport) RoundTrip(*http.Request) (*http.Response, error) {
    return &http.Response{
        StatusCode: http.StatusOK,
        Header:     make(http.Header),
        Body:       t.body,
    }, nil
}

func newEI7ShimWithFakeBody(t *testing.T) (*DeepSeekProvider, *hangingReader) {
    t.Helper()
    body := &hangingReader{unblock: make(chan struct{})}
    // Build the shim directly (same package) so we control the clients —
    // newBaseOpenAICompatible builds a real TransportForBackend transport,
    // which we must replace to isolate the watcher from Go's transport ctx.
    // R3 split baseStream onto streamHTTPClient, so the fake transport must
    // be wired on BOTH clients (stream path uses streamHTTPClient).
    fake := &ei7FakeTransport{body: body}
    p := &DeepSeekProvider{
        baseOpenAICompatible: baseOpenAICompatible{
            name:             "deepseek",
            baseURL:          "http://deepseek.test",
            apiKey:           "k",
            httpClient:       &http.Client{Transport: fake},
            streamHTTPClient: &http.Client{Transport: fake},
        },
    }
    return p, body
}

func TestEI7_ShimStreamChatWatcherUnblocksOnCancel(t *testing.T) {
    p, _ := newEI7ShimWithFakeBody(t)

    ctx, cancel := context.WithCancel(context.Background())
    ch, err := p.StreamChat(ctx, &ChatRequest{})
    if err != nil {
        t.Fatalf("StreamChat returned error: %v", err)
    }

    // Pump goroutine is now blocked in the hangingReader Read. ctx.Done() is
    // only checked on the send arm (never reached). Without a watcher this
    // blocks forever — the RR8/EI7 gap.
    time.Sleep(50 * time.Millisecond)

    cancel() // watcher should close the body → Read errors → pump exits

    select {
    case _, ok := <-ch:
        if ok {
            t.Fatal("channel should be closed after pump exit")
        }
        // pump exited, channel closed — watcher worked
    case <-time.After(3 * time.Second):
        t.Fatal("EI7: shim StreamChat pump did not exit within 3s of cancel — baseStream ctx-watcher missing (RR8 leak persists)")
    }
}

// TestEI7_AllBearerShimsEmbedBase asserts every converted Bearer-auth shim
// embeds baseOpenAICompatible (the EI7 refactor's structural invariant). If a
// shim is later rewritten as standalone, the type assertion fails — surfacing
// a regression to the copy-paste structure that reopens the horizontal-fix
// leak (RR8/RR11/RR9/B6 would again need per-shim patches).
func TestEI7_AllBearerShimsEmbedBase(t *testing.T) {
    cfg := config.BackendConfig{Type: "openai-compatible", BaseURL: "http://x", APIKey: "k"}
    shims := []Provider{
        NewDeepSeekProvider("deepseek", cfg),
        NewMoonshotProvider("moonshot", cfg),
        NewBaichuanProvider("baichuan", cfg),
        NewDashScopeProvider("dashscope", cfg),
        NewHunyuanProvider("hunyuan", cfg),
        NewMinimaxProvider("minimax", cfg),
        NewZhipuProvider("zhipu", cfg),
        NewStepFunProvider("stepfun", cfg),
        NewYiProvider("yi", cfg),
    }
    for _, s := range shims {
        if _, ok := s.(interface {
            baseName() string
            baseClient() *http.Client
        }); !ok {
            t.Errorf("EI7: %s does not embed baseOpenAICompatible — standalone copy-paste restored", s.Name())
        }
    }
}

// TestEI7_QianfanCustomAuthPreserved asserts qianfan keeps its accessToken
// fallback (prefers Bearer accessToken over apiKey) after the base conversion.
func TestEI7_QianfanCustomAuthPreserved(t *testing.T) {
    cfg := config.BackendConfig{Type: "qianfan", BaseURL: "http://x", APIKey: "raw-key"}
    qp := NewQianfanProvider("qianfan", cfg)
    if qp.accessToken != "" {
        t.Fatalf("accessToken must start empty, got %q", qp.accessToken)
    }
    // No accessToken set → falls through to apiKey Bearer.
    req, _ := http.NewRequest(http.MethodGet, "http://x", nil)
    qp.setAuth(req)
    if got := req.Header.Get("Authorization"); got != "Bearer raw-key" {
        t.Fatalf("apiKey fallback Bearer = %q, want %q", got, "Bearer raw-key")
    }
    // With accessToken set → uses it.
    qp.accessToken = "tok-123"
    req2, _ := http.NewRequest(http.MethodGet, "http://x", nil)
    qp.setAuth(req2)
    if got := req2.Header.Get("Authorization"); got != "Bearer tok-123" {
        t.Fatalf("accessToken Bearer = %q, want %q", got, "Bearer tok-123")
    }
}

var _ = atomic.Bool{}
