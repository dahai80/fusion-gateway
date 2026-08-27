package cache

import (
    "context"
    "fmt"
    "log/slog"
    "sync/atomic"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/redis/go-redis/v9"
)

type RedisBackend struct {
    client *redis.Client
    prefix string
    // E2: hits/misses are atomic counters, NOT plain int64. Get runs on
    // concurrent request goroutines, so a `r.hits++` read-modify-write is a
    // data race (torn counter, /metrics hit-rate corrupted). Same bug class
    // as EI2 (semantic.go) — atomic.Int64 makes the increment lock-free and
    // race-free. sync/atomic, not a mutex, to keep the hot Get path cheap.
    hits   atomic.Int64
    misses atomic.Int64
}

func NewRedisBackend(cfg config.RedisConfig) (*RedisBackend, error) {
    poolSize := cfg.PoolSize
    if poolSize <= 0 {
        poolSize = 10
    }
    client := redis.NewClient(&redis.Options{
        Addr:     cfg.Addr,
        Password: cfg.Password,
        DB:       cfg.DB,
        PoolSize: poolSize,
    })
    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()
    if err := client.Ping(ctx).Err(); err != nil {
        return nil, fmt.Errorf("redis ping failed: %w", err)
    }
    slog.Info("redis cache backend connected", "addr", cfg.Addr, "db", cfg.DB)
    return &RedisBackend{
        client: client,
        prefix: "fusion:cache:",
    }, nil
}

// recordHit/recordMiss are the hot-path counter bumps Get makes on every
// request goroutine. E2: kept as methods (not inline `r.hits++`) so the race
// guard exercises the REAL increment path and the race detector fires when
// the fields revert to plain int64. atomic.Int64.Add is lock-free.
func (r *RedisBackend) recordHit()  { r.hits.Add(1) }
func (r *RedisBackend) recordMiss() { r.misses.Add(1) }

func (r *RedisBackend) Get(key string) ([]byte, bool) {
    ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
    defer cancel()
    val, err := r.client.Get(ctx, r.prefix+key).Bytes()
    if err != nil {
        r.recordMiss()
        return nil, false
    }
    r.recordHit()
    return val, true
}

func (r *RedisBackend) Set(key string, value []byte, ttl time.Duration) {
    ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
    defer cancel()
    if err := r.client.Set(ctx, r.prefix+key, value, ttl).Err(); err != nil {
        slog.Error("redis set failed", "key", key, "error", err)
    }
}

func (r *RedisBackend) Delete(key string) {
    ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
    defer cancel()
    if err := r.client.Del(ctx, r.prefix+key).Err(); err != nil {
        slog.Error("redis delete failed", "key", key, "error", err)
    }
}

func (r *RedisBackend) Stats() (hits, misses int64, size int) {
    ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
    defer cancel()
    dbsize, err := r.client.DBSize(ctx).Result()
    if err != nil {
        slog.Error("redis dbsize failed", "error", err)
    }
    // E2: hits/misses are atomic.Int64 — Load() is the race-free read.
    return r.hits.Load(), r.misses.Load(), int(dbsize)
}

// redisCounterSnapshot is the counter-only view of Stats(), with no redis
// client access. E2: lets the race guard exercise the hits/misses atomic
// fields against a zero-value RedisBackend (nil client) without panicking on
// DBSize. Stats() delegates the counter loads to the same atomic reads.
type redisCounterSnapshot struct {
    hits   int64
    misses int64
}

func (r *RedisBackend) StatsCounterSnapshot() redisCounterSnapshot {
    return redisCounterSnapshot{hits: r.hits.Load(), misses: r.misses.Load()}
}

func (r *RedisBackend) Close() {
    if err := r.client.Close(); err != nil {
        slog.Error("redis close failed", "error", err)
    }
}
