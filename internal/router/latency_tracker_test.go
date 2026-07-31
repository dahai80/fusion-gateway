package router

import (
    "testing"
    "time"
)

func TestNewLatencyTracker_DefaultsMaxSize(t *testing.T) {
    t.Parallel()
    lt := NewLatencyTracker(0)
    if lt.maxSize != 1000 {
        t.Errorf("expected default maxSize 1000, got %d", lt.maxSize)
    }

    lt2 := NewLatencyTracker(-5)
    if lt2.maxSize != 1000 {
        t.Errorf("expected default maxSize 1000 for negative input, got %d", lt2.maxSize)
    }
}

func TestLatencyTracker_RecordAndP95(t *testing.T) {
    t.Parallel()
    lt := NewLatencyTracker(1000)

    lt.Record("openai", 100*time.Millisecond)
    lt.Record("openai", 200*time.Millisecond)
    lt.Record("openai", 300*time.Millisecond)
    lt.Record("openai", 400*time.Millisecond)
    lt.Record("openai", 500*time.Millisecond)

    p95 := lt.P95("openai")
    if p95 <= 0 {
        t.Errorf("expected positive P95, got %v", p95)
    }
}

func TestLatencyTracker_P95UnknownBackend(t *testing.T) {
    t.Parallel()
    lt := NewLatencyTracker(1000)

    p95 := lt.P95("nonexistent")
    if p95 != 0 {
        t.Errorf("expected 0 for unknown backend, got %v", p95)
    }
}

func TestLatencyTracker_SampleCount(t *testing.T) {
    t.Parallel()
    lt := NewLatencyTracker(1000)

    if count := lt.SampleCount("openai"); count != 0 {
        t.Errorf("expected 0 samples for unknown backend, got %d", count)
    }

    lt.Record("openai", 100*time.Millisecond)
    lt.Record("openai", 200*time.Millisecond)
    lt.Record("openai", 300*time.Millisecond)

    if count := lt.SampleCount("openai"); count != 3 {
        t.Errorf("expected 3 samples, got %d", count)
    }
}

func TestLatencyTracker_MaxSizeEviction(t *testing.T) {
    lt := NewLatencyTracker(10)

    for i := 0; i < 20; i++ {
        lt.Record("openai", time.Duration(i+1)*time.Millisecond)
    }

    count := lt.SampleCount("openai")
    if count != 10 {
        t.Errorf("expected 10 samples after eviction, got %d", count)
    }

    p95 := lt.P95("openai")
    if p95 <= 0 {
        t.Errorf("expected positive P95 after eviction, got %v", p95)
    }

    if p95 < 11*time.Millisecond {
        t.Errorf("expected P95 >= 11ms (oldest evicted samples 1-10), got %v", p95)
    }
}

func TestLatencyTracker_P95WithEnoughSamples(t *testing.T) {
    t.Parallel()
    lt := NewLatencyTracker(1000)

    for i := 0; i < 100; i++ {
        lt.Record("deepseek", time.Duration(i+1)*time.Millisecond)
    }

    p95 := lt.P95("deepseek")
    expectedP95 := 95 * time.Millisecond

    if p95 < 90*time.Millisecond || p95 > 100*time.Millisecond {
        t.Errorf("expected P95 around %v, got %v", expectedP95, p95)
    }
}

func TestLatencyTracker_MultipleBackends(t *testing.T) {
    t.Parallel()
    lt := NewLatencyTracker(1000)

    lt.Record("openai", 200*time.Millisecond)
    lt.Record("openai", 300*time.Millisecond)

    lt.Record("deepseek", 50*time.Millisecond)
    lt.Record("deepseek", 60*time.Millisecond)

    p95Openai := lt.P95("openai")
    p95Deepseek := lt.P95("deepseek")

    if p95Openai <= p95Deepseek {
        t.Errorf("expected openai P95 > deepseek P95, got openai=%v deepseek=%v", p95Openai, p95Deepseek)
    }
}

func TestLatencyTracker_SampleCountIsolated(t *testing.T) {
    t.Parallel()
    lt := NewLatencyTracker(1000)

    lt.Record("openai", 100*time.Millisecond)
    lt.Record("openai", 200*time.Millisecond)

    lt.Record("deepseek", 50*time.Millisecond)

    if count := lt.SampleCount("openai"); count != 2 {
        t.Errorf("expected 2 samples for openai, got %d", count)
    }
    if count := lt.SampleCount("deepseek"); count != 1 {
        t.Errorf("expected 1 sample for deepseek, got %d", count)
    }
}
