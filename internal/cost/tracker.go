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
    mu         sync.RWMutex
    records    []UsageRecord
    maxRecords int
    totalCost  float64
}

func NewTracker(maxRecords int) *Tracker {
    if maxRecords <= 0 {
        maxRecords = 10000
    }
    return &Tracker{
        records:    make([]UsageRecord, 0, maxRecords),
        maxRecords: maxRecords,
    }
}

func (t *Tracker) Record(keyName, backend, model string, promptTokens, completionTokens int) {
    cost := CalculateCost(model, promptTokens, completionTokens)
    rec := UsageRecord{
        Timestamp:        time.Now(),
        KeyName:          keyName,
        Backend:          backend,
        Model:            model,
        PromptTokens:     promptTokens,
        CompletionTokens: completionTokens,
        TotalTokens:      promptTokens + completionTokens,
        CostUSD:          cost,
    }

    t.mu.Lock()
    defer t.mu.Unlock()

    t.totalCost += cost
    t.records = append(t.records, rec)
    if len(t.records) > t.maxRecords {
        t.records = t.records[len(t.records)-t.maxRecords:]
    }

    slog.Debug("cost recorded",
        "key", keyName,
        "backend", backend,
        "model", model,
        "cost_usd", cost,
        "total_cost_usd", t.totalCost,
    )
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
