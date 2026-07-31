package memory

import (
    "fmt"
    "sort"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/store"
)

type AnalyticsStore struct {
    logStore *LogStore
}

func NewAnalyticsStore(logStore *LogStore) *AnalyticsStore {
    return &AnalyticsStore{logStore: logStore}
}

func (a *AnalyticsStore) GetTokenStats(from, to time.Time, groupBy string) ([]*store.TokenStat, error) {
    logs := a.logStore.AllLogs()
    buckets := make(map[string]*store.TokenStat)

    for _, l := range logs {
        if l.Timestamp.Before(from) || l.Timestamp.After(to) {
            continue
        }
        key := bucketKey(l.Timestamp, groupBy)
        s, ok := buckets[key]
        if !ok {
            s = &store.TokenStat{Time: key}
            buckets[key] = s
        }
        s.InputTokens += int64(l.InputTokens)
        s.OutputTokens += int64(l.OutputTokens)
        s.TotalTokens += int64(l.TotalTokens)
    }

    result := make([]*store.TokenStat, 0, len(buckets))
    for _, s := range buckets {
        result = append(result, s)
    }
    sort.Slice(result, func(i, j int) bool {
        return result[i].Time < result[j].Time
    })
    return result, nil
}

func (a *AnalyticsStore) GetCostStats(from, to time.Time, groupBy string) ([]*store.CostStat, error) {
    logs := a.logStore.AllLogs()
    buckets := make(map[string]*store.CostStat)

    for _, l := range logs {
        if l.Timestamp.Before(from) || l.Timestamp.After(to) {
            continue
        }
        key := bucketKey(l.Timestamp, groupBy)
        s, ok := buckets[key]
        if !ok {
            s = &store.CostStat{Time: key}
            buckets[key] = s
        }
        s.Cost += l.Cost
        s.CostUSD += l.CostUSD
        s.Savings += l.LocalSavings
    }

    result := make([]*store.CostStat, 0, len(buckets))
    for _, s := range buckets {
        result = append(result, s)
    }
    sort.Slice(result, func(i, j int) bool {
        return result[i].Time < result[j].Time
    })
    return result, nil
}

func (a *AnalyticsStore) GetModelStats(from, to time.Time) ([]*store.ModelStat, error) {
    logs := a.logStore.AllLogs()
    buckets := make(map[string]*store.ModelStat)

    for _, l := range logs {
        if l.Timestamp.Before(from) || l.Timestamp.After(to) {
            continue
        }
        s, ok := buckets[l.Model]
        if !ok {
            s = &store.ModelStat{Model: l.Model}
            buckets[l.Model] = s
        }
        s.RequestCount++
        s.InputTokens += int64(l.InputTokens)
        s.OutputTokens += int64(l.OutputTokens)
        s.Cost += l.Cost
    }

    result := make([]*store.ModelStat, 0, len(buckets))
    for _, s := range buckets {
        result = append(result, s)
    }
    sort.Slice(result, func(i, j int) bool {
        return result[i].RequestCount > result[j].RequestCount
    })
    return result, nil
}

func (a *AnalyticsStore) GetLatencyStats(from, to time.Time) ([]*store.LatencyStat, error) {
    logs := a.logStore.AllLogs()
    channelLatencies := make(map[string][]float64)

    for _, l := range logs {
        if l.Timestamp.Before(from) || l.Timestamp.After(to) {
            continue
        }
        if l.Latency <= 0 {
            continue
        }
        channelLatencies[l.ChannelName] = append(channelLatencies[l.ChannelName], l.Latency)
    }

    result := make([]*store.LatencyStat, 0, len(channelLatencies))
    for ch, latencies := range channelLatencies {
        stat := &store.LatencyStat{Channel: ch}
        sort.Float64s(latencies)
        stat.P50 = percentile(latencies, 50)
        stat.P90 = percentile(latencies, 90)
        stat.P99 = percentile(latencies, 99)
        result = append(result, stat)
    }
    return result, nil
}

func (a *AnalyticsStore) GetErrorStats(from, to time.Time) ([]*store.ErrorStat, error) {
    logs := a.logStore.AllLogs()
    buckets := make(map[string]*store.ErrorStat)

    for _, l := range logs {
        if l.Timestamp.Before(from) || l.Timestamp.After(to) {
            continue
        }
        if l.IsSuccess {
            continue
        }
        key := l.ChannelName + ":" + l.Model + ":" + errorType(l.StatusCode)
        s, ok := buckets[key]
        if !ok {
            s = &store.ErrorStat{
                Channel:   l.ChannelName,
                Model:     l.Model,
                ErrorType: errorType(l.StatusCode),
            }
            buckets[key] = s
        }
        s.Count++
    }

    result := make([]*store.ErrorStat, 0, len(buckets))
    for _, s := range buckets {
        result = append(result, s)
    }
    sort.Slice(result, func(i, j int) bool {
        return result[i].Count > result[j].Count
    })
    return result, nil
}

func bucketKey(t time.Time, groupBy string) string {
    switch groupBy {
    case "hour":
        return t.Format("2006-01-02T15:04")
    case "day":
        return t.Format("2006-01-02")
    case "week":
        y, w := t.ISOWeek()
        return fmt.Sprintf("%04d-W%02d", y, w)
    case "month":
        return t.Format("2006-01")
    default:
        return t.Format("2006-01-02")
    }
}

func percentile(sorted []float64, p int) float64 {
    if len(sorted) == 0 {
        return 0
    }
    idx := (p * len(sorted)) / 100
    if idx >= len(sorted) {
        idx = len(sorted) - 1
    }
    return sorted[idx]
}

func errorType(statusCode int) string {
    switch {
    case statusCode >= 400 && statusCode < 500:
        return "4xx"
    case statusCode >= 500:
        return "5xx"
    default:
        return "unknown"
    }
}
