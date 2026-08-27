package redis

import (
    "context"
    "encoding/json"
    "fmt"
    "log/slog"
    "sort"
    "strconv"
    "time"

    "github.com/redis/go-redis/v9"

    "github.com/fusion-gateway/fusion-gateway/internal/store"
)

const (
    teamPrefix    = "fusion:team:"
    orgPrefix     = "fusion:org:"
    batchPrefix   = "fusion:batch:"
    costPrefix    = "fusion:cost:"
    costByTime    = "fusion:cost:by_time"
    keyTeamIndex  = "fusion:key_team:"
    logPrefix     = "fusion:log:"
    apiKeyPrefix     = "fusion:key:"
    apiKeyHashPrefix = "fusion:keyhash:"
    channelPrefix = "fusion:channel:"
    // AH1 (audit P1): dedicated quota usage counter, kept OUTSIDE the key JSON
    // blob. The prior DeductQuota was a GetKey -> mutate QuotaUsed -> UpdateKey
    // RMW on the blob — non-atomic, so two concurrent gateway instances both
    // read the same QuotaUsed, each adds its amount, each writes back: one
    // increment is lost. Across N instances the budget is overshot N-fold
    // (billing bypass / quota double-spend). A dedicated float counter
    // incremented by atomic INCRBYFLOAT fixes the lost-increment defect.
    // Match the memory store contract: DeductQuota is additive (limit
    // enforcement lives in CheckQuota, not the deduction), so no Lua
    // check-then-deduct is needed.
    quotaUsedPrefix = "fusion:quota:used:"
    // R4 (audit P1): dedicated team cost counters, kept OUTSIDE the team JSON
    // blob for the same reason as quotaUsedPrefix (AH1). The prior AddTeamCost
    // was a GetTeam -> mutate QuotaUsed+CostAccumulated -> UpdateTeam RMW on the
    // blob — non-atomic, so two concurrent gateway instances both read the same
    // values, each adds its cost, each writes back: one cost is lost (team
    // billing never reconciles across a multi-gateway + Redis deployment). Two
    // dedicated float counters incremented atomically fix the lost-cost defect.
    // A Lua script applies BOTH increments in one atomic Redis op so the two
    // counters cannot diverge on a partial failure (quota-used and lifetime
    // cost stay consistent).
    teamQuotaUsedPrefix = "fusion:team:quota_used:"
    teamCostAccumPrefix = "fusion:team:cost_accum:"
)

type RedisStore struct {
    client *redis.Client
}

// redisOpTimeout is the per-call deadline applied to every store command (B3).
// Redis is an "optional" store (memory is the default), but once configured it
// is a hard dependency on the auth path (GetKeyByHash serves every API-key
// request). A stalled Redis (BGSAVE, slow command, network blip) without a
// per-call timeout blocks the calling goroutine indefinitely — cascading to a
// full gateway stall. 3s bounds any single op; the redis.Options Read/Write
// timeouts are the backstop for commands that ignore the context deadline.
const redisOpTimeout = 3 * time.Second

// redisListTimeout is the per-call deadline for SCAN-based list operations
// (ListKeys/ListChannels/...). These iterate an unbounded keyspace, so they
// get a longer budget than single-key ops but are still bounded.
const redisListTimeout = 10 * time.Second

func NewRedisStore(addr, password string, db int) (*RedisStore, error) {
    client := redis.NewClient(&redis.Options{
        Addr:         addr,
        Password:     password,
        DB:           db,
        DialTimeout:  3 * time.Second,
        ReadTimeout:  3 * time.Second, // B3: backstop for context-ignoring commands
        WriteTimeout: 3 * time.Second,
        PoolTimeout:  4 * time.Second,
    })
    ctx, cancel := context.WithTimeout(context.Background(), redisOpTimeout)
    defer cancel()
    if err := client.Ping(ctx).Err(); err != nil {
        return nil, fmt.Errorf("redis ping failed: %w", err)
    }
    slog.Info("redis store connected", "addr", addr)
    return &RedisStore{client: client}, nil
}

