package cost

import (
    "log/slog"
    "os"
    "sync"
    "time"

    "github.com/fsnotify/fsnotify"
    "github.com/fusion-gateway/fusion-gateway/internal/safego"
    "gopkg.in/yaml.v3"
)

type CustomPricingConfig struct {
    Models map[string]ModelPricing `yaml:"models"`
}

type CustomPricingManager struct {
    mu       sync.RWMutex
    pricing  map[string]ModelPricing
    filePath string
    watcher  *fsnotify.Watcher
}

var globalCustomPricing *CustomPricingManager

func NewCustomPricingManager(filePath string) *CustomPricingManager {
    m := &CustomPricingManager{
        filePath: filePath,
        pricing:  make(map[string]ModelPricing),
    }
    if filePath != "" {
        if err := m.load(); err != nil {
            slog.Warn("custom pricing file load failed, using defaults", "path", filePath, "error", err)
        } else {
            slog.Info("custom pricing loaded", "path", filePath, "models", len(m.pricing))
        }
    }
    globalCustomPricing = m
    return m
}

func (m *CustomPricingManager) load() error {
    data, err := os.ReadFile(m.filePath)
    if err != nil {
        return err
    }
    var cfg CustomPricingConfig
    if err := yaml.Unmarshal(data, &cfg); err != nil {
        return err
    }
    m.mu.Lock()
    m.pricing = cfg.Models
    m.mu.Unlock()
    return nil
}

func (m *CustomPricingManager) StartWatch() {
    if m.filePath == "" {
        return
    }
    watcher, err := fsnotify.NewWatcher()
    if err != nil {
        slog.Error("custom pricing watcher failed", "error", err)
        return
    }
    m.watcher = watcher
    if err := watcher.Add(m.filePath); err != nil {
        slog.Error("custom pricing watch add failed", "path", m.filePath, "error", err)
        return
    }
    // H3: long-lived fsnotify watcher loop — restart on panic so a single panic
    // does not permanently stop hot-reload of custom pricing (pricing stuck).
    safego.GoRestart("custom_pricing_watcher", func() {
        var debounce timer
        for {
            select {
            case event, ok := <-watcher.Events:
                if !ok {
                    return
                }
                if event.Has(fsnotify.Write) || event.Has(fsnotify.Create) {
                    debounce.reset(500*time.Millisecond, func() {
                        if err := m.load(); err != nil {
                            slog.Error("custom pricing reload failed", "error", err)
                        } else {
                            m.mu.RLock()
                            count := len(m.pricing)
                            m.mu.RUnlock()
                            slog.Info("custom pricing reloaded", "models", count)
                        }
                    })
                }
            case err, ok := <-watcher.Errors:
                if !ok {
                    return
                }
                slog.Error("custom pricing watcher error", "error", err)
            }
        }
    })
    slog.Info("custom pricing file watcher started", "path", m.filePath)
}

func (m *CustomPricingManager) Stop() {
    if m.watcher != nil {
        m.watcher.Close()
    }
}

func (m *CustomPricingManager) Lookup(model string) (ModelPricing, bool) {
    m.mu.RLock()
    defer m.mu.RUnlock()
    p, ok := m.pricing[model]
    return p, ok
}

func (m *CustomPricingManager) AllPricing() map[string]ModelPricing {
    m.mu.RLock()
    defer m.mu.RUnlock()
    result := make(map[string]ModelPricing, len(m.pricing))
    for k, v := range m.pricing {
        result[k] = v
    }
    return result
}

type timer struct {
    t *time.Timer
}

func (tb *timer) reset(d time.Duration, fn func()) {
    if tb.t != nil {
        tb.t.Stop()
    }
    tb.t = time.AfterFunc(d, fn)
}
