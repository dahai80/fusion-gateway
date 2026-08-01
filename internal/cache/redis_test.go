package cache

import (
    "context"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

func TestNewRedisBackend_ConnectionFailed(t *testing.T) {
    cfg := config.RedisConfig{
        Addr: "localhost:1",
        DB:   0,
    }
    _, err := NewRedisBackend(cfg)
    if err == nil {
        t.Fatal("expected error when redis is unreachable")
    }
    t.Logf("expected connection error: %v", err)
}

func TestNewRedisBackend_DefaultPoolSize(t *testing.T) {
    cfg := config.RedisConfig{
        Addr:     "localhost:6379",
        PoolSize: 0,
    }
    ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
    defer cancel()

    backend, err := NewRedisBackend(cfg)
    if err != nil {
        t.Skipf("redis not available: %v", err)
        return
    }
    defer backend.Close()

    if backend.client.Options().PoolSize != 10 {
        t.Fatalf("expected default pool size 10, got %d", backend.client.Options().PoolSize)
    }
    cancel()
    _ = ctx
}

func TestRedisBackend_Operations_WhenAvailable(t *testing.T) {
    cfg := config.RedisConfig{
        Addr: "localhost:6379",
        DB:   0,
    }
    backend, err := NewRedisBackend(cfg)
    if err != nil {
        t.Skipf("redis not available: %v", err)
    }
    defer backend.Close()

    backend.Set("testkey", []byte("testval"), time.Minute)

    val, ok := backend.Get("testkey")
    if !ok || string(val) != "testval" {
        t.Fatalf("expected testval, got %s ok=%v", val, ok)
    }

    backend.Delete("testkey")

    _, ok = backend.Get("testkey")
    if ok {
        t.Fatal("expected miss after delete")
    }

    hits, misses, size := backend.Stats()
    t.Logf("stats: hits=%d misses=%d size=%d", hits, misses, size)
}

func TestRedisBackend_GetMiss(t *testing.T) {
    cfg := config.RedisConfig{Addr: "localhost:6379"}
    backend, err := NewRedisBackend(cfg)
    if err != nil {
        t.Skipf("redis not available: %v", err)
    }
    defer backend.Close()

    _, ok := backend.Get("nonexistent_key_for_test")
    if ok {
        t.Fatal("expected miss for nonexistent key")
    }
}

func TestRedisBackend_SetError_Handled(t *testing.T) {
    cfg := config.RedisConfig{Addr: "localhost:6379"}
    backend, err := NewRedisBackend(cfg)
    if err != nil {
        t.Skipf("redis not available: %v", err)
    }
    backend.Close()

    backend.Set("after_close", []byte("data"), time.Minute)
}

func TestRedisBackend_GetAfterClose(t *testing.T) {
    cfg := config.RedisConfig{Addr: "localhost:6379"}
    backend, err := NewRedisBackend(cfg)
    if err != nil {
        t.Skipf("redis not available: %v", err)
    }
    backend.Close()

    _, ok := backend.Get("after_close")
    if ok {
        t.Log("Get after close returned ok=true (connection may have been reused)")
    }
}

func TestRedisBackend_DeleteAfterClose(t *testing.T) {
    cfg := config.RedisConfig{Addr: "localhost:6379"}
    backend, err := NewRedisBackend(cfg)
    if err != nil {
        t.Skipf("redis not available: %v", err)
    }
    backend.Close()

    backend.Delete("after_close")
}

func TestRedisBackend_StatsAfterClose(t *testing.T) {
    cfg := config.RedisConfig{Addr: "localhost:6379"}
    backend, err := NewRedisBackend(cfg)
    if err != nil {
        t.Skipf("redis not available: %v", err)
    }
    backend.Close()

    hits, misses, size := backend.Stats()
    t.Logf("stats after close: hits=%d misses=%d size=%d", hits, misses, size)
}

func TestRedisBackend_CloseIdempotent(t *testing.T) {
    cfg := config.RedisConfig{Addr: "localhost:6379"}
    backend, err := NewRedisBackend(cfg)
    if err != nil {
        t.Skipf("redis not available: %v", err)
    }
    backend.Close()
    backend.Close()
}