// withTimeout derives a per-call context with a deadline. B3: every store
// method MUST use this instead of a shared background context, so a stalled
// Redis cannot block a goroutine forever. The caller defers cancel().
func (r *RedisStore) withTimeout(dur time.Duration) (context.Context, context.CancelFunc) {
    return context.WithTimeout(context.Background(), dur)
}

// scanKeys lists all keys matching a glob pattern via SCAN (B3). KEYS is O(N)
// and blocks the Redis event loop — a footgun once the keyspace grows (logs
// have a 72h TTL, easily reaching millions of keys). SCAN is cursor-based,
// non-blocking, and bounded by redisListTimeout. Returns the full key set
// (the keyspace is small enough that collecting in memory is fine).
func (r *RedisStore) scanKeys(match string) ([]string, error) {
    ctx, cancel := r.withTimeout(redisListTimeout)
    defer cancel()
    var keys []string
    var cursor uint64
    for {
        batch, cur, err := r.client.Scan(ctx, cursor, match, 256).Result()
        if err != nil {
            return nil, fmt.Errorf("scan %s: %w", match, err)
        }
        keys = append(keys, batch...)
        if cur == 0 {
            break
        }
        cursor = cur
    }
    return keys, nil
}

func (r *RedisStore) Close() error {
    return r.client.Close()
}

// --- Logs ---

func (r *RedisStore) AppendLog(log *store.RequestLog) error {
    data, err := json.Marshal(log)
    if err != nil {
        return fmt.Errorf("marshal log: %w", err)
    }
    key := logPrefix + log.ID
    ctx, cancel := r.withTimeout(redisOpTimeout)
    defer cancel()
    pipe := r.client.Pipeline()
    pipe.Set(ctx, key, data, 72*time.Hour)
    pipe.ZAdd(ctx, logPrefix+"index", redis.Z{
        Score:  float64(log.Timestamp.Unix()),
        Member: log.ID,
    })
    _, err = pipe.Exec(ctx)
    return err
}

func (r *RedisStore) QueryLogs(filter store.LogFilter) ([]*store.RequestLog, int, error) {
    min := "-inf"
    max := "+inf"
    if filter.StartTime != nil {
        min = strconv.FormatInt(filter.StartTime.Unix(), 10)
    }
    if filter.EndTime != nil {
        max = strconv.FormatInt(filter.EndTime.Unix(), 10)
    }
    ctx, cancel := r.withTimeout(redisOpTimeout)
    defer cancel()
    ids, err := r.client.ZRangeArgs(ctx, redis.ZRangeArgs{
        Key:     logPrefix + "index",
        Start:   min,
        Stop:    max,
        ByScore: true,
        Offset:  int64(filter.Page * filter.PageSize),
        Count:   int64(filter.PageSize),
    }).Result()
    if err != nil {
        return nil, 0, err
    }
    total, _ := r.client.ZCount(ctx, logPrefix+"index", min, max).Result()
    var logs []*store.RequestLog
    for _, id := range ids {
        data, err := r.client.Get(ctx, logPrefix+id).Bytes()
        if err != nil {
            continue
        }
        var l store.RequestLog
        if err := json.Unmarshal(data, &l); err != nil {
            continue
        }
        logs = append(logs, &l)
    }
    return logs, int(total), nil
}

func (r *RedisStore) GetLog(id string) (*store.RequestLog, error) {
    ctx, cancel := r.withTimeout(redisOpTimeout)
    defer cancel()
    data, err := r.client.Get(ctx, logPrefix+id).Bytes()
    if err != nil {
        return nil, fmt.Errorf("log %s not found", id)
    }
    var l store.RequestLog
    if err := json.Unmarshal(data, &l); err != nil {
        return nil, fmt.Errorf("unmarshal log: %w", err)
    }
    return &l, nil
}

func (r *RedisStore) ExportLogs(filter store.LogFilter, format string) ([]byte, error) {
    logs, _, err := r.QueryLogs(filter)
    if err != nil {
        return nil, err
    }
    return json.Marshal(logs)
}

