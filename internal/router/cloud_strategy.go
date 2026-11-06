package router

import (
    "log/slog"
    "math/rand"
    "sync"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

type CloudStrategy struct {
    mu             sync.RWMutex
    cfg            config.CloudRoutingConfig
    latencyTracker *LatencyTracker
    counter        map[string]int
    // usage (G1) counts selections per backend for the "usage" strategy;
    // it is fed by every Select call regardless of the active strategy so
    // switching strategies mid-flight still has data.
    usage map[string]int
    // cooldown state (G1): consecutive-failure streaks and the per-backend
    // cooldown deadline. The gate always consults the CURRENT config's
    // CooldownSeconds, so a reload that disables cooldown deactivates the
    // state immediately (#541 lesson: state must not outlive its config).
    cooldownUntil map[string]time.Time
    failures      map[string]int
}

func NewCloudStrategy(cfg config.CloudRoutingConfig, lt *LatencyTracker) *CloudStrategy {
    return &CloudStrategy{
        cfg:            cfg,
        latencyTracker: lt,
        counter:        make(map[string]int),
        usage:          make(map[string]int),
        cooldownUntil:  make(map[string]time.Time),
        failures:       make(map[string]int),
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
        cs.recordUsage(backends[0])
        return backends[0]
    }

    cs.mu.RLock()
    strategy := cs.cfg.Strategy
    cooldownOn := cs.cfg.CooldownSeconds > 0
    cs.mu.RUnlock()

    candidates := backends
    if cooldownOn {
        candidates = cs.filterCooled(backends)
    }

    var chosen string
    switch strategy {
    case "latency":
        chosen = cs.selectByLatency(candidates)
    case "cost":
        chosen = cs.selectByCost(candidates)
    case "weight":
        chosen = cs.selectByWeight(candidates)
    case "least-busy":
        chosen = cs.selectLeastBusy(candidates)
    case "usage":
        chosen = cs.selectByUsage(candidates)
    case "round-robin":
        chosen = cs.selectRoundRobin(candidates)
    default:
        chosen = candidates[0]
    }
    cs.recordUsage(chosen)
    return chosen
}

// filterCooled drops backends whose cooldown window is still active. When
// every candidate is cooled it returns the full list: cooldown must never
// turn into a self-inflicted outage — availability beats stickiness.
func (cs *CloudStrategy) filterCooled(backends []string) []string {
    now := time.Now()
    cs.mu.RLock()
    defer cs.mu.RUnlock()

    live := make([]string, 0, len(backends))
    for _, b := range backends {
        if until, ok := cs.cooldownUntil[b]; ok && now.Before(until) {
            continue
        }
        live = append(live, b)
    }
    if len(live) == 0 {
        return backends
    }
    return live
}

// RecordOutcome feeds deployment health from the adapter layer (one call
// per cloud request outcome). A failure extends the streak and arms the
// cooldown at FailureThreshold (default 1); a success resets the streak and
// clears an active cooldown — a proven-healthy deployment is eligible again
// without waiting out the window.
func (cs *CloudStrategy) RecordOutcome(backend string, ok bool) {
    cs.mu.Lock()
    defer cs.mu.Unlock()

    if ok {
        if cs.failures[backend] > 0 {
            delete(cs.failures, backend)
        }
        if _, cooled := cs.cooldownUntil[backend]; cooled {
            delete(cs.cooldownUntil, backend)
            slog.Info("cloud deployment cooldown cleared by success", "backend", backend)
        }
        return
    }

    threshold := cs.cfg.FailureThreshold
    if threshold <= 0 {
        threshold = 1
    }
    cs.failures[backend]++
    if cs.failures[backend] >= threshold && cs.cfg.CooldownSeconds > 0 {
        until := time.Now().Add(time.Duration(cs.cfg.CooldownSeconds) * time.Second)
        cs.cooldownUntil[backend] = until
        delete(cs.failures, backend)
        slog.Warn("cloud deployment cooled down after consecutive failures",
            "backend", backend, "cooldown_seconds", cs.cfg.CooldownSeconds, "cooldown_until", until.Format(time.RFC3339))
    }
}

func (cs *CloudStrategy) recordUsage(backend string) {
    if backend == "" {
        return
    }
    cs.mu.Lock()
    cs.usage[backend]++
    cs.mu.Unlock()
}

func (cs *CloudStrategy) selectByUsage(backends []string) string {
    cs.mu.RLock()
    best := backends[0]
    bestCount := cs.usage[best]
    for _, b := range backends[1:] {
        if c := cs.usage[b]; c < bestCount {
            best = b
            bestCount = c
        }
    }
    cs.mu.RUnlock()
    slog.Debug("cloud strategy: usage selected", "backend", best, "selections", bestCount)
    return best
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
