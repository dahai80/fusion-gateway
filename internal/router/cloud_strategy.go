package router

import (
    "log/slog"
    "math/rand"
    "sync"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

type CloudStrategy struct {
    mu             sync.RWMutex
    cfg            config.CloudRoutingConfig
    latencyTracker *LatencyTracker
    counter        map[string]int
}

func NewCloudStrategy(cfg config.CloudRoutingConfig, lt *LatencyTracker) *CloudStrategy {
    return &CloudStrategy{
        cfg:            cfg,
        latencyTracker: lt,
        counter:        make(map[string]int),
    }
}

func (cs *CloudStrategy) UpdateConfig(cfg config.CloudRoutingConfig) {
    cs.mu.Lock()
    defer cs.mu.Unlock()
    cs.cfg = cfg
    slog.Info("cloud strategy config updated", "strategy", cfg.Strategy)
}

func (cs *CloudStrategy) Select(backends []string) string {
    if len(backends) == 0 {
        return ""
    }
    if len(backends) == 1 {
        return backends[0]
    }

    cs.mu.RLock()
    strategy := cs.cfg.Strategy
    cs.mu.RUnlock()

    switch strategy {
    case "latency":
        return cs.selectByLatency(backends)
    case "cost":
        return cs.selectByCost(backends)
    case "weight":
        return cs.selectByWeight(backends)
    case "least-busy":
        return cs.selectLeastBusy(backends)
    case "round-robin":
        return cs.selectRoundRobin(backends)
    default:
        return backends[0]
    }
}

func (cs *CloudStrategy) selectByLatency(backends []string) string {
    if cs.latencyTracker == nil {
        return backends[0]
    }

    best := backends[0]
    bestLatency := cs.latencyTracker.P95(best)
    for _, b := range backends[1:] {
        lat := cs.latencyTracker.P95(b)
        if lat > 0 && (bestLatency <= 0 || lat < bestLatency) {
            best = b
            bestLatency = lat
        }
    }
    slog.Debug("cloud strategy: latency selected", "backend", best, "p95_ms", bestLatency.Milliseconds())
    return best
}

func (cs *CloudStrategy) selectByCost(backends []string) string {
    costOrder := map[string]int{
        "deepseek":     0,
        "groq":         1,
        "together":     2,
        "openai":       3,
        "anthropic":    3,
        "azure-openai": 3,
        "qianfan":      2,
        "volcengine":   2,
    }

    best := backends[0]
    bestCost := 999
    for _, b := range backends {
        if c, ok := costOrder[b]; ok && c < bestCost {
            best = b
            bestCost = c
        }
    }
    return best
}

func (cs *CloudStrategy) selectByWeight(backends []string) string {
    cs.mu.RLock()
    weights := cs.cfg.CloudWeights
    cs.mu.RUnlock()

    if len(weights) == 0 {
        return cs.selectRoundRobin(backends)
    }

    totalWeight := 0
    for _, b := range backends {
        if w, ok := weights[b]; ok {
            totalWeight += w
        }
    }

    if totalWeight == 0 {
        return cs.selectRoundRobin(backends)
    }

    r := rand.Intn(totalWeight)
    cumulative := 0
    for _, b := range backends {
        w := weights[b]
        cumulative += w
        if r < cumulative {
            return b
        }
    }

    return backends[0]
}

func (cs *CloudStrategy) selectLeastBusy(backends []string) string {
    if cs.latencyTracker == nil {
        return cs.selectRoundRobin(backends)
    }

    best := backends[0]
    bestCount := cs.latencyTracker.SampleCount(best)
    for _, b := range backends[1:] {
        count := cs.latencyTracker.SampleCount(b)
        if count < bestCount {
            best = b
            bestCount = count
        }
    }
    return best
}

func (cs *CloudStrategy) selectRoundRobin(backends []string) string {
    cs.mu.Lock()
    defer cs.mu.Unlock()

    idx := cs.counter["round_robin"]
    cs.counter["round_robin"] = idx + 1
    return backends[idx%len(backends)]
}