func (r *RedisStore) DistinctLogFilters() (*store.LogFilters, error) {
    keys, err := r.scanKeys(logPrefix + "*")
    if err != nil {
        return nil, fmt.Errorf("list log keys: %w", err)
    }

    ctx, cancel := r.withTimeout(redisListTimeout)
    defer cancel()
    models := make(map[string]struct{})
    channels := make(map[string]struct{})
    for _, k := range keys {
        data, err := r.client.Get(ctx, k).Bytes()
        if err != nil {
            continue
        }
        var l store.RequestLog
        if err := json.Unmarshal(data, &l); err != nil {
            continue
        }
        if l.Model != "" {
            models[l.Model] = struct{}{}
        }
        if l.ChannelName != "" {
            channels[l.ChannelName] = struct{}{}
        }
    }

    out := &store.LogFilters{
        Models:   make([]string, 0, len(models)),
        Channels: make([]string, 0, len(channels)),
    }
    for m := range models {
        out.Models = append(out.Models, m)
    }
    for c := range channels {
        out.Channels = append(out.Channels, c)
    }
    sort.Strings(out.Models)
    sort.Strings(out.Channels)
    slog.Debug("distinct log filters", "models", len(out.Models), "channels", len(out.Channels))
    return out, nil
}

// --- API Keys ---

func (r *RedisStore) ListKeys() ([]*store.APIKeyEntry, error) {
    keys, err := r.scanKeys(apiKeyPrefix + "*")
    if err != nil {
        return nil, err
    }
    ctx, cancel := r.withTimeout(redisListTimeout)
    defer cancel()
    var result []*store.APIKeyEntry
    for _, k := range keys {
        data, err := r.client.Get(ctx, k).Bytes()
        if err != nil {
            continue
        }
        var entry store.APIKeyEntry
        if err := json.Unmarshal(data, &entry); err != nil {
            continue
        }
        result = append(result, &entry)
    }
    return result, nil
}

func (r *RedisStore) GetKey(name string) (*store.APIKeyEntry, error) {
    ctx, cancel := r.withTimeout(redisOpTimeout)
    defer cancel()
    data, err := r.client.Get(ctx, apiKeyPrefix+name).Bytes()
    if err != nil {
        return nil, fmt.Errorf("key %s not found", name)
    }
    var entry store.APIKeyEntry
    if err := json.Unmarshal(data, &entry); err != nil {
        return nil, err
    }
    return &entry, nil
}

func (r *RedisStore) GetKeyByHash(hash string) (*store.APIKeyEntry, error) {
    ctx, cancel := r.withTimeout(redisOpTimeout)
    defer cancel()
    name, err := r.client.Get(ctx, apiKeyHashPrefix+hash).Result()
    if err != nil {
        return nil, fmt.Errorf("key not found by hash")
    }
    return r.GetKey(name)
}

func (r *RedisStore) CreateKey(key *store.APIKeyEntry) error {
    data, err := json.Marshal(key)
    if err != nil {
        return err
    }
    ctx, cancel := r.withTimeout(redisOpTimeout)
    defer cancel()
    if err := r.client.Set(ctx, apiKeyPrefix+key.Name, data, 0).Err(); err != nil {
        return err
    }
    if key.KeyHash != "" {
        if err := r.client.Set(ctx, apiKeyHashPrefix+key.KeyHash, key.Name, 0).Err(); err != nil {
            slog.Warn("failed to index key hash", "key", key.Name, "error", err)
        }
    }
    // AH1 (audit P1): seed the usage counter from the blob's QuotaUsed only if
    // the counter is absent (SetNX never overwrites) — the counter is the
    // source of truth once any deduction ran; never overwrite it with a stale
    // blob value. The budget limit stays read from the blob in CheckQuota; no
    // separate limit key is needed.
    if _, err := r.client.SetNX(ctx, quotaUsedPrefix+key.Name, key.QuotaUsed, 0).Result(); err != nil {
        slog.Warn("failed to seed quota used counter", "key", key.Name, "error", err)
    }
    return nil
}

