package memory

import (
    "log/slog"
    "sync"

    "github.com/fusion-gateway/fusion-gateway/internal/store"
)

type CostSubStore struct {
    mu         sync.RWMutex
    records    []store.UsageRecord
    maxRecords int
    totalCost  float64
}

func NewCostSubStore(maxRecords int) *CostSubStore {
    if maxRecords <= 0 {
        maxRecords = 10000
    }
    return &CostSubStore{
        records:    make([]store.UsageRecord, 0, maxRecords),
        maxRecords: maxRecords,
    }
}

func (c *CostSubStore) Record(keyName, backend, model string, promptTokens, completionTokens int, costUSD float64) {
    rec := store.UsageRecord{
        KeyName:          keyName,
        Backend:          backend,
        Model:            model,
        PromptTokens:     promptTokens,
        CompletionTokens: completionTokens,
        TotalTokens:      promptTokens + completionTokens,
        CostUSD:          costUSD,
    }

    c.mu.Lock()
    defer c.mu.Unlock()

    c.totalCost += costUSD
    c.records = append(c.records, rec)
    if len(c.records) > c.maxRecords {
        c.records = c.records[len(c.records)-c.maxRecords:]
    }

    slog.Debug("usage recorded",
        "key", keyName,
        "backend", backend,
        "model", model,
        "cost_usd", costUSD,
    )
}

func (c *CostSubStore) Summary(keyName string) *store.CostSummary {
    c.mu.RLock()
    defer c.mu.RUnlock()

    s := &store.CostSummary{
        ByKey:     make(map[string]float64),
        ByBackend: make(map[string]float64),
        ByModel:   make(map[string]float64),
    }

    for _, r := range c.records {
        if keyName != "" && r.KeyName != keyName {
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
