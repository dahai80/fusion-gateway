// #326: usage cost alerting worker. Periodically sums per-(app, scenario,
// model) cost over each configured threshold's trailing window via the
// store's usage aggregation and POSTs a JSON alert to the configured webhook
// on breach. Per-threshold cooldown equals the threshold window: a breach
// refires only after one full window of still-breaching traffic, so a
// steady over-budget stream alerts once per window, not once per tick.
package usagealerts

import (
    "bytes"
    "context"
    "encoding/json"
    "fmt"
    "log/slog"
    "net/http"
    "sync"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/httpx"
    "github.com/fusion-gateway/fusion-gateway/internal/lifecycle"
    "github.com/fusion-gateway/fusion-gateway/internal/store"
)

// Client is the store dependency (narrowed for tests).
type Client interface {
    GetUsageAggregate(from, to time.Time, app, scenario, model string) ([]*store.UsageAggItem, error)
}

// Worker is the background usage-alerting loop.
type Worker struct {
    cfg     config.UsageAlertsConfig
    store   Client
    webhook string
    worker  *lifecycle.Worker
    client  *http.Client

    mu       sync.Mutex
    // lastFired tracks the last webhook fire time per threshold index; the
    // cooldown is the threshold's own window.
    lastFired map[int]time.Time
}

// Start launches the alerting loop. cfg must already be normalized
// (config.NormalizeUsageAlerts); enabled=false or empty webhook returns nil.
func Start(parent context.Context, cfg config.UsageAlertsConfig, st Client) *Worker {
    cfg, _ = config.NormalizeUsageAlerts(cfg)
    if !cfg.Enabled || cfg.WebhookURL == "" || len(cfg.Thresholds) == 0 {
        return nil
    }
    w := &Worker{
        cfg:     cfg,
        store:   st,
        webhook: cfg.WebhookURL,
        // Through httpx.TransportForBackend (RR11/RR5): MaxConnsPerHost cap +
        // header/dial timeouts — never a bare &http.Client{}.
        client:    &http.Client{Transport: httpx.TransportForBackend(config.BackendConfig{Type: "openai-compatible", BaseURL: cfg.WebhookURL})},
        lastFired: make(map[int]time.Time),
    }
    w.worker = lifecycle.Start(parent, "usage-alerts", w.loop)
    slog.Info("usage alerts worker started",
        "interval", cfg.CheckInterval.String(), "thresholds", len(cfg.Thresholds))
    return w
}

// Stop joins the worker.
func (w *Worker) Stop() {
    if w.worker != nil {
        w.worker.Stop()
    }
}

func (w *Worker) loop(ctx context.Context) {
    ticker := time.NewTicker(w.cfg.CheckInterval)
    defer ticker.Stop()
    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            w.runOnce(ctx, time.Now())
        }
    }
}

// runOnce evaluates every threshold once. Split out from loop so tests can
// drive ticks with a fixed clock.
func (w *Worker) runOnce(ctx context.Context, now time.Time) {
    for i, t := range w.cfg.Thresholds {
        w.checkThreshold(ctx, i, t, now)
    }
}

func (w *Worker) checkThreshold(ctx context.Context, idx int, t config.UsageAlertThreshold, now time.Time) {
    from := now.Add(-t.Window)
    rows, err := w.store.GetUsageAggregate(from, now, t.App, t.Scenario, t.Model)
    if err != nil {
        slog.Warn("usage alerts: aggregation failed",
            "app", t.App, "scenario", t.Scenario, "model", t.Model, "error", err)
        return
    }
    var cost float64
    for _, r := range rows {
        cost += r.CostUSD
    }
    if cost <= t.MaxCostUSD {
        return
    }

    w.mu.Lock()
    last, fired := w.lastFired[idx]
    if fired && now.Sub(last) < t.Window {
        w.mu.Unlock()
        slog.Debug("usage alerts: breach suppressed by cooldown",
            "app", t.App, "scenario", t.Scenario, "cost_usd", cost)
        return
    }
    w.lastFired[idx] = now
    w.mu.Unlock()

    if err := w.fireWebhook(ctx, t, cost, now); err != nil {
        slog.Error("usage alerts: webhook post failed",
            "webhook", w.webhook, "error", err)
    }
}

type alertPayload struct {
    Type         string    `json:"type"`
    App          string    `json:"app"`
    Scenario     string    `json:"scenario"`
    Model        string    `json:"model"`
    WindowHours  float64   `json:"window_hours"`
    CostUSD      float64   `json:"cost_usd"`
    ThresholdUSD float64   `json:"threshold_usd"`
    TriggeredAt  time.Time `json:"triggered_at"`
}

func (w *Worker) fireWebhook(ctx context.Context, t config.UsageAlertThreshold, cost float64, now time.Time) error {
    payload := alertPayload{
        Type:         "usage_alert",
        App:          t.App,
        Scenario:     t.Scenario,
        Model:        t.Model,
        WindowHours:  t.Window.Hours(),
        CostUSD:      cost,
        ThresholdUSD: t.MaxCostUSD,
        TriggeredAt:  now,
    }
    body, err := json.Marshal(payload)
    if err != nil {
        return fmt.Errorf("marshal alert: %w", err)
    }
    req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.webhook, bytes.NewReader(body))
    if err != nil {
        return fmt.Errorf("build alert request: %w", err)
    }
    req.Header.Set("Content-Type", "application/json")
    resp, err := w.client.Do(req)
    if err != nil {
        return err
    }
    defer resp.Body.Close()
    if resp.StatusCode >= 300 {
        return fmt.Errorf("alert webhook returned %d", resp.StatusCode)
    }
    slog.Warn("usage alert fired",
        "app", t.App, "scenario", t.Scenario, "model", t.Model,
        "cost_usd", cost, "threshold_usd", t.MaxCostUSD,
        "window", t.Window.String())
    return nil
}
