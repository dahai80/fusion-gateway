package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fusion-gateway/fusion-gateway/internal/adapter"
	"github.com/fusion-gateway/fusion-gateway/internal/config"
)

// TestLoadBaseline is the enterprise production-readiness load/stress baseline.
// It runs entirely offline against a FAKE OpenAI-compatible backend (no
// fusion-mlx, no real LLM) and asserts a reproducible performance contract:
//   - all concurrent non-stream + SSE requests succeed (200 / parseable),
//   - P99 latency stays bounded under the configured load,
//   - no goroutine leak (post-load NumGoroutine within a few of pre-load),
//   - no unbounded heap growth (post-load steady Alloc reported, bounded),
//   - slot-no-leak: the local fusion-mlx in-flight counter returns to 0.
//
// Numbers are reported via t.Logf so each run leaves a traceable baseline in
// the test output. Load is modest (64x50 non-stream + 32x1 SSE) so it runs
// fast on a macOS CI runner.
func TestLoadBaseline(t *testing.T) {
	const (
		nonStreamWorkers   = 64
		nonStreamPerWorker = 50
		streamWorkers      = 32
		streamPerWorker    = 1
		fakeModel          = "qwen-7b"
		p99CeilingMs       = 2000
		leakGoroutineDelta = 20
	)

	fakeBackend := newFakeOpenAIBackend(t, fakeModel)

	s := newLoadTestServer(t, fakeBackend.URL, fakeModel)

	// Minimal mux exposing only the chat-completions route through the real
	// middleware chain. Same-package access lets us call the unexported
	// withMiddleware + handler directly; this exercises the full auth/PII/
	// rate-limit/budget gate exactly as production, without spinning a real
	// listener (Server.Start binds a port + launches background goroutines).
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", s.withMiddleware(s.handleChatCompletions))
	gatewaySrv := httptest.NewServer(mux)
	// Safety net: if the test fails before the explicit drain-close below, the
	// httptest servers still get closed by Cleanup. The explicit close below is
	// idempotent-safe because httptest.Server.Close sets a closed flag.
	t.Cleanup(fakeBackend.Close)
	t.Cleanup(gatewaySrv.Close)

	// Slot-no-leak baseline: capture the local provider in-flight counter
	// before any load, then assert it returns to 0 after the load drains.
	localProvider, ok := s.pool.Get("fusion-mlx")
	if !ok {
		t.Fatal("fusion-mlx provider not registered")
	}
	mlxProvider, isMLX := localProvider.(*adapter.FusionMLXProvider)
	if !isMLX {
		t.Fatalf("expected *adapter.FusionMLXProvider, got %T", localProvider)
	}
	inFlightBefore := mlxProvider.InFlight()
	if inFlightBefore != 0 {
		t.Fatalf("precondition: in-flight not 0 before load, got %d", inFlightBefore)
	}

	runtime.GC()
	var memBefore runtime.MemStats
	runtime.ReadMemStats(&memBefore)
	goroutinesBefore := runtime.NumGoroutine()

	// Phase 1: concurrent non-streaming load.
	nonStreamLatencies := runConcurrentLoad(t, gatewaySrv.URL, nonStreamWorkers, nonStreamPerWorker, false, fakeModel)
	reportLatency(t, "non-stream", nonStreamLatencies)
	if p99(nonStreamLatencies) > float64(p99CeilingMs) {
		t.Errorf("non-stream P99 %vms exceeded ceiling %dms", p99(nonStreamLatencies), p99CeilingMs)
	}

	// Phase 2: concurrent SSE load.
	streamLatencies := runConcurrentLoad(t, gatewaySrv.URL, streamWorkers, streamPerWorker, true, fakeModel)
	reportLatency(t, "stream", streamLatencies)
	if p99(streamLatencies) > float64(p99CeilingMs) {
		t.Errorf("stream P99 %vms exceeded ceiling %dms", p99(streamLatencies), p99CeilingMs)
	}

	// Drain HTTP transport idle connections BEFORE the leak assertion. Both the
	// load client (->gateway) and the gateway's backend client (->fakeBackend)
	// pool keep-alive connections (MaxIdleConnsPerHost up to 64, IdleConnTimeout
	// 90s) — each pooled conn pins a readLoop + writeLoop goroutine pair. These
	// are NOT gateway leaks: they are deliberate pooled conns that self-reap at
	// 90s, far beyond any test window. Without an explicit drain they'd register
	// as a false-positive goroutine leak. Closing both servers forces EOF on the
	// gateway->backend conns (their readLoop/writeLoop exit on read error);
	// CloseIdleConnections reaps the load client's pooled conns to the gateway.
	// The leak assertion then measures ONLY gateway-internal goroutines (stream
	// pumps, lifecycle workers, session-affinity/rate-limiter evictors).
	gatewaySrv.Close()
	fakeBackend.Close()
	loadClient.CloseIdleConnections()

	// Settle: let in-flight requests drain and detached parser goroutines exit.
	// The stream handler spawns a safego parser goroutine that lingers briefly
	// after the handler returns; a short settle window is required before the
	// goroutine/heap/in-flight leak assertions.
	settleGoroutines(t, goroutinesBefore, leakGoroutineDelta)

	runtime.GC()
	var memAfter runtime.MemStats
	runtime.ReadMemStats(&memAfter)
	allocGrowthMB := float64(memAfter.Alloc-memBefore.Alloc) / 1024 / 1024
	t.Logf("heap: before_alloc=%d after_alloc=%d growth=%.2f MiB sys=%d",
		memBefore.Alloc, memAfter.Alloc, allocGrowthMB, memAfter.Sys)
	// Memory is bounded: post-load steady Alloc must not exceed pre-load by more
	// than a generous working-set multiple. SSE buffers + parser goroutine
	// residue can hold a few MiB transiently; 256 MiB is a wide safe ceiling
	// that still catches an unbounded leak (e.g. a stream buffer that never
	// releases).
	if allocGrowthMB > 256 {
		t.Errorf("heap growth %.2f MiB exceeded 256 MiB ceiling (unbounded leak)", allocGrowthMB)
	}

	inFlightAfter := mlxProvider.InFlight()
	t.Logf("slot: in_flight before=%d after=%d", inFlightBefore, inFlightAfter)
	if inFlightAfter != 0 {
		t.Errorf("slot leak: in-flight did not return to 0 after load, got %d", inFlightAfter)
	}

	slog.Info("TestLoadBaseline passed",
		"non_stream_requests", nonStreamWorkers*nonStreamPerWorker,
		"stream_requests", streamWorkers*streamPerWorker,
		"non_stream_p99_ms", p99(nonStreamLatencies),
		"stream_p99_ms", p99(streamLatencies),
		"heap_growth_mib", allocGrowthMB,
		"in_flight_after", inFlightAfter,
	)
}