func (r *RedisStore) UpdateKey(key *store.APIKeyEntry) error {
    return r.CreateKey(key)
}

func (r *RedisStore) DeleteKey(name string) error {
    ctx, cancel := r.withTimeout(redisOpTimeout)
    defer cancel()
    existing, err := r.GetKey(name)
    if err == nil && existing.KeyHash != "" {
        r.client.Del(ctx, apiKeyHashPrefix+existing.KeyHash)
    }
    if err := r.client.Del(ctx, apiKeyPrefix+name).Err(); err != nil {
        return err
    }
    // EI5: cascade-delete the per-key quota usage counter (AH1 dedicated
    // fusion:quota:used:<name>). Without this a deleted key left its quota
    // counter behind forever — orphaned Redis keys accumulate over long-running
    // deployments with frequent key churn. Best-effort Del after the key entry
    // is gone; a missing counter is a no-op (Del on a non-existent key is not
    // an error in Redis).
    if err := r.client.Del(ctx, quotaUsedPrefix+name).Err(); err != nil {
        slog.Warn("failed to reclaim quota counter for deleted key", "key", name, "error", err)
    }
    return nil
}

// --- Channels ---

func (r *RedisStore) ListChannels() ([]*store.ChannelEntry, error) {
    keys, err := r.scanKeys(channelPrefix + "*")
    if err != nil {
        return nil, err
    }
    ctx, cancel := r.withTimeout(redisListTimeout)
    defer cancel()
    var result []*store.ChannelEntry
    for _, k := range keys {
        data, err := r.client.Get(ctx, k).Bytes()
        if err != nil {
            continue
        }
        var entry store.ChannelEntry
        if err := json.Unmarshal(data, &entry); err != nil {
            continue
        }
        result = append(result, &entry)
    }
    return result, nil
}

func (r *RedisStore) GetChannel(name string) (*store.ChannelEntry, error) {
    ctx, cancel := r.withTimeout(redisOpTimeout)
    defer cancel()
    data, err := r.client.Get(ctx, channelPrefix+name).Bytes()
    if err != nil {
        return nil, fmt.Errorf("channel %s not found", name)
    }
    var entry store.ChannelEntry
    if err := json.Unmarshal(data, &entry); err != nil {
        return nil, err
    }
    return &entry, nil
}

func (r *RedisStore) CreateChannel(ch *store.ChannelEntry) error {
    data, err := json.Marshal(ch)
    if err != nil {
        return err
    }
    ctx, cancel := r.withTimeout(redisOpTimeout)
    defer cancel()
    return r.client.Set(ctx, channelPrefix+ch.Name, data, 0).Err()
}

func (r *RedisStore) UpdateChannel(ch *store.ChannelEntry) error {
    return r.CreateChannel(ch)
}

func (r *RedisStore) DeleteChannel(name string) error {
    ctx, cancel := r.withTimeout(redisOpTimeout)
    defer cancel()
    return r.client.Del(ctx, channelPrefix+name).Err()
}

// --- Analytics ---

func (r *RedisStore) GetTokenStats(from, to time.Time, groupBy string) ([]*store.TokenStat, error) {
    return nil, nil
}

func (r *RedisStore) GetCostStats(from, to time.Time, groupBy string) ([]*store.CostStat, error) {
    return nil, nil
}

func (r *RedisStore) GetModelStats(from, to time.Time) ([]*store.ModelStat, error) {
    return nil, nil
}

func (r *RedisStore) GetLatencyStats(from, to time.Time) ([]*store.LatencyStat, error) {
    return nil, nil
}

func (r *RedisStore) GetErrorStats(from, to time.Time) ([]*store.ErrorStat, error) {
    return nil, nil
}

func (r *RedisStore) GetDashboardOverview() (*store.DashboardOverview, error) {
    return &store.DashboardOverview{}, nil
}

