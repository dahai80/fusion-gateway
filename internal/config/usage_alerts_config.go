package config

import "time"

// UsageAlertsConfig is the usage_alerts.* namespace (#326): periodic
// per-(app, scenario, model) cost checks against configured thresholds with
// webhook notification on breach. Default off — no worker, no webhook calls.
type UsageAlertsConfig struct {
    Enabled bool `mapstructure:"enabled"`
    // WebhookURL receives the breach JSON POST. Required when enabled.
    WebhookURL string `mapstructure:"webhook_url"`
    // CheckInterval is the polling cadence. Default 5m.
    CheckInterval time.Duration `mapstructure:"check_interval"`
    // Thresholds are the cost ceilings to evaluate each tick. Each entry
    // sums CostUSD over its trailing Window via the store's usage
    // aggregation and fires the webhook when the sum exceeds MaxCostUSD.
    Thresholds []UsageAlertThreshold `mapstructure:"thresholds"`
}

// UsageAlertThreshold is one cost ceiling. App is the API key name and
// Scenario the X-Fusion-Scenario label; empty means "any" on that dimension.
type UsageAlertThreshold struct {
    App        string        `mapstructure:"app"`
    Scenario   string        `mapstructure:"scenario"`
    Model      string        `mapstructure:"model"`
    Window     time.Duration `mapstructure:"window"`
    MaxCostUSD float64       `mapstructure:"max_cost_usd"`
}

// NormalizeUsageAlerts seeds defaults and drops invalid entries. Returns the
// effective config plus a list of warnings for invalid thresholds (a bad
// entry disables only itself, never the whole alerting subsystem).
func NormalizeUsageAlerts(cfg UsageAlertsConfig) (UsageAlertsConfig, []string) {
    var warns []string
    if cfg.CheckInterval <= 0 {
        cfg.CheckInterval = 5 * time.Minute
    }
    out := cfg.Thresholds[:0:0]
    for _, t := range cfg.Thresholds {
        if t.Window <= 0 {
            warns = append(warns, "usage_alerts: threshold missing window, entry disabled")
            continue
        }
        if t.MaxCostUSD <= 0 {
            warns = append(warns, "usage_alerts: threshold missing max_cost_usd, entry disabled")
            continue
        }
        out = append(out, t)
    }
    cfg.Thresholds = out
    if cfg.Enabled && cfg.WebhookURL == "" {
        warns = append(warns, "usage_alerts: enabled without webhook_url, alerting inert")
    }
    return cfg, warns
}