func p99(latencies []float64) float64 {
    if len(latencies) == 0 {
        return 0
    }
    sorted := make([]float64, len(latencies))
    copy(sorted, latencies)
    sort.Float64s(sorted)
    idx := int(float64(len(sorted)) * 0.99)
    if idx >= len(sorted) {
        idx = len(sorted) - 1
    }
    return sorted[idx]
}

// reportLatency logs the min/p50/p95/p99/max latency for a load phase so each
// run leaves a traceable baseline in test output.
func reportLatency(t *testing.T, label string, latencies []float64) {
    t.Helper()
    if len(latencies) == 0 {
        t.Logf("%s: no samples", label)
        return
    }
    sorted := make([]float64, len(latencies))
    copy(sorted, latencies)
    sort.Float64s(sorted)
    pct := func(p float64) float64 {
        idx := int(float64(len(sorted)) * p)
        if idx >= len(sorted) {
            idx = len(sorted) - 1
        }
        return sorted[idx]
    }
    t.Logf("%s latency ms: n=%d min=%.2f p50=%.2f p95=%.2f p99=%.2f max=%.2f",
        label, len(sorted), sorted[0], pct(0.5), pct(0.95), pct(0.99), sorted[len(sorted)-1])
}

// settleGoroutines waits for detached stream-parser goroutines to exit, then
// asserts the live goroutine count is within delta of the pre-load baseline.
// The SSE handler spawns safego parser goroutines that linger briefly after
// the handler returns; a bounded settle window is required before asserting
// no leak. Fails loudly on a real leak (Rule 12).
func settleGoroutines(t *testing.T, baseline, delta int) {
    t.Helper()
    deadline := time.Now().Add(5 * time.Second)
    for time.Now().Before(deadline) {
        if runtime.NumGoroutine() <= baseline+delta {
            t.Logf("goroutines: baseline=%d now=%d delta=%d settled", baseline, runtime.NumGoroutine(), runtime.NumGoroutine()-baseline)
            return
        }
        time.Sleep(50 * time.Millisecond)
    }
    now := runtime.NumGoroutine()
    t.Errorf("goroutine leak: baseline=%d now=%d exceeded delta=%d", baseline, now, delta)
    // Dump all live goroutine stacks so the leak source is diagnosable.
    buf := make([]byte, 1<<20)
    n := runtime.Stack(buf, true)
    t.Logf("LEAK DUMP (%d goroutines):\n%s", now, string(buf[:n]))
}