func (r *RedisStore) GetKeyProfitStats(from, to time.Time) ([]*store.KeyProfitStat, error) {
    return []*store.KeyProfitStat{}, nil
}

// --- Quota ---

func (r *RedisStore) CheckQuota(keyName string) (used, limit float64, exceeded bool, err error) {
    // AH1 (audit P1): read the authoritative usage COUNTER, not the QuotaUsed
    // field baked into the key blob. The blob is a non-atomic RMW victim; the
    // counter is incremented by DeductQuota atomically. Missing key is a hard
    // error (matches the memory store + admin contract) — fail visibly, never
    // silently gate an unknown key open.
    ctx, cancel := r.withTimeout(redisOpTimeout)
    defer cancel()
    key, err := r.GetKey(keyName)
    if err != nil {
        return 0, 0, false, fmt.Errorf("quota check: key %s not found", keyName)
    }
    usedStr, err := r.client.Get(ctx, quotaUsedPrefix+keyName).Result()
    if err != nil && err != redis.Nil {
        return 0, 0, false, fmt.Errorf("quota used read failed: %w", err)
    }
    used, _ = strconv.ParseFloat(usedStr, 64)
    limit = key.BudgetLimit
    if limit <= 0 {
        limit = key.QuotaLimit
    }
    exceeded = limit > 0 && used >= limit
    return used, limit, exceeded, nil
}

func (r *RedisStore) DeductQuota(keyName string, amount float64) error {
    // AH1 (audit P1): atomic INCRBYFLOAT on the dedicated usage counter, NOT a
    // GetKey -> QuotaUsed += amount -> UpdateKey blob RMW. The RMW lost an
    // increment on every concurrent deduction across gateway instances (quota
    // double-spend): both read the same QuotaUsed, each adds its amount, each
    // writes back — one increment lost. INCRBYFLOAT is a single atomic Redis
    // op, so no increment is lost even across N instances. Additive only —
    // limit enforcement lives in CheckQuota (matches the memory store
    // contract); DeductQuota is admin-manual, fail visibly on missing key.
    ctx, cancel := r.withTimeout(redisOpTimeout)
    defer cancel()
    if _, err := r.client.Get(ctx, apiKeyPrefix+keyName).Result(); err != nil {
        return fmt.Errorf("quota deduct: key %s not found", keyName)
    }
    if err := r.client.IncrByFloat(ctx, quotaUsedPrefix+keyName, amount).Err(); err != nil {
        return fmt.Errorf("quota deduct failed: %w", err)
    }
    slog.Debug("quota deducted atomically", "key", keyName, "amount", amount)
    return nil
}

// --- Teams ---

func (r *RedisStore) CreateTeam(team *store.Team) error {
    data, err := json.Marshal(team)
    if err != nil {
        return err
    }
    ctx, cancel := r.withTimeout(redisOpTimeout)
    defer cancel()
    return r.client.Set(ctx, teamPrefix+team.ID, data, 0).Err()
}

func (r *RedisStore) GetTeam(id string) (*store.Team, error) {
    ctx, cancel := r.withTimeout(redisOpTimeout)
    defer cancel()
    data, err := r.client.Get(ctx, teamPrefix+id).Bytes()
    if err != nil {
        return nil, fmt.Errorf("team %s not found", id)
    }
    var team store.Team
    if err := json.Unmarshal(data, &team); err != nil {
        return nil, err
    }
    return &team, nil
}

func (r *RedisStore) ListTeams() ([]*store.Team, error) {
    keys, err := r.scanKeys(teamPrefix + "*")
    if err != nil {
        return nil, err
    }
    ctx, cancel := r.withTimeout(redisListTimeout)
    defer cancel()
    var result []*store.Team
    for _, k := range keys {
        data, err := r.client.Get(ctx, k).Bytes()
        if err != nil {
            continue
        }
        var team store.Team
        if err := json.Unmarshal(data, &team); err != nil {
            continue
        }
        result = append(result, &team)
    }
    return result, nil
}

