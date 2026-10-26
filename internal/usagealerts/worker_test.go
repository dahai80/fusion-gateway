package usagealerts

import (
    "context"
    "encoding/json"
    "io"
    "net/http"
    "net/http/httptest"
    "sync"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/store"
)

type fakeAggStore struct {
    mu   sync.Mutex
    rows []*store.UsageAggItem
    err  error
}

func (f *fakeAggStore) GetUsageAggregate(from, to time.Time, app, scenario, model string) ([]*store.UsageAggItem, error) {
    f.mu.Lock()
    defer f.mu.Unlock()
    return f.rows, f.err
}

func (f *fakeAggStore) setRows(rows []*store.UsageAggItem) {
    f.mu.Lock()
    defer f.mu.Unlock()
    f.rows = rows
}

type recordingWebhook struct {
    mu     sync.Mutex
    alerts []map[string]interface{}
    code   int
}

func (rw *recordingWebhook) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    body, _ := io.ReadAll(r.Body)
    var m map[string]interface{}
    _ = json.Unmarshal(body, &m)
    rw.mu.Lock()
    rw.alerts = append(rw.alerts, m)
    code := rw.code
    rw.mu.Unlock()
    if code == 0 {
        code = http.StatusOK
    }
    w.WriteHeader(code)
}

func (rw *recordingWebhook) count() int {
    rw.mu.Lock()
    defer rw.mu.Unlock()
    return len(rw.alerts)
}

func newTestWorker(t *testing.T, st Client, webhook string) *Worker {
    t.Helper()
    cfg := config.UsageAlertsConfig{
        Enabled:       true,
        WebhookURL:    webhook,
        CheckInterval: time.Hour,
        Thresholds: []config.UsageAlertThreshold{
            {App: "app-a", Scenario: "question-gen", Window: time.Hour, MaxCostUSD: 1.0},
        },
    }
    w := &Worker{
        cfg:       cfg,
        store:     st,
        webhook:   webhook,
        client:    http.DefaultClient,
        lastFired: make(map[int]time.Time),
    }
    return w
}

// Breach fires the webhook with the correct payload fields.
func TestWorker_BreachFires(t *testing.T) {
    wh := &recordingWebhook{}
    srv := httptest.NewServer(wh)
    defer srv.Close()

    st := &fakeAggStore{rows: []*store.UsageAggItem{
        {App: "app-a", Scenario: "question-gen", Model: "glm-5", CostUSD: 0.80},
        {App: "app-a", Scenario: "question-gen", Model: "other", CostUSD: 0.40},
    }}
    w := newTestWorker(t, st, srv.URL)

    now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
    w.runOnce(context.Background(), now)

    if wh.count() != 1 {
        t.Fatalf("expected 1 alert, got %d", wh.count())
    }
    a := wh.alerts[0]
    if a["type"] != "usage_alert" || a["app"] != "app-a" || a["scenario"] != "question-gen" {
        t.Errorf("unexpected alert identity: %v", a)
    }
    if a["cost_usd"].(float64) < 1.19 || a["cost_usd"].(float64) > 1.21 {
        t.Errorf("expected cost ~1.2, got %v", a["cost_usd"])
    }
    if a["threshold_usd"].(float64) != 1.0 {
        t.Errorf("expected threshold 1.0, got %v", a["threshold_usd"])
    }
    if a["window_hours"].(float64) != 1.0 {
        t.Errorf("expected window_hours 1.0, got %v", a["window_hours"])
    }
}

// Under-threshold traffic never fires.
func TestWorker_UnderThresholdNoFire(t *testing.T) {
    wh := &recordingWebhook{}
    srv := httptest.NewServer(wh)
    defer srv.Close()

    st := &fakeAggStore{rows: []*store.UsageAggItem{{CostUSD: 0.99}}}
    w := newTestWorker(t, st, srv.URL)

    w.runOnce(context.Background(), time.Now())
    if wh.count() != 0 {
        t.Fatalf("expected 0 alerts, got %d", wh.count())
    }
}

// Cooldown suppresses refires within the window; after the window a
// still-breaching stream alerts again.
func TestWorker_CooldownWindow(t *testing.T) {
    wh := &recordingWebhook{}
    srv := httptest.NewServer(wh)
    defer srv.Close()

    st := &fakeAggStore{rows: []*store.UsageAggItem{{CostUSD: 5.0}}}
    w := newTestWorker(t, st, srv.URL)

    t0 := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
    w.runOnce(context.Background(), t0)
    // 30 minutes later: still within the 1h window — suppressed.
    w.runOnce(context.Background(), t0.Add(30*time.Minute))
    if wh.count() != 1 {
        t.Fatalf("expected 1 alert after cooldown suppression, got %d", wh.count())
    }
    // 1 hour later: past the window — refires.
    w.runOnce(context.Background(), t0.Add(time.Hour))
    if wh.count() != 2 {
        t.Fatalf("expected 2 alerts after window elapsed, got %d", wh.count())
    }
}

// Store error must not fire nor crash.
func TestWorker_StoreErrorNoFire(t *testing.T) {
    wh := &recordingWebhook{}
    srv := httptest.NewServer(wh)
    defer srv.Close()

    st := &fakeAggStore{err: context.DeadlineExceeded}
    w := newTestWorker(t, st, srv.URL)

    w.runOnce(context.Background(), time.Now())
    if wh.count() != 0 {
        t.Fatalf("expected 0 alerts on store error, got %d", wh.count())
    }
}

// Start returns nil when disabled or misconfigured.
func TestStart_DisabledReturnsNil(t *testing.T) {
    if w := Start(context.Background(), config.UsageAlertsConfig{}, &fakeAggStore{}); w != nil {
        t.Error("expected nil worker for disabled config")
    }
    if w := Start(context.Background(), config.UsageAlertsConfig{Enabled: true, WebhookURL: "http://x"}, &fakeAggStore{}); w != nil {
        t.Error("expected nil worker with no thresholds")
    }
    if w := Start(context.Background(), config.UsageAlertsConfig{Enabled: true, Thresholds: []config.UsageAlertThreshold{{Window: time.Hour, MaxCostUSD: 1}}}, &fakeAggStore{}); w != nil {
        t.Error("expected nil worker without webhook_url")
    }
}