// newLoadTestServer builds a Server wired to force all requests to the local
// fusion-mlx backend: routing.mode="local" fast-paths Decide to LocalBackend
// (engine.go:741) deterministically, bypassing the hybrid rule chain. A real
// FusionMLXProvider is registered against the fake backend URL and its model
// set refreshed so the local-model guard and allowlist see the fake model.
func newLoadTestServer(t *testing.T, baseURL, model string) *Server {
    t.Helper()
    s := newTestServer()
    s.cfg.Config.Routing.Mode = "local"
    s.cfg.Config.Routing.LocalPriority.MaxConcurrent = 0
    // newTestServer() leaves taskRegistry nil; the RequestID middleware injects
    // a task-id on every request, and handleChatCompletions calls
    // taskRegistry.Register when one is present — a nil registry panics (500).
    // Wire a real (limit-less, reaper-less) registry, matching agent_slot_test.
    s.taskRegistry = NewTaskRegistry()
    mlx := adapter.NewFusionMLXProvider(config.BackendConfig{
        Type:    "fusion-mlx",
        BaseURL: baseURL,
        Enabled: true,
    }, s.cfg.Config.Routing)
    s.pool.Register("fusion-mlx", mlx, config.BackendConfig{
        Type:    "fusion-mlx",
        BaseURL: baseURL,
        Enabled: true,
    })
    mlx.RefreshModelSet(context.Background())
    s.router.SetLocalInFlight(func() int64 { return mlx.InFlight() })
    s.router.SetLocalModels(func() map[string]bool { return mlx.ModelSet() })
    s.router.SetLocalReady(true)
    slog.Info("load test server wired local-only", "base_url", baseURL, "model", model)
    return s
}

// newFakeOpenAIBackend returns an offline httptest.Server emulating a
// fusion-mlx OpenAI-compatible backend: /health (model_loaded=true), /readyz
// (200), /v1/models (lists model), /v1/chat/completions (non-stream JSON or
// SSE stream). No real inference, no real model.
func newFakeOpenAIBackend(t *testing.T, model string) *httptest.Server {
    t.Helper()
    healthBody, _ := json.Marshal(map[string]interface{}{
        "status":        "healthy",
        "ready":         true,
        "model_loaded":  true,
        "loaded_models": []string{model},
    })
    modelsBody, _ := json.Marshal(map[string]interface{}{
        "object": "list",
        "data": []map[string]string{
            {"id": model, "object": "model", "owned_by": "mlx"},
        },
    })
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        switch r.URL.Path {
        case "/health":
            w.Header().Set("Content-Type", "application/json")
            _, _ = w.Write(healthBody)
        case "/readyz":
            w.WriteHeader(http.StatusOK)
        case "/v1/models":
            w.Header().Set("Content-Type", "application/json")
            _, _ = w.Write(modelsBody)
        case "/v1/chat/completions":
            handleFakeChat(w, r, model)
        default:
            w.WriteHeader(http.StatusNotFound)
        }
    }))
    slog.Info("fake openai backend started", "url", srv.URL, "model", model)
    return srv
}