func (r *RedisStore) UpdateTeam(team *store.Team) error {
    return r.CreateTeam(team)
}

func (r *RedisStore) DeleteTeam(id string) error {
    ctx, cancel := r.withTimeout(redisOpTimeout)
    defer cancel()
    return r.client.Del(ctx, teamPrefix+id).Err()
}

func (r *RedisStore) BindKeyToTeam(apiKey, teamID string) error {
    ctx, cancel := r.withTimeout(redisOpTimeout)
    defer cancel()
    return r.client.Set(ctx, keyTeamIndex+apiKey, teamID, 0).Err()
}

func (r *RedisStore) GetTeamByKey(apiKey string) (*store.Team, error) {
    ctx, cancel := r.withTimeout(redisOpTimeout)
    defer cancel()
    teamID, err := r.client.Get(ctx, keyTeamIndex+apiKey).Result()
    if err != nil {
        return nil, fmt.Errorf("no team for key")
    }
    return r.GetTeam(teamID)
}

// addTeamCostScript atomically increments BOTH the team quota-used counter and
// the lifetime cost-accumulated counter in a single Redis EVAL. R4 (audit P1):
// two separate INCRBYFLOAT calls are each atomic but not transactional — if the
// second failed after the first succeeded, the two counters diverge (quota used
// charged but lifetime cost not recorded, or vice versa). KEYS[1]=quota-used,
// KEYS[2]=cost-accum, ARGV[1]=cost. Returns the new quota-used so the caller
// can log it; the script itself cannot error on a well-formed INCRBYFLOAT.
const addTeamCostScript = `
local used = redis.call('INCRBYFLOAT', KEYS[1], ARGV[1])
redis.call('INCRBYFLOAT', KEYS[2], ARGV[1])
return used
`

func (r *RedisStore) AddTeamCost(teamID string, cost float64) error {
    // R4 (audit P1): atomic Lua INCRBYFLOAT on dedicated counters, NOT a
    // GetTeam -> mutate QuotaUsed+CostAccumulated -> UpdateTeam blob RMW. The
    // RMW lost a cost on every concurrent AddCost across gateway instances
    // (team billing double-spend): both read the same values, each adds its
    // cost, each writes back — one cost lost. The Lua script applies both
    // increments in one atomic Redis op so the counters stay consistent. The
    // team blob's QuotaUsed/CostAccumulated fields are NOT updated here (they
    // are the non-atomic RMW victim, same tradeoff as AH1's per-key path); the
    // dedicated counters are the authoritative source read by CheckTeamQuota.
    ctx, cancel := r.withTimeout(redisOpTimeout)
    defer cancel()
    if _, err := r.client.Get(ctx, teamPrefix+teamID).Result(); err != nil {
        return fmt.Errorf("add team cost: team %s not found", teamID)
    }
    quotaKey := teamQuotaUsedPrefix + teamID
    costKey := teamCostAccumPrefix + teamID
    if err := r.client.Eval(ctx, addTeamCostScript, []string{quotaKey, costKey}, cost).Err(); err != nil {
        return fmt.Errorf("add team cost atomic increment failed: %w", err)
    }
    slog.Debug("team cost added atomically", "team", teamID, "amount", cost)
    return nil
}

func (r *RedisStore) CheckTeamQuota(teamID string) (limit, used float64, ok bool, err error) {
    // R4 (audit P1): read the authoritative quota-used COUNTER, not the
    // QuotaUsed field baked into the team blob. The blob is a non-atomic RMW
    // victim (AddTeamCost no longer writes it); the counter is incremented
    // atomically by the Lua script. The limit still comes from the blob
    // (admin-set, never mutated by the cost path). A missing counter reads as
    // 0 (no cost recorded yet) — matches the memory store's fresh-team
    // semantics, not a hard error.
    ctx, cancel := r.withTimeout(redisOpTimeout)
    defer cancel()
    team, err := r.GetTeam(teamID)
    if err != nil {
        return 0, 0, false, err
    }
    usedStr, err := r.client.Get(ctx, teamQuotaUsedPrefix+teamID).Result()
    if err != nil && err != redis.Nil {
        return 0, 0, false, fmt.Errorf("team quota used read failed: %w", err)
    }
    used, _ = strconv.ParseFloat(usedStr, 64)
    limit = team.QuotaLimit
    ok = used < limit
    return limit, used, ok, nil
}

