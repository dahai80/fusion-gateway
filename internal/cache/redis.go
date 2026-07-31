package cache

import (
    "context"
    "fmt"
    "log/slog"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/redis/go-redis/v9"
)

type RedisBackend struct {
    client *redis.Client
    prefix string
    hits   int64
    misses int64
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

func (r *RedisBackend) Get(key string) ([]byte, bool) {
    ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
    defer cancel()
    val, err := r.client.Get(ctx, r.prefix+key).Bytes()
    if err != nil {
        r.misses++
        return nil, false
    }
    r.hits++
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
    return r.hits, r.misses, int(dbsize)
}

func (r *RedisBackend) Close() {
    if err := r.client.Close(); err != nil {
        slog.Error("redis close failed", "error", err)
    }
}
