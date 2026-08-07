package redis

import (
    "context"
    "encoding/json"
    "fmt"
    "log/slog"
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
)

type RedisStore struct {
    client *redis.Client
    ctx    context.Context
}

func NewRedisStore(addr, password string, db int) (*RedisStore, error) {
    client := redis.NewClient(&redis.Options{
        Addr:     addr,
        Password: password,
        DB:       db,
    })
    ctx := context.Background()
    if err := client.Ping(ctx).Err(); err != nil {
        return nil, fmt.Errorf("redis ping failed: %w", err)
    }
    slog.Info("redis store connected", "addr", addr)
    return &RedisStore{client: client, ctx: ctx}, nil
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
    pipe := r.client.Pipeline()
    pipe.Set(r.ctx, key, data, 72*time.Hour)
    pipe.ZAdd(r.ctx, logPrefix+"index", redis.Z{
        Score:  float64(log.Timestamp.Unix()),
        Member: log.ID,
    })
    _, err = pipe.Exec(r.ctx)
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
    ids, err := r.client.ZRangeArgs(r.ctx, redis.ZRangeArgs{
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
    total, _ := r.client.ZCount(r.ctx, logPrefix+"index", min, max).Result()
    var logs []*store.RequestLog
    for _, id := range ids {
        data, err := r.client.Get(r.ctx, logPrefix+id).Bytes()
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
    data, err := r.client.Get(r.ctx, logPrefix+id).Bytes()
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

// --- API Keys ---

func (r *RedisStore) ListKeys() ([]*store.APIKeyEntry, error) {
    keys, err := r.client.Keys(r.ctx, apiKeyPrefix+"*").Result()
    if err != nil {
        return nil, err
    }
    var result []*store.APIKeyEntry
    for _, k := range keys {
        data, err := r.client.Get(r.ctx, k).Bytes()
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
    data, err := r.client.Get(r.ctx, apiKeyPrefix+name).Bytes()
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
    name, err := r.client.Get(r.ctx, apiKeyHashPrefix+hash).Result()
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
    if err := r.client.Set(r.ctx, apiKeyPrefix+key.Name, data, 0).Err(); err != nil {
        return err
    }
    if key.KeyHash != "" {
        if err := r.client.Set(r.ctx, apiKeyHashPrefix+key.KeyHash, key.Name, 0).Err(); err != nil {
            slog.Warn("failed to index key hash", "key", key.Name, "error", err)
        }
    }
    return nil
}

func (r *RedisStore) UpdateKey(key *store.APIKeyEntry) error {
    return r.CreateKey(key)
}

func (r *RedisStore) DeleteKey(name string) error {
    existing, err := r.GetKey(name)
    if err == nil && existing.KeyHash != "" {
        r.client.Del(r.ctx, apiKeyHashPrefix+existing.KeyHash)
    }
    return r.client.Del(r.ctx, apiKeyPrefix+name).Err()
}

// --- Channels ---

func (r *RedisStore) ListChannels() ([]*store.ChannelEntry, error) {
    keys, err := r.client.Keys(r.ctx, channelPrefix+"*").Result()
    if err != nil {
        return nil, err
    }
    var result []*store.ChannelEntry
    for _, k := range keys {
        data, err := r.client.Get(r.ctx, k).Bytes()
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
    data, err := r.client.Get(r.ctx, channelPrefix+name).Bytes()
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
    return r.client.Set(r.ctx, channelPrefix+ch.Name, data, 0).Err()
}

func (r *RedisStore) UpdateChannel(ch *store.ChannelEntry) error {
    return r.CreateChannel(ch)
}

func (r *RedisStore) DeleteChannel(name string) error {
    return r.client.Del(r.ctx, channelPrefix+name).Err()
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
    key, kerr := r.GetKey(keyName)
    if kerr != nil {
        return 0, 0, false, kerr
    }
    return key.QuotaUsed, key.QuotaLimit, key.QuotaUsed >= key.QuotaLimit, nil
}

func (r *RedisStore) DeductQuota(keyName string, amount float64) error {
    key, err := r.GetKey(keyName)
    if err != nil {
        return err
    }
    key.QuotaUsed += amount
    key.QuotaRemaining = key.QuotaLimit - key.QuotaUsed
    return r.UpdateKey(key)
}

// --- Teams ---

func (r *RedisStore) CreateTeam(team *store.Team) error {
    data, err := json.Marshal(team)
    if err != nil {
        return err
    }
    return r.client.Set(r.ctx, teamPrefix+team.ID, data, 0).Err()
}

func (r *RedisStore) GetTeam(id string) (*store.Team, error) {
    data, err := r.client.Get(r.ctx, teamPrefix+id).Bytes()
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
    keys, err := r.client.Keys(r.ctx, teamPrefix+"*").Result()
    if err != nil {
        return nil, err
    }
    var result []*store.Team
    for _, k := range keys {
        data, err := r.client.Get(r.ctx, k).Bytes()
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
    return r.client.Del(r.ctx, teamPrefix+id).Err()
}

func (r *RedisStore) BindKeyToTeam(apiKey, teamID string) error {
    return r.client.Set(r.ctx, keyTeamIndex+apiKey, teamID, 0).Err()
}

func (r *RedisStore) GetTeamByKey(apiKey string) (*store.Team, error) {
    teamID, err := r.client.Get(r.ctx, keyTeamIndex+apiKey).Result()
    if err != nil {
        return nil, fmt.Errorf("no team for key")
    }
    return r.GetTeam(teamID)
}

func (r *RedisStore) AddTeamCost(teamID string, cost float64) error {
    team, err := r.GetTeam(teamID)
    if err != nil {
        return err
    }
    team.QuotaUsed += cost
    team.CostAccumulated += cost
    team.UpdatedAt = time.Now()
    return r.UpdateTeam(team)
}

func (r *RedisStore) CheckTeamQuota(teamID string) (limit, used float64, ok bool, err error) {
    team, err := r.GetTeam(teamID)
    if err != nil {
        return 0, 0, false, err
    }
    return team.QuotaLimit, team.QuotaUsed, team.QuotaUsed < team.QuotaLimit, nil
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
    return r.client.Set(r.ctx, orgPrefix+org.ID, data, 0).Err()
}

func (r *RedisStore) GetOrg(id string) (*store.Organization, error) {
    data, err := r.client.Get(r.ctx, orgPrefix+id).Bytes()
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
    keys, err := r.client.Keys(r.ctx, orgPrefix+"*").Result()
    if err != nil {
        return nil, err
    }
    var result []*store.Organization
    for _, k := range keys {
        data, err := r.client.Get(r.ctx, k).Bytes()
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
    return r.client.Del(r.ctx, orgPrefix+id).Err()
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
    if err := r.client.Set(r.ctx, batchPrefix+id, data, 0).Err(); err != nil {
        return nil, err
    }
    slog.Info("batch created in redis", "id", id)
    return b, nil
}

func (r *RedisStore) GetBatch(id string) (*store.Batch, error) {
    data, err := r.client.Get(r.ctx, batchPrefix+id).Bytes()
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
    keys, err := r.client.Keys(r.ctx, batchPrefix+"*").Result()
    if err != nil {
        return nil, err
    }
    var result []*store.Batch
    for _, k := range keys {
        data, err := r.client.Get(r.ctx, k).Bytes()
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
    return r.client.Set(r.ctx, batchPrefix+batch.ID, data, 0).Err()
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
    pipe := r.client.Pipeline()
    pipe.Set(r.ctx, costPrefix+"rec:"+id, data, 72*time.Hour)
    pipe.ZAdd(r.ctx, costByTime, redis.Z{
        Score:  float64(rec.Timestamp.Unix()),
        Member: id,
    })
    _, err = pipe.Exec(r.ctx)
    return err
}

func (r *RedisStore) GetCostSummary(keyName string) (*store.CostSummary, error) {
    ids, err := r.client.ZRange(r.ctx, costByTime, 0, -1).Result()
    if err != nil {
        return nil, err
    }
    s := &store.CostSummary{
        ByKey:     make(map[string]float64),
        ByBackend: make(map[string]float64),
        ByModel:   make(map[string]float64),
    }
    for _, id := range ids {
        data, err := r.client.Get(r.ctx, costPrefix+"rec:"+id).Bytes()
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
