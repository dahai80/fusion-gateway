package adapter

import (
    "context"
    "io"
    "log/slog"
    "net"
    "net/http"
    "net/http/httptest"
    "strings"
    "sync/atomic"
    "testing"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

// fakeAdaptersPayload mirrors fusion-mlx FineTuneService.list_adapters() output:
// a bare JSON array of dicts (no envelope).
const fakeAdaptersPayload = `[
    {"adapter_name":"lora-code","model_id":"qwen2.5-coder-7b","adapter_path":"/adapters/qwen2.5-coder-7b/lora-code","has_weights":true,"has_config":true,"lora_rank":8},
    {"adapter_name":"lora-sql","model_id":"qwen2.5-coder-7b","adapter_path":"/adapters/qwen2.5-coder-7b/lora-sql","has_weights":true,"has_config":true,"lora_rank":16}
]`

// newAdapterIndexServer stands up an httptest server emulating the fusion-mlx
// /admin/api/fine-tune/adapters endpoint. It records request count and the last
// Authorization header so tests can assert the index sends the Bearer key.
func newAdapterIndexServer(t *testing.T, status int, body string) (*httptest.Server, *atomic.Int64, *atomic.Value) {
    t.Helper()
    var calls atomic.Int64
    var lastAuth atomic.Value
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        calls.Add(1)
        lastAuth.Store(r.Header.Get("Authorization"))
        if r.URL.Path != "/admin/api/fine-tune/adapters" {
            t.Errorf("unexpected path: %s", r.URL.Path)
            http.Error(w, "not found", http.StatusNotFound)
            return
        }
        w.Header().Set("Content-Type", "application/json")
        w.WriteHeader(status)
        io.WriteString(w, body)
    }))
    t.Cleanup(srv.Close)
    return srv, &calls, &lastAuth
}

func TestAdapterIndex_RefreshAndList(t *testing.T) {
    srv, calls, lastAuth := newAdapterIndexServer(t, http.StatusOK, fakeAdaptersPayload)
    cfg := config.BackendConfig{BaseURL: srv.URL, APIKey: "test-key"}

    idx := NewAdapterIndex(cfg)
    if err := idx.Refresh(context.Background()); err != nil {
        t.Fatalf("Refresh: %v", err)
    }

    if calls.Load() != 1 {
        t.Errorf("expected 1 fetch, got %d", calls.Load())
    }
    if got := lastAuth.Load().(string); got != "Bearer test-key" {
        t.Errorf("Authorization: want Bearer test-key, got %q", got)
    }

    list := idx.List()
    if len(list) != 2 {
        t.Fatalf("List len: want 2, got %d", len(list))
    }
    if list[0].AdapterName != "lora-code" || list[0].LoraRank != 8 {
        t.Errorf("first entry: %+v", list[0])
    }
    if list[1].ModelID != "qwen2.5-coder-7b" {
        t.Errorf("second entry model_id: %s", list[1].ModelID)
    }
    if list[0].AdapterPath != "/adapters/qwen2.5-coder-7b/lora-code" {
        t.Errorf("first entry adapter_path: %s", list[0].AdapterPath)
    }

    if !idx.Has("lora-code") {
        t.Error("Has(lora-code): want true")
    }
    if !idx.Has("lora-sql") {
        t.Error("Has(lora-sql): want true")
    }
    if idx.Has("missing") {
        t.Error("Has(missing): want false")
    }
    if idx.Has("") {
        t.Error("Has(empty): want false")
    }

    if path, ok := idx.Path("lora-code"); !ok || path != "/adapters/qwen2.5-coder-7b/lora-code" {
        t.Errorf("Path(lora-code): want (/adapters/qwen2.5-coder-7b/lora-code, true), got (%q, %v)", path, ok)
    }
    if path, ok := idx.Path("lora-sql"); !ok || path != "/adapters/qwen2.5-coder-7b/lora-sql" {
        t.Errorf("Path(lora-sql): want (/adapters/qwen2.5-coder-7b/lora-sql, true), got (%q, %v)", path, ok)
    }
    if _, ok := idx.Path("missing"); ok {
        t.Error("Path(missing): want false")
    }
    if _, ok := idx.Path(""); ok {
        t.Error("Path(empty): want false")
    }

    if idx.LastError() != nil {
        t.Errorf("LastError after success: want nil, got %v", idx.LastError())
    }
    if idx.RefreshedAt().IsZero() {
        t.Error("RefreshedAt should be non-zero after success")
    }
}

func TestAdapterIndex_RefreshEmpty(t *testing.T) {
    srv, _, _ := newAdapterIndexServer(t, http.StatusOK, `[]`)
    idx := NewAdapterIndex(config.BackendConfig{BaseURL: srv.URL})

    if err := idx.Refresh(context.Background()); err != nil {
        t.Fatalf("Refresh empty: %v", err)
    }
    if got := idx.List(); got != nil {
        t.Errorf("List on empty: want nil, got %v", got)
    }
    if idx.Has("anything") {
        t.Error("Has on empty index should be false")
    }
}

