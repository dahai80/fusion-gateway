package memory

import (
    "github.com/fusion-gateway/fusion-gateway/internal/store"
)

type DashboardStore struct {
    logStore *LogStore
}

func NewDashboardStore(logStore *LogStore) *DashboardStore {
    return &DashboardStore{logStore: logStore}
}

func (d *DashboardStore) Overview() (*store.DashboardOverview, error) {
    logs := d.logStore.AllLogs()

    overview := &store.DashboardOverview{
        RouteDistribution: make(map[string]float64),
    }

    var totalLatency float64
    var localCount int64

    for _, l := range logs {
        overview.TotalRequests++
        overview.TotalTokens += int64(l.TotalTokens)
        overview.TotalCost += l.Cost
        totalLatency += l.Latency

        if l.ChannelType == "local" {
            localCount++
        }
        overview.RouteDistribution[l.ChannelType]++
    }

    if overview.TotalRequests > 0 {
        overview.LocalHitRate = float64(localCount) / float64(overview.TotalRequests)
    }

    return overview, nil
}
