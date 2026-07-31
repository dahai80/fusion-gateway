package memory

import (
    "sort"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/store"
)

type ProfitStore struct {
    logStore *LogStore
}

func NewProfitStore(logStore *LogStore) *ProfitStore {
    return &ProfitStore{logStore: logStore}
}

func (p *ProfitStore) GetKeyProfitStats(from, to time.Time) ([]*store.KeyProfitStat, error) {
    logs := p.logStore.AllLogs()
    buckets := make(map[string]*store.KeyProfitStat)

    for _, l := range logs {
        if l.Timestamp.Before(from) || l.Timestamp.After(to) {
            continue
        }
        keyName := l.APIKeyName
        if keyName == "" {
            keyName = "anonymous"
        }
        s, ok := buckets[keyName]
        if !ok {
            s = &store.KeyProfitStat{KeyName: keyName}
            buckets[keyName] = s
        }
        s.TotalInput += int64(l.InputTokens)
        s.TotalOutput += int64(l.OutputTokens)
        s.TotalCost += l.Cost
        s.RequestCount++
    }

    result := make([]*store.KeyProfitStat, 0, len(buckets))
    for _, s := range buckets {
        if s.TotalInput > 0 {
            s.Ratio = float64(s.TotalOutput) / float64(s.TotalInput)
        }
        result = append(result, s)
    }
    sort.Slice(result, func(i, j int) bool {
        return result[i].TotalCost > result[j].TotalCost
    })
    return result, nil
}
