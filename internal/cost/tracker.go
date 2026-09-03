package cost

import (
    "encoding/json"
    "log/slog"
    "os"
    "sync"
    "time"
)

type UsageRecord struct {
    Timestamp        time.Time `json:"timestamp"`
    KeyName          string    `json:"key_name"`
    Backend          string    `json:"backend"`
    Model            string    `json:"model"`
    PromptTokens     int       `json:"prompt_tokens"`
    CompletionTokens int       `json:"completion_tokens"`
    TotalTokens      int       `json:"total_tokens"`
    CostUSD          float64   `json:"cost_usd"`
}

type Tracker struct {
    mu          sync.RWMutex
    records     []UsageRecord
    maxRecords  int
    totalCost   float64
    markup      float64
    keyMarkups map[string]float64
}

func NewTracker(maxRecords int) *Tracker {
    if maxRecords <= 0 {
        maxRecords = 10000
    }
    return &Tracker{
        records:     make([]UsageRecord, 0, maxRecords),
        maxRecords:  maxRecords,
        keyMarkups:  make(map[string]float64),
    }
}

func (t *Tracker) Record(keyName, backend, model string, promptTokens, completionTokens int) {
    t.RecordAndReturn(keyName, backend, model, promptTokens, completionTokens)
}

// RecordAndReturn is #159's variant: it records the usage AND returns the
// billed USD cost so the caller can deduct it from the per-key and per-tenant
// quota counters. Without this the gateway metered cost (for the /v1/cost
// dashboard) but never decremented the budget — BudgetBlock only checked at
// request start, so a key could spend past its cap within a single check
// window. The deduction is best-effort: a store error is logged, not surfaced,
// because the inference response is already delivered.
func (t *Tracker) RecordAndReturn(keyName, backend, model string, promptTokens, completionTokens int) float64 {
    baseCost := CalculateCost(model, promptTokens, completionTokens)
    billedCost := t.applyMarkup(keyName, baseCost)
    rec := UsageRecord{
        Timestamp:        time.Now(),
        KeyName:          keyName,
        Backend:          backend,
        Model:            model,
        PromptTokens:     promptTokens,
        CompletionTokens: completionTokens,
        TotalTokens:      promptTokens + completionTokens,
        CostUSD:          billedCost,
    }

    t.mu.Lock()
    defer t.mu.Unlock()

    t.totalCost += billedCost
    t.records = append(t.records, rec)
    if len(t.records) > t.maxRecords {
        // B9 fix: copy to new slice to release old underlying array for GC
        trimmed := make([]UsageRecord, t.maxRecords)
        copy(trimmed, t.records[len(t.records)-t.maxRecords:])
        t.records = trimmed
    }

    slog.Debug("cost recorded",
        "key", keyName,
        "backend", backend,
        "model", model,
        "base_cost", baseCost,
        "billed_cost", billedCost,
        "total_cost_usd", t.totalCost,
    )
    return billedCost
}

func (t *Tracker) applyMarkup(keyName string, baseCost float64) float64 {
    t.mu.RLock()
    defer t.mu.RUnlock()
    multiplier := 1.0 + t.markup
    if km, ok := t.keyMarkups[keyName]; ok {
        multiplier = 1.0 + km
    }
    return baseCost * multiplier
}

func (t *Tracker) SetGlobalMarkup(markup float64) {
    t.mu.Lock()
    defer t.mu.Unlock()
    t.markup = markup
    slog.Info("global cost markup set", "markup", markup)
}

func (t *Tracker) SetKeyMarkup(keyName string, markup float64) {
    t.mu.Lock()
    defer t.mu.Unlock()
    t.keyMarkups[keyName] = markup
    slog.Info("key cost markup set", "key", keyName, "markup", markup)
}

func (t *Tracker) GetMarkup(keyName string) float64 {
    t.mu.RLock()
    defer t.mu.RUnlock()
    if km, ok := t.keyMarkups[keyName]; ok {
        return km
    }
    return t.markup
}

type CostSummary struct {
    TotalCostUSD  float64           `json:"total_cost_usd"`
    ByKey         map[string]float64 `json:"by_key"`
    ByBackend     map[string]float64 `json:"by_backend"`
    ByModel       map[string]float64 `json:"by_model"`
    TotalTokens   int               `json:"total_tokens"`
    TotalRequests int               `json:"total_requests"`
}

func (t *Tracker) Summary() *CostSummary {
    t.mu.RLock()
    defer t.mu.RUnlock()

    s := &CostSummary{
        TotalCostUSD:  t.totalCost,
        ByKey:         make(map[string]float64),
        ByBackend:     make(map[string]float64),
        ByModel:       make(map[string]float64),
        TotalTokens:   0,
        TotalRequests: len(t.records),
    }

    for _, r := range t.records {
        s.ByKey[r.KeyName] += r.CostUSD
        s.ByBackend[r.Backend] += r.CostUSD
        s.ByModel[r.Model] += r.CostUSD
        s.TotalTokens += r.TotalTokens
    }

    return s
}

func (t *Tracker) SummaryByKey(keyName string) *CostSummary {
    t.mu.RLock()
    defer t.mu.RUnlock()

    s := &CostSummary{
        ByKey:     make(map[string]float64),
        ByBackend: make(map[string]float64),
        ByModel:   make(map[string]float64),
    }

    for _, r := range t.records {
        if r.KeyName != keyName {
            continue
        }
        s.TotalCostUSD += r.CostUSD
        s.ByKey[r.KeyName] += r.CostUSD
        s.ByBackend[r.Backend] += r.CostUSD
        s.ByModel[r.Model] += r.CostUSD
        s.TotalTokens += r.TotalTokens
        s.TotalRequests++
    }

    return s
}

func (t *Tracker) ExportJSON(path string) error {
    t.mu.RLock()
    defer t.mu.RUnlock()

    data, err := json.MarshalIndent(t.records, "", "  ")
    if err != nil {
        return err
    }
    return os.WriteFile(path, data, 0644)
}
