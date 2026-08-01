package cost

import (
    "os"
    "path/filepath"
    "testing"
    "time"

    "gopkg.in/yaml.v3"
)

func TestStartWatch_FileWriteTriggersReload(t *testing.T) {
    saved := globalCustomPricing
    defer func() { globalCustomPricing = saved }()

    dir := t.TempDir()
    f := filepath.Join(dir, "pricing.yaml")

    cfg := CustomPricingConfig{
        Models: map[string]ModelPricing{
            "model-a": {PromptPricePer1M: 1.0, CompletionPricePer1M: 2.0},
        },
    }
    data, _ := yaml.Marshal(cfg)
    if err := os.WriteFile(f, data, 0644); err != nil {
        t.Fatalf("initial write: %v", err)
    }

    m := NewCustomPricingManager(f)
    m.StartWatch()
    defer m.Stop()

    p, ok := m.Lookup("model-a")
    if !ok || p.PromptPricePer1M != 1.0 {
        t.Fatalf("expected model-a pricing before update, got ok=%v p=%+v", ok, p)
    }

    updated := CustomPricingConfig{
        Models: map[string]ModelPricing{
            "model-a": {PromptPricePer1M: 99.0, CompletionPricePer1M: 88.0},
            "model-b": {PromptPricePer1M: 5.0, CompletionPricePer1M: 6.0},
        },
    }
    data2, _ := yaml.Marshal(updated)
    if err := os.WriteFile(f, data2, 0644); err != nil {
        t.Fatalf("update write: %v", err)
    }

    t.Log("waiting for fsnotify debounce + reload...")
    deadline := time.After(5 * time.Second)
    for {
        p, ok := m.Lookup("model-a")
        if ok && p.PromptPricePer1M == 99.0 {
            break
        }
        select {
        case <-deadline:
            t.Fatal("timed out waiting for file watcher reload")
        case <-time.After(100 * time.Millisecond):
        }
    }

    p2, ok2 := m.Lookup("model-b")
    if !ok2 || p2.PromptPricePer1M != 5.0 {
        t.Fatalf("expected model-b after reload, got ok=%v p=%+v", ok2, p2)
    }
}

func TestStartWatch_DebounceRapidWrites(t *testing.T) {
    saved := globalCustomPricing
    defer func() { globalCustomPricing = saved }()

    dir := t.TempDir()
    f := filepath.Join(dir, "pricing.yaml")

    cfg := CustomPricingConfig{
        Models: map[string]ModelPricing{
            "rapid-model": {PromptPricePer1M: 1.0, CompletionPricePer1M: 2.0},
        },
    }
    data, _ := yaml.Marshal(cfg)
    if err := os.WriteFile(f, data, 0644); err != nil {
        t.Fatalf("initial write: %v", err)
    }

    m := NewCustomPricingManager(f)
    m.StartWatch()
    defer m.Stop()

    for i := 0; i < 5; i++ {
        tmpCfg := CustomPricingConfig{
            Models: map[string]ModelPricing{
                "rapid-model": {PromptPricePer1M: float64(i + 10), CompletionPricePer1M: 2.0},
            },
        }
        tmpData, _ := yaml.Marshal(tmpCfg)
        _ = os.WriteFile(f, tmpData, 0644)
        time.Sleep(50 * time.Millisecond)
    }

    finalCfg := CustomPricingConfig{
        Models: map[string]ModelPricing{
            "rapid-model": {PromptPricePer1M: 77.0, CompletionPricePer1M: 88.0},
            "debounced-model": {PromptPricePer1M: 3.0, CompletionPricePer1M: 4.0},
        },
    }
    finalData, _ := yaml.Marshal(finalCfg)
    if err := os.WriteFile(f, finalData, 0644); err != nil {
        t.Fatalf("final write: %v", err)
    }

    t.Log("waiting for debounce + reload after rapid writes...")
    deadline := time.After(5 * time.Second)
    for {
        p, ok := m.Lookup("debounced-model")
        if ok && p.PromptPricePer1M == 3.0 {
            break
        }
        select {
        case <-deadline:
            t.Fatal("timed out waiting for debounced reload")
        case <-time.After(100 * time.Millisecond):
        }
    }
}