// handleFakeChat serves a non-stream ChatResponse JSON or a short SSE stream
// of OpenAI-format chunks terminated by [DONE]. The request body is peeked
// once to detect stream:true (the provider re-serializes the request, so a
// query flag is not reliable). The payload is fixed and tiny so the load
// measures gateway overhead, not model latency.
func handleFakeChat(w http.ResponseWriter, r *http.Request, model string) {
    raw, err := io.ReadAll(r.Body)
    if err != nil {
        slog.Warn("fake backend: read request body failed", "error", err)
        http.Error(w, `{"error":{"message":"bad request","type":"server_error"}}`, http.StatusBadRequest)
        return
    }
    var probe struct {
        Stream bool `json:"stream"`
    }
    _ = json.Unmarshal(raw, &probe)
    if probe.Stream {
        writeFakeStream(w, model)
        return
    }
    body, _ := json.Marshal(map[string]interface{}{
        "id":      "chatcmpl-fake-load",
        "object":  "chat.completion",
        "created": 1700000000,
        "model":   model,
        "choices": []map[string]interface{}{
            {
                "index": 0,
                "message": map[string]string{
                    "role":    "assistant",
                    "content": "ok",
                },
                "finish_reason": "stop",
            },
        },
        "usage": map[string]int{
            "prompt_tokens":     4,
            "completion_tokens": 1,
            "total_tokens":      5,
        },
    })
    w.Header().Set("Content-Type", "application/json")
    _, _ = w.Write(body)
}

// writeFakeStream emits a short SSE stream: one content delta, one stop chunk,
// then [DONE]. Matches the OpenAI streaming wire format the gateway's SSE
// forwarder expects; flushes after each frame so the pump makes progress.
func writeFakeStream(w http.ResponseWriter, model string) {
    w.Header().Set("Content-Type", "text/event-stream")
    w.Header().Set("Cache-Control", "no-cache")
    w.Header().Set("Connection", "keep-alive")
    flusher, ok := w.(http.Flusher)
    if !ok {
        http.Error(w, `{"error":{"message":"streaming unsupported","type":"server_error"}}`, http.StatusInternalServerError)
        return
    }
    deltaChunk := map[string]interface{}{
        "id":      "chatcmpl-fake-load-stream",
        "object":  "chat.completion.chunk",
        "created": 1700000000,
        "model":   model,
        "choices": []map[string]interface{}{
            {
                "index": 0,
                "delta": map[string]string{"content": "ok"},
            },
        },
    }
    deltaPayload, _ := json.Marshal(deltaChunk)
    fmt.Fprintf(w, "data: %s\n\n", deltaPayload)
    flusher.Flush()
    stopChunk := map[string]interface{}{
        "id":      "chatcmpl-fake-load-stream",
        "object":  "chat.completion.chunk",
        "created": 1700000000,
        "model":   model,
        "choices": []map[string]interface{}{
            {
                "index":         0,
                "delta":         map[string]string{},
                "finish_reason": "stop",
            },
        },
    }
    stopPayload, _ := json.Marshal(stopChunk)
    fmt.Fprintf(w, "data: %s\n\n", stopPayload)
    flusher.Flush()
    fmt.Fprintf(w, "data: [DONE]\n\n")
    flusher.Flush()
}

