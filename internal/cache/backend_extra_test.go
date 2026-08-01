package cache

import (
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

func TestNoopBackend_AllMethods(t *testing.T) {
    b := NewBackend(nil, nil)

    val, ok := b.Get("k")
    if ok || val != nil {
        t.Fatalf("noop Get should return nil,false: val=%v ok=%v", val, ok)
    }

    b.Set("k", []byte("v"), time.Minute)
    b.Delete("k")

    hits, misses, size := b.Stats()
    if hits != 0 || misses != 0 || size != 0 {
        t.Fatalf("noop Stats should be 0,0,0: %d,%d,%d", hits, misses, size)
    }

    b.Close()
}

func TestLocalBackend_Stats(t *testing.T) {
    c := New(config.CacheConfig{Enabled: true, MaxEntries: 100, TTL: time.Minute})
    b := NewBackend(c, nil)

    b.Set("k1", []byte("v1"), time.Minute)
    b.Get("k1")
    b.Get("missing")

    hits, misses, size := b.Stats()
    if hits != 1 {
        t.Fatalf("expected 1 hit, got %d", hits)
    }
    if misses != 1 {
        t.Fatalf("expected 1 miss, got %d", misses)
    }
    if size != 1 {
        t.Fatalf("expected size 1, got %d", size)
    }
}

func TestLocalBackend_Close(t *testing.T) {
    c := New(config.CacheConfig{Enabled: true, MaxEntries: 100, TTL: time.Minute})
    b := NewBackend(c, nil)
    b.Set("k", []byte("v"), time.Minute)
    b.Close()
}

func TestLocalBackend_DeleteAndReGet(t *testing.T) {
    c := New(config.CacheConfig{Enabled: true, MaxEntries: 100, TTL: time.Minute})
    b := NewBackend(c, nil)
    b.Set("k", []byte("v"), time.Minute)
    val, ok := b.Get("k")
    if !ok || string(val) != "v" {
        t.Fatalf("expected v, got %s ok=%v", val, ok)
    }
    b.Delete("k")
    _, ok = b.Get("k")
    if ok {
        t.Fatal("expected miss after delete")
    }
}

func TestTieredBackend_Stats(t *testing.T) {
    c := New(config.CacheConfig{Enabled: true, MaxEntries: 100, TTL: time.Minute})
    mockL2 := &mockCacheBackend{}
    b := NewBackend(c, mockL2)

    b.Set("k1", []byte("v1"), time.Minute)
    b.Get("k1")

    hits, misses, size := b.Stats()
    if hits != 1 {
        t.Fatalf("expected 1 hit from L1 stats, got %d", hits)
    }
    if misses != 0 {
        t.Fatalf("expected 0 misses, got %d", misses)
    }
    if size != 1 {
        t.Fatalf("expected size 1, got %d", size)
    }
}

func TestTieredBackend_L2Miss(t *testing.T) {
    c := New(config.CacheConfig{Enabled: true, MaxEntries: 100, TTL: time.Minute})
    mockL2 := &mockCacheBackend{}
    b := NewBackend(c, mockL2)

    _, ok := b.Get("nonexistent")
    if ok {
        t.Fatal("expected miss when both L1 and L2 miss")
    }
}

func TestTieredBackend_CloseWithoutL2(t *testing.T) {
    c := New(config.CacheConfig{Enabled: true, MaxEntries: 100, TTL: time.Minute})
    b := NewBackend(c, nil)
    b.Close()
}

func TestTieredBackend_SetPropagatesToL2(t *testing.T) {
    c := New(config.CacheConfig{Enabled: true, MaxEntries: 100, TTL: time.Minute})
    mockL2 := &mockCacheBackend{}
    b := NewBackend(c, mockL2)

    b.Set("propagate", []byte("data"), 30*time.Second)

    if mockL2.lastSetKey != "propagate" {
        t.Fatalf("expected L2 set key 'propagate', got %q", mockL2.lastSetKey)
    }
    if string(mockL2.data["propagate"]) != "data" {
        t.Fatalf("expected L2 data 'data', got %q", string(mockL2.data["propagate"]))
    }
}

func TestTieredBackend_DeletePropagatesToL2(t *testing.T) {
    c := New(config.CacheConfig{Enabled: true, MaxEntries: 100, TTL: time.Minute})
    mockL2 := &mockCacheBackend{data: map[string][]byte{"delme": []byte("v")}}
    b := NewBackend(c, mockL2)

    b.Set("delme", []byte("v"), time.Minute)
    b.Delete("delme")

    if mockL2.lastDeleteKey != "delme" {
        t.Fatalf("expected L2 delete key 'delme', got %q", mockL2.lastDeleteKey)
    }
}

type countingMockBackend struct {
    data          map[string][]byte
    getCount      int
    setCount      int
    deleteCount   int
    statsCount    int
    closed        bool
}

func (m *countingMockBackend) Get(key string) ([]byte, bool) {
    m.getCount++
    v, ok := m.data[key]
    return v, ok
}

func (m *countingMockBackend) Set(key string, value []byte, ttl time.Duration) {
    m.setCount++
    if m.data == nil {
        m.data = make(map[string][]byte)
    }
    m.data[key] = value
}

func (m *countingMockBackend) Delete(key string) {
    m.deleteCount++
    delete(m.data, key)
}

func (m *countingMockBackend) Stats() (hits, misses int64, size int) {
    m.statsCount++
    return 0, 0, len(m.data)
}

func (m *countingMockBackend) Close() {
    m.closed = true
}

func TestTieredBackend_L2PromotesToL1(t *testing.T) {
    c := New(config.CacheConfig{Enabled: true, MaxEntries: 100, TTL: time.Minute})
    l2 := &countingMockBackend{data: map[string][]byte{"promo": []byte("l2val")}}
    b := NewBackend(c, l2)

    val, ok := b.Get("promo")
    if !ok || string(val) != "l2val" {
        t.Fatalf("expected l2val from L2, got %s ok=%v", val, ok)
    }

    val2, ok2 := c.Get("promo")
    if !ok2 || string(val2) != "l2val" {
        t.Fatal("expected L1 to be populated from L2 promotion")
    }

    if l2.getCount != 1 {
        t.Fatalf("expected 1 L2 Get call, got %d", l2.getCount)
    }
}

func TestTieredBackend_L1HitSkipsL2(t *testing.T) {
    c := New(config.CacheConfig{Enabled: true, MaxEntries: 100, TTL: time.Minute})
    l2 := &countingMockBackend{}
    b := NewBackend(c, l2)

    b.Set("onlyl1", []byte("data"), time.Minute)
    l2.getCount = 0

    val, ok := b.Get("onlyl1")
    if !ok || string(val) != "data" {
        t.Fatalf("expected data from L1, got %s ok=%v", val, ok)
    }

    if l2.getCount != 0 {
        t.Fatalf("L2 Get should not be called on L1 hit, got %d calls", l2.getCount)
    }
}
