package router

import (
    "sync"
    "time"
)

type LatencyTracker struct {
    mu      sync.RWMutex
    windows map[string]*latencyWindow
    maxSize int
}

type latencyWindow struct {
    samples []time.Duration
}

func NewLatencyTracker(maxSize int) *LatencyTracker {
    if maxSize <= 0 {
        maxSize = 1000
    }
    return &LatencyTracker{
        windows: make(map[string]*latencyWindow),
        maxSize: maxSize,
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
}

func (lt *LatencyTracker) P95(backend string) time.Duration {
    lt.mu.RLock()
    defer lt.mu.RUnlock()

    w, ok := lt.windows[backend]
    if !ok || len(w.samples) == 0 {
        return 0
    }

    n := len(w.samples)
    sorted := make([]time.Duration, n)
    copy(sorted, w.samples)
    quickSelect(sorted, 0, n-1, n*95/100)

    return sorted[n*95/100]
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
