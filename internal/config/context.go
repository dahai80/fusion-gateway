package config

import "context"

type contextKey string

const (
    ConfigSnapshotKey contextKey = "config_snapshot"
    ConfigVersionKey  contextKey = "config_version"
)

func WithSnapshot(ctx context.Context, snap *ConfigSnapshot) context.Context {
    ctx = context.WithValue(ctx, ConfigSnapshotKey, snap)
    ctx = context.WithValue(ctx, ConfigVersionKey, snap.Version)
    return ctx
}

func SnapshotFromContext(ctx context.Context) *ConfigSnapshot {
    snap, ok := ctx.Value(ConfigSnapshotKey).(*ConfigSnapshot)
    if !ok {
        return GetSnapshot()
    }
    return snap
}

func VersionFromContext(ctx context.Context) uint64 {
    v, ok := ctx.Value(ConfigVersionKey).(uint64)
    if !ok {
        return 0
    }
    return v
}
