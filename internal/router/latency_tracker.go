package router

import (
    "sync"
    "time"
)

type LatencyTracker struct {
    mu      sync.RWMutex
    windows map[string]*latencyWindow
    maxSize int
    // P3 fix: cache recent P95 results to avoid redundant quickSelect on hot path
    p95Cache   map[string]p95CacheEntry
    p95TTL     time.Duration
}

type p95CacheEntry struct {
    value      time.Duration
    computedAt time.Time
}

type latencyWindow struct {
    samples []time.Duration
}

func NewLatencyTracker(maxSize int) *LatencyTracker {
    if maxSize <= 0 {
        maxSize = 1000
    }
    return &LatencyTracker{
        windows:  make(map[string]*latencyWindow),
        maxSize:  maxSize,
        p95Cache: make(map[string]p95CacheEntry),
        p95TTL:   5 * time.Second,
    }
}

func (lt *LatencyTracker) Record(backend string, duration time.Duration) {
    lt.mu.Lock()
    defer lt.mu.Unlock()

    w, ok := lt.windows[backend]
    if !ok {
        w = &latencyWindow{samples: make([]time.Duration, 0, lt.maxSize)}
        lt.windows[backend] = w
    }

    w.samples = append(w.samples, duration)
    if len(w.samples) > lt.maxSize {
        w.samples = w.samples[len(w.samples)-lt.maxSize:]
    }

    // Invalidate P95 cache for this backend on new sample
    delete(lt.p95Cache, backend)
}

func (lt *LatencyTracker) P95(backend string) time.Duration {
    lt.mu.RLock()
    // P3 fix: return cached result if still valid
    if entry, ok := lt.p95Cache[backend]; ok {
        if time.Since(entry.computedAt) < lt.p95TTL {
            lt.mu.RUnlock()
            return entry.value
        }
    }

    w, ok := lt.windows[backend]
    if !ok || len(w.samples) == 0 {
        lt.mu.RUnlock()
        return 0
    }

    n := len(w.samples)
    sorted := make([]time.Duration, n)
    copy(sorted, w.samples)
    lt.mu.RUnlock()

    quickSelect(sorted, 0, n-1, n*95/100)
    result := sorted[n*95/100]

    // Cache result
    lt.mu.Lock()
    lt.p95Cache[backend] = p95CacheEntry{value: result, computedAt: time.Now()}
    lt.mu.Unlock()

    return result
}

func (lt *LatencyTracker) SampleCount(backend string) int {
    lt.mu.RLock()
    defer lt.mu.RUnlock()

    w, ok := lt.windows[backend]
    if !ok {
        return 0
    }
    return len(w.samples)
}

func quickSelect(arr []time.Duration, lo, hi, k int) {
    for lo < hi {
        pivot := arr[(lo+hi)/2]
        i, j := lo, hi
        for i <= j {
            for arr[i] < pivot {
                i++
            }
            for arr[j] > pivot {
                j--
            }
            if i <= j {
                arr[i], arr[j] = arr[j], arr[i]
                i++
                j--
            }
        }
        if k <= j {
            hi = j
        } else if k >= i {
            lo = i
        } else {
            return
        }
    }
}