func (r *RedisStore) AddTeamMember(teamID, userID, role string) error {
    team, err := r.GetTeam(teamID)
    if err != nil {
        return err
    }
    team.Members = append(team.Members, store.TeamMember{UserID: userID, Role: role})
    team.UpdatedAt = time.Now()
    return r.UpdateTeam(team)
}

func (r *RedisStore) RemoveTeamMember(teamID, userID string) error {
    team, err := r.GetTeam(teamID)
    if err != nil {
        return err
    }
    for i, m := range team.Members {
        if m.UserID == userID {
            team.Members = append(team.Members[:i], team.Members[i+1:]...)
            break
        }
    }
    team.UpdatedAt = time.Now()
    return r.UpdateTeam(team)
}

// --- Organizations ---

func (r *RedisStore) CreateOrg(org *store.Organization) error {
    data, err := json.Marshal(org)
    if err != nil {
        return err
    }
    ctx, cancel := r.withTimeout(redisOpTimeout)
    defer cancel()
    return r.client.Set(ctx, orgPrefix+org.ID, data, 0).Err()
}

func (r *RedisStore) GetOrg(id string) (*store.Organization, error) {
    ctx, cancel := r.withTimeout(redisOpTimeout)
    defer cancel()
    data, err := r.client.Get(ctx, orgPrefix+id).Bytes()
    if err != nil {
        return nil, fmt.Errorf("organization %s not found", id)
    }
    var org store.Organization
    if err := json.Unmarshal(data, &org); err != nil {
        return nil, err
    }
    return &org, nil
}

func (r *RedisStore) ListOrgs() ([]*store.Organization, error) {
    keys, err := r.scanKeys(orgPrefix + "*")
    if err != nil {
        return nil, err
    }
    ctx, cancel := r.withTimeout(redisListTimeout)
    defer cancel()
    var result []*store.Organization
    for _, k := range keys {
        data, err := r.client.Get(ctx, k).Bytes()
        if err != nil {
            continue
        }
        var org store.Organization
        if err := json.Unmarshal(data, &org); err != nil {
            continue
        }
        result = append(result, &org)
    }
    return result, nil
}

func (r *RedisStore) DeleteOrg(id string) error {
    ctx, cancel := r.withTimeout(redisOpTimeout)
    defer cancel()
    return r.client.Del(ctx, orgPrefix+id).Err()
}

// --- Batch ---

func (r *RedisStore) CreateBatch(requests []store.BatchRequest, endpoint, window string) (*store.Batch, error) {
    id := fmt.Sprintf("batch_%d", time.Now().UnixNano())
    b := &store.Batch{
        ID:               id,
        Status:           store.BatchStatusPending,
        Requests:         requests,
        Results:          make([]store.BatchResult, 0),
        Total:            len(requests),
        CreatedAt:        time.Now(),
        Endpoint:         endpoint,
        CompletionWindow: window,
    }
    data, err := json.Marshal(b)
    if err != nil {
        return nil, err
    }
    ctx, cancel := r.withTimeout(redisOpTimeout)
    defer cancel()
    if err := r.client.Set(ctx, batchPrefix+id, data, 0).Err(); err != nil {
        return nil, err
    }
    slog.Info("batch created in redis", "id", id)
    return b, nil
}

func (r *RedisStore) GetBatch(id string) (*store.Batch, error) {
    ctx, cancel := r.withTimeout(redisOpTimeout)
    defer cancel()
    data, err := r.client.Get(ctx, batchPrefix+id).Bytes()
    if err != nil {
        return nil, fmt.Errorf("batch %s not found", id)
    }
    var b store.Batch
    if err := json.Unmarshal(data, &b); err != nil {
        return nil, err
    }
    return &b, nil
}

