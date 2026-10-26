package config

import (
    "testing"
    "time"
)

// #326: usage_alerts normalization — defaults, invalid-entry dropping,
// enabled-without-webhook warning.
func TestNormalizeUsageAlerts(t *testing.T) {
    cfg, warns := NormalizeUsageAlerts(UsageAlertsConfig{
        Enabled: true,
        Thresholds: []UsageAlertThreshold{
            {App: "app-a", Window: time.Hour, MaxCostUSD: 1.0},
            {App: "app-b", MaxCostUSD: 2.0},                            // missing window
            {App: "app-c", Window: time.Hour},                          // missing max_cost_usd
            {App: "app-d", Window: 30 * time.Minute, MaxCostUSD: 0.5},  // valid
        },
    })
    if cfg.CheckInterval != 5*time.Minute {
        t.Errorf("expected default 5m interval, got %v", cfg.CheckInterval)
    }
    if len(cfg.Thresholds) != 2 {
        t.Fatalf("expected 2 valid thresholds, got %d", len(cfg.Thresholds))
    }
    if cfg.Thresholds[0].App != "app-a" || cfg.Thresholds[1].App != "app-d" {
        t.Errorf("unexpected surviving thresholds: %+v", cfg.Thresholds)
    }
    if len(warns) != 3 {
        t.Errorf("expected 3 warns (2 invalid + webhook missing), got %d: %v", len(warns), warns)
    }

    // Disabled with no webhook: only the invalid-entry warns remain.
    cfg, warns = NormalizeUsageAlerts(UsageAlertsConfig{
        Thresholds: []UsageAlertThreshold{{App: "x", MaxCostUSD: 1}},
    })
    if len(cfg.Thresholds) != 0 {
        t.Errorf("expected 0 thresholds, got %d", len(cfg.Thresholds))
    }
    if len(warns) != 1 {
        t.Errorf("expected 1 warn, got %d: %v", len(warns), warns)
    }
}