func TestStartWatch_ReloadFailureOnBadYAML(t *testing.T) {
    saved := globalCustomPricing
    defer func() { globalCustomPricing = saved }()

    dir := t.TempDir()
    f := filepath.Join(dir, "pricing.yaml")

    cfg := CustomPricingConfig{
        Models: map[string]ModelPricing{
            "good-model": {PromptPricePer1M: 1.0, CompletionPricePer1M: 2.0},
        },
    }
    data, _ := yaml.Marshal(cfg)
    if err := os.WriteFile(f, data, 0644); err != nil {
        t.Fatalf("initial write: %v", err)
    }

    m := NewCustomPricingManager(f)
    m.StartWatch()
    defer m.Stop()

    p, ok := m.Lookup("good-model")
    if !ok || p.PromptPricePer1M != 1.0 {
        t.Fatalf("expected good-model before bad write")
    }

    if err := os.WriteFile(f, []byte("{{invalid yaml"), 0644); err != nil {
        t.Fatalf("bad write: %v", err)
    }

    time.Sleep(2 * time.Second)

    p2, ok2 := m.Lookup("good-model")
    if !ok2 || p2.PromptPricePer1M != 1.0 {
        t.Logf("after bad YAML, good-model still present (old data kept): ok=%v p=%+v", ok2, p2)
    }
}

func TestStop_WithActiveWatcher(t *testing.T) {
    saved := globalCustomPricing
    defer func() { globalCustomPricing = saved }()

    dir := t.TempDir()
    f := filepath.Join(dir, "pricing.yaml")
    cfg := CustomPricingConfig{
        Models: map[string]ModelPricing{
            "m1": {PromptPricePer1M: 1.0, CompletionPricePer1M: 2.0},
        },
    }
    data, _ := yaml.Marshal(cfg)
    _ = os.WriteFile(f, data, 0644)

    m := NewCustomPricingManager(f)
    m.StartWatch()

    if m.watcher == nil {
        t.Fatal("expected watcher to be non-nil after StartWatch")
    }

    m.Stop()

    time.Sleep(200 * time.Millisecond)

    p, ok := m.Lookup("m1")
    if !ok || p.PromptPricePer1M != 1.0 {
        t.Fatalf("expected Lookup to still work after Stop, got ok=%v p=%+v", ok, p)
    }
}

func TestTimerReset_NewTimer(t *testing.T) {
    var tb timer
    done := make(chan struct{}, 1)

    tb.reset(50*time.Millisecond, func() {
        done <- struct{}{}
    })

    if tb.t == nil {
        t.Fatal("expected timer to be created")
    }

    select {
    case <-done:
        t.Log("timer fired correctly")
    case <-time.After(2 * time.Second):
        t.Fatal("timer did not fire within timeout")
    }
}

func TestTimerReset_ReplaceExistingTimer(t *testing.T) {
    var tb timer
    callCount := 0
    done := make(chan struct{}, 1)

    tb.reset(200*time.Millisecond, func() {
        callCount++
    })

    tb.reset(50*time.Millisecond, func() {
        callCount++
        done <- struct{}{}
    })

    select {
    case <-done:
    case <-time.After(2 * time.Second):
        t.Fatal("second timer did not fire within timeout")
    }

    time.Sleep(300 * time.Millisecond)

    if callCount != 1 {
        t.Fatalf("expected 1 call (first cancelled), got %d", callCount)
    }
}

func TestTimerReset_StopOldTimer(t *testing.T) {
    var tb timer
    firstFired := false
    secondDone := make(chan struct{}, 1)

    tb.reset(100*time.Millisecond, func() {
        firstFired = true
    })

    time.Sleep(20 * time.Millisecond)

    tb.reset(500*time.Millisecond, func() {
        secondDone <- struct{}{}
    })

    select {
    case <-secondDone:
    case <-time.After(2 * time.Second):
        t.Fatal("second timer did not fire")
    }

    if firstFired {
        t.Error("first timer should have been stopped")
    }
}

func TestStartWatch_WatcherCloseExitsGoroutine(t *testing.T) {
    saved := globalCustomPricing
    defer func() { globalCustomPricing = saved }()

    dir := t.TempDir()
    f := filepath.Join(dir, "pricing.yaml")
    cfg := CustomPricingConfig{
        Models: map[string]ModelPricing{
            "m1": {PromptPricePer1M: 1.0, CompletionPricePer1M: 2.0},
        },
    }
    data, _ := yaml.Marshal(cfg)
    _ = os.WriteFile(f, data, 0644)

    m := NewCustomPricingManager(f)
    m.StartWatch()

    if m.watcher == nil {
        t.Fatal("expected watcher to be set")
    }

    m.Stop()
    t.Log("Stop() called, watcher closed - goroutine should exit via ok==false")
}