func (r *RedisStore) ListBatches() ([]*store.Batch, error) {
    keys, err := r.scanKeys(batchPrefix + "*")
    if err != nil {
        return nil, err
    }
    ctx, cancel := r.withTimeout(redisListTimeout)
    defer cancel()
    var result []*store.Batch
    for _, k := range keys {
        data, err := r.client.Get(ctx, k).Bytes()
        if err != nil {
            continue
        }
        var b store.Batch
        if err := json.Unmarshal(data, &b); err != nil {
            continue
        }
        result = append(result, &b)
    }
    return result, nil
}

func (r *RedisStore) CancelBatch(id string) (*store.Batch, error) {
    b, err := r.GetBatch(id)
    if err != nil {
        return nil, err
    }
    if b.Status == store.BatchStatusCompleted || b.Status == store.BatchStatusCancelled {
        return b, fmt.Errorf("batch already %s", b.Status)
    }
    b.Status = store.BatchStatusCancelled
    now := time.Now()
    b.CompletedAt = &now
    if err := r.UpdateBatch(b); err != nil {
        return nil, err
    }
    return b, nil
}

func (r *RedisStore) UpdateBatch(batch *store.Batch) error {
    data, err := json.Marshal(batch)
    if err != nil {
        return err
    }
    ctx, cancel := r.withTimeout(redisOpTimeout)
    defer cancel()
    return r.client.Set(ctx, batchPrefix+batch.ID, data, 0).Err()
}

// --- Cost ---

func (r *RedisStore) RecordUsage(keyName, backend, model string, promptTokens, completionTokens int, costUSD float64) error {
    rec := store.UsageRecord{
        Timestamp:        time.Now(),
        KeyName:          keyName,
        Backend:          backend,
        Model:            model,
        PromptTokens:     promptTokens,
        CompletionTokens: completionTokens,
        TotalTokens:      promptTokens + completionTokens,
        CostUSD:          costUSD,
    }
    data, err := json.Marshal(rec)
    if err != nil {
        return err
    }
    id := fmt.Sprintf("%d", time.Now().UnixNano())
    ctx, cancel := r.withTimeout(redisOpTimeout)
    defer cancel()
    pipe := r.client.Pipeline()
    pipe.Set(ctx, costPrefix+"rec:"+id, data, 72*time.Hour)
    pipe.ZAdd(ctx, costByTime, redis.Z{
        Score:  float64(rec.Timestamp.Unix()),
        Member: id,
    })
    _, err = pipe.Exec(ctx)
    return err
}

func (r *RedisStore) GetCostSummary(keyName string) (*store.CostSummary, error) {
    ctx, cancel := r.withTimeout(redisListTimeout)
    defer cancel()
    ids, err := r.client.ZRange(ctx, costByTime, 0, -1).Result()
    if err != nil {
        return nil, err
    }
    s := &store.CostSummary{
        ByKey:     make(map[string]float64),
        ByBackend: make(map[string]float64),
        ByModel:   make(map[string]float64),
    }
    for _, id := range ids {
        data, err := r.client.Get(ctx, costPrefix+"rec:"+id).Bytes()
        if err != nil {
            continue
        }
        var rec store.UsageRecord
        if err := json.Unmarshal(data, &rec); err != nil {
            continue
        }
        if keyName != "" && rec.KeyName != keyName {
            continue
        }
        s.TotalCostUSD += rec.CostUSD
        s.ByKey[rec.KeyName] += rec.CostUSD
        s.ByBackend[rec.Backend] += rec.CostUSD
        s.ByModel[rec.Model] += rec.CostUSD
        s.TotalTokens += rec.TotalTokens
        s.TotalRequests++
    }
    return s, nil
}

func (r *RedisStore) GetCostSummaryAll() (*store.CostSummary, error) {
    return r.GetCostSummary("")
}