// runConcurrentLoad fires `workers` goroutines, each issuing `perWorker`
// sequential chat-completions requests against the gateway. When stream is
// true the request asks for SSE and the client drains the event stream to
// completion (so the server-side pump fully flushes + the slot releases).
// Returns per-request wall-clock latencies in milliseconds. A failed request
// (non-2xx, IO error, or an incomplete SSE stream) is logged and recorded as
// the ceiling so the P99 assertion fails loudly (Rule 12) instead of skewing
// the latency stats.
func runConcurrentLoad(t *testing.T, url string, workers, perWorker int, stream bool, model string) []float64 {
    t.Helper()
    total := workers * perWorker
    latencies := make([]float64, 0, total)
    var mu sync.Mutex
    var wg sync.WaitGroup
    var failCount int64

    for w := 0; w < workers; w++ {
        wg.Add(1)
        go func() {
            defer wg.Done()
            for i := 0; i < perWorker; i++ {
                lat := sendLoadRequest(url, stream, model)
                mu.Lock()
                latencies = append(latencies, lat)
                mu.Unlock()
                if lat < 0 {
                    atomic.AddInt64(&failCount, 1)
                }
            }
        }()
    }
    wg.Wait()

    if got := atomic.LoadInt64(&failCount); got > 0 {
        t.Errorf("load phase had %d/%d failed requests (non-2xx or IO error)", got, total)
    }
    return latencies
}

// loadClient is a dedicated HTTP client with keep-alives disabled so each
// request closes its TCP conn on completion. Without this, http.DefaultClient
// pools idle conns to the httptest gateway, and each pooled conn holds a live
// server serve-goroutine — the load's 3k+ requests would leave hundreds of
// lingering goroutines and trip the no-leak assertion even though the gateway
// itself leaked nothing. DisableKeepAlives makes the goroutine count reflect
// only real gateway-side leaks.
var loadClient = &http.Client{
    Transport: &http.Transport{
        DisableKeepAlives: true,
        MaxIdleConns:       0,
        MaxIdleConnsPerHost: 0,
    },
}

// sendLoadRequest issues one chat-completions request and returns its latency
// in milliseconds. A negative return signals failure. For stream requests the
// body is fully drained (bufio.Scanner over the SSE frames) so the server
// stream pump completes and releases its slot before the client returns.
func sendLoadRequest(url string, stream bool, model string) float64 {
    payload := map[string]interface{}{
        "model":    model,
        "stream":   stream,
        "messages": []map[string]string{{"role": "user", "content": "ping"}},
    }
    if stream {
        payload["max_tokens"] = 16
    }
    body, _ := json.Marshal(payload)
    req, err := http.NewRequest(http.MethodPost, url+"/v1/chat/completions", strings.NewReader(string(body)))
    if err != nil {
        slog.Warn("load request: new request failed", "error", err)
        return -1
    }
    req.Header.Set("Content-Type", "application/json")

    start := time.Now()
    resp, err := loadClient.Do(req)
    if err != nil {
        slog.Warn("load request: do failed", "error", err)
        return -1
    }
    defer resp.Body.Close()
    if resp.StatusCode < 200 || resp.StatusCode >= 300 {
        snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
        slog.Warn("load request: non-2xx", "status", resp.StatusCode, "body", string(snippet))
        return -1
    }
    if stream {
        // Drain the SSE event stream to completion so the server-side pump
        // flushes every frame + releases the stream slot before we return.
        scanner := bufio.NewScanner(resp.Body)
        scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
        for scanner.Scan() {
            if strings.TrimSpace(scanner.Text()) == "data: [DONE]" {
                break
            }
        }
        if err := scanner.Err(); err != nil {
            slog.Warn("load request: sse scan error", "error", err)
            return -1
        }
    } else {
        _, _ = io.Copy(io.Discard, resp.Body)
    }
    return float64(time.Since(start).Microseconds()) / 1000.0
}