func TestAdapterIndex_RefreshNon200(t *testing.T) {
    srv, _, _ := newAdapterIndexServer(t, http.StatusUnauthorized, `{"detail":"unauthorized"}`)
    idx := NewAdapterIndex(config.BackendConfig{BaseURL: srv.URL, APIKey: "bad"})

    err := idx.Refresh(context.Background())
    if err == nil {
        t.Fatal("Refresh on 401 should error")
    }
    if !strings.Contains(err.Error(), "unexpected status") {
        t.Errorf("error should mention unexpected status, got: %v", err)
    }
    if idx.LastError() == nil {
        t.Error("LastError should be set after failure")
    }
    if got := idx.List(); got != nil {
        t.Errorf("List after failed refresh should be nil, got %v", got)
    }
}

func TestAdapterIndex_RefreshBadJSON(t *testing.T) {
    srv, _, _ := newAdapterIndexServer(t, http.StatusOK, `{not json}`)
    idx := NewAdapterIndex(config.BackendConfig{BaseURL: srv.URL})

    if err := idx.Refresh(context.Background()); err == nil {
        t.Fatal("Refresh on bad JSON should error")
    }
    if idx.LastError() == nil {
        t.Error("LastError should be set after decode failure")
    }
}

func TestAdapterIndex_RefreshConnError(t *testing.T) {
    idx := NewAdapterIndex(config.BackendConfig{BaseURL: "http://127.0.0.1:1"})
    if err := idx.Refresh(context.Background()); err == nil {
        t.Fatal("Refresh on unreachable host should error")
    }
    if idx.LastError() == nil {
        t.Error("LastError should be set after conn error")
    }
}

func TestAdapterIndex_EmptyBaseURLNoop(t *testing.T) {
    idx := NewAdapterIndex(config.BackendConfig{})
    if err := idx.Refresh(context.Background()); err != nil {
        t.Fatalf("Refresh with empty base_url should be no-op, got %v", err)
    }
    if idx.LastError() != nil {
        t.Errorf("LastError should stay nil on no-op, got %v", idx.LastError())
    }
}

func TestAdapterIndex_ListIsCopy(t *testing.T) {
    srv, _, _ := newAdapterIndexServer(t, http.StatusOK, fakeAdaptersPayload)
    idx := NewAdapterIndex(config.BackendConfig{BaseURL: srv.URL})
    if err := idx.Refresh(context.Background()); err != nil {
        t.Fatalf("Refresh: %v", err)
    }

    a := idx.List()
    a[0].AdapterName = "mutated"
    if idx.Has("mutated") {
        t.Error("mutating returned slice should not affect the index")
    }
    if !idx.Has("lora-code") {
        t.Error("original adapter name should still be present")
    }
}

// TestAdapterIndex_RefreshOverUDS asserts the index fetch rides the outbound
// UDS transport when SocketPath is set — the same zero-copy path as inference
// traffic. Stands up a hand-started http.Server on a unix listener (emulates
// fusion-mlx --host unix:), then points the index at it via a dummy base_url.
func TestAdapterIndex_RefreshOverUDS(t *testing.T) {
    sock := udsSocketPath(t)

    ln, err := net.Listen("unix", sock)
    if err != nil {
        t.Fatalf("listen unix: %v", err)
    }
    defer ln.Close()

    srv := &http.Server{
        Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            if r.URL.Path != "/admin/api/fine-tune/adapters" {
                t.Errorf("unexpected path: %s", r.URL.Path)
            }
            w.Header().Set("Content-Type", "application/json")
            io.WriteString(w, fakeAdaptersPayload)
        }),
    }
    go srv.Serve(ln)
    defer srv.Close()

    idx := NewAdapterIndex(config.BackendConfig{
        BaseURL:    "http://unix/",
        SocketPath: sock,
    })
    if err := idx.Refresh(context.Background()); err != nil {
        t.Fatalf("Refresh over UDS: %v", err)
    }
    if !idx.Has("lora-code") {
        t.Error("Has(lora-code) over UDS: want true")
    }
}

// TestAdapterIndex_RepeatedRefreshStaysConsistent asserts a second successful
// refresh fully replaces the snapshot (no stale append) and clears lastErr.
func TestAdapterIndex_RepeatedRefreshStaysConsistent(t *testing.T) {
    var body atomic.Value
    body.Store(fakeAdaptersPayload)
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("Content-Type", "application/json")
        io.WriteString(w, body.Load().(string))
    }))
    t.Cleanup(srv.Close)

    idx := NewAdapterIndex(config.BackendConfig{BaseURL: srv.URL})
    if err := idx.Refresh(context.Background()); err != nil {
        t.Fatalf("first Refresh: %v", err)
    }
    if len(idx.List()) != 2 {
        t.Fatalf("after first refresh: want 2, got %d", len(idx.List()))
    }

    body.Store(`[{"adapter_name":"only-lora","model_id":"m","has_weights":true,"has_config":true,"lora_rank":4}]`)
    if err := idx.Refresh(context.Background()); err != nil {
        t.Fatalf("second Refresh: %v", err)
    }
    list := idx.List()
    if len(list) != 1 || list[0].AdapterName != "only-lora" {
        t.Fatalf("after second refresh: want [only-lora], got %v", list)
    }
    if idx.Has("lora-code") {
        t.Error("stale adapter should be gone after re-refresh")
    }
}

func init() {
    slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
}
