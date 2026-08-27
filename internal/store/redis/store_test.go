package redis

import (
    "context"
    "encoding/json"
    "fmt"
    "log/slog"
    "strconv"
    "sync"
    "testing"
    "time"

    "github.com/alicebob/miniredis/v2"
    "github.com/redis/go-redis/v9"

    "github.com/fusion-gateway/fusion-gateway/internal/store"
)

func setupTestStore(t *testing.T) (*RedisStore, *miniredis.Miniredis) {
    t.Helper()
    mr, err := miniredis.Run()
    if err != nil {
        t.Fatalf("miniredis start failed: %v", err)
    }
    rs, err := NewRedisStore(mr.Addr(), "", 0)
    if err != nil {
        mr.Close()
        t.Fatalf("redis store connect failed: %v", err)
    }
    return rs, mr
}

func teardownTestStore(rs *RedisStore, mr *miniredis.Miniredis) {
    rs.Close()
    mr.Close()
}

func TestNewRedisStore_ConnectionFailure(t *testing.T) {
    _, err := NewRedisStore("127.0.0.1:1", "", 0)
    if err == nil {
        t.Fatal("expected error for unreachable redis")
    }
    slog.Info("connection failure test passed", "error", err)
}

func TestRedisStore_Close(t *testing.T) {
    rs, mr := setupTestStore(t)
    defer mr.Close()
    if err := rs.Close(); err != nil {
        t.Fatalf("close failed: %v", err)
    }
}

func TestRedisStore_Logs_CRUD(t *testing.T) {
    rs, mr := setupTestStore(t)
    defer teardownTestStore(rs, mr)

    now := time.Now()
    log := &store.RequestLog{
        ID:          "log-1",
        RequestID:   "req-1",
        Timestamp:   now,
        APIKeyName:  "key1",
        Model:       "gpt-4",
        RequestType: "chat",
        IsStream:    true,
        ChannelName: "openai",
        ChannelType: "cloud",
        InputTokens: 100,
        OutputTokens: 50,
        TotalTokens: 150,
        Cost:        0.01,
        IsSuccess:   true,
        StatusCode:  200,
    }
    if err := rs.AppendLog(log); err != nil {
        t.Fatalf("AppendLog failed: %v", err)
    }

    got, err := rs.GetLog("log-1")
    if err != nil {
        t.Fatalf("GetLog failed: %v", err)
    }
    if got.Model != "gpt-4" {
        t.Fatalf("expected gpt-4, got %s", got.Model)
    }

    _, err = rs.GetLog("nonexistent")
    if err == nil {
        t.Fatal("expected error for missing log")
    }
}

func TestRedisStore_QueryLogs(t *testing.T) {
    rs, mr := setupTestStore(t)
    defer teardownTestStore(rs, mr)

    now := time.Now()
    _ = rs.AppendLog(&store.RequestLog{ID: "log-1", Model: "gpt-4", Timestamp: now.Add(-2 * time.Hour)})
    _ = rs.AppendLog(&store.RequestLog{ID: "log-2", Model: "claude-3", Timestamp: now})

    filter := store.LogFilter{Page: 0, PageSize: 10}
    logs, total, err := rs.QueryLogs(filter)
    if err != nil {
        t.Fatalf("QueryLogs failed: %v", err)
    }
    if total != 2 {
        t.Fatalf("expected 2, got %d", total)
    }
    if len(logs) != 2 {
        t.Fatalf("expected 2 logs, got %d", len(logs))
    }
}

func TestRedisStore_QueryLogs_TimeRange(t *testing.T) {
    rs, mr := setupTestStore(t)
    defer teardownTestStore(rs, mr)

    now := time.Now()
    _ = rs.AppendLog(&store.RequestLog{ID: "old", Model: "gpt-4", Timestamp: now.Add(-2 * time.Hour)})
    _ = rs.AppendLog(&store.RequestLog{ID: "new", Model: "gpt-4", Timestamp: now})

    start := now.Add(-time.Hour)
    filter := store.LogFilter{StartTime: &start, Page: 0, PageSize: 10}
    logs, total, _ := rs.QueryLogs(filter)
    if total != 1 {
        t.Fatalf("expected 1 with start time filter, got %d", total)
    }
    if len(logs) > 0 && logs[0].ID != "new" {
        t.Fatalf("expected new, got %s", logs[0].ID)
    }

    end := now.Add(-time.Hour)
    filter2 := store.LogFilter{EndTime: &end, Page: 0, PageSize: 10}
    _, total2, _ := rs.QueryLogs(filter2)
    if total2 != 1 {
        t.Fatalf("expected 1 with end time filter, got %d", total2)
    }
}

func TestRedisStore_ExportLogs(t *testing.T) {
    rs, mr := setupTestStore(t)
    defer teardownTestStore(rs, mr)

    _ = rs.AppendLog(&store.RequestLog{ID: "log-1", Model: "gpt-4", Timestamp: time.Now()})

    data, err := rs.ExportLogs(store.LogFilter{Page: 0, PageSize: 10}, "json")
    if err != nil {
        t.Fatalf("ExportLogs failed: %v", err)
    }
    var logs []store.RequestLog
    if err := json.Unmarshal(data, &logs); err != nil {
        t.Fatalf("unmarshal failed: %v", err)
    }
    if len(logs) != 1 {
        t.Fatalf("expected 1, got %d", len(logs))
    }
}

func TestRedisStore_Keys_CRUD(t *testing.T) {
    rs, mr := setupTestStore(t)
    defer teardownTestStore(rs, mr)

    key := &store.APIKeyEntry{
        Name:         "test-key",
        KeyPrefix:    "sk-",
        Status:       "active",
        QuotaLimit:   100.0,
        BudgetLimit:  50.0,
        AllowedModels: []string{"gpt-4"},
    }
    if err := rs.CreateKey(key); err != nil {
        t.Fatalf("CreateKey failed: %v", err)
    }

    got, err := rs.GetKey("test-key")
    if err != nil {
        t.Fatalf("GetKey failed: %v", err)
    }
    if got.Name != "test-key" {
        t.Fatalf("expected test-key, got %s", got.Name)
    }

    got.BudgetLimit = 200.0
    if err := rs.UpdateKey(got); err != nil {
        t.Fatalf("UpdateKey failed: %v", err)
    }
    updated, _ := rs.GetKey("test-key")
    if updated.BudgetLimit != 200.0 {
        t.Fatalf("expected 200, got %f", updated.BudgetLimit)
    }

    keys, err := rs.ListKeys()
    if err != nil {
        t.Fatalf("ListKeys failed: %v", err)
    }
    if len(keys) != 1 {
        t.Fatalf("expected 1 key, got %d", len(keys))
    }

    if err := rs.DeleteKey("test-key"); err != nil {
        t.Fatalf("DeleteKey failed: %v", err)
    }
    _, err = rs.GetKey("test-key")
    if err == nil {
        t.Fatal("expected error after delete")
    }
}

func TestRedisStore_GetKey_NotFound(t *testing.T) {
    rs, mr := setupTestStore(t)
    defer teardownTestStore(rs, mr)

    _, err := rs.GetKey("nonexistent")
    if err == nil {
        t.Fatal("expected error for missing key")
    }
}

func TestRedisStore_Channels_CRUD(t *testing.T) {
    rs, mr := setupTestStore(t)
    defer teardownTestStore(rs, mr)

    ch := &store.ChannelEntry{
        Name:     "openai",
        Type:     "cloud",
        BaseURL:  "https://api.openai.com",
        Models:   []string{"gpt-4"},
        Status:   "online",
        Priority: 1,
        Weight:   100,
        Enabled:  true,
    }
    if err := rs.CreateChannel(ch); err != nil {
        t.Fatalf("CreateChannel failed: %v", err)
    }

    got, err := rs.GetChannel("openai")
    if err != nil {
        t.Fatalf("GetChannel failed: %v", err)
    }
    if got.Name != "openai" {
        t.Fatalf("expected openai, got %s", got.Name)
    }

    got.Weight = 200
    if err := rs.UpdateChannel(got); err != nil {
        t.Fatalf("UpdateChannel failed: %v", err)
    }

    channels, err := rs.ListChannels()
    if err != nil {
        t.Fatalf("ListChannels failed: %v", err)
    }
    if len(channels) != 1 {
        t.Fatalf("expected 1 channel, got %d", len(channels))
    }

    if err := rs.DeleteChannel("openai"); err != nil {
        t.Fatalf("DeleteChannel failed: %v", err)
    }
    _, err = rs.GetChannel("openai")
    if err == nil {
        t.Fatal("expected error after delete")
    }
}

func TestRedisStore_GetChannel_NotFound(t *testing.T) {
    rs, mr := setupTestStore(t)
    defer teardownTestStore(rs, mr)

    _, err := rs.GetChannel("nonexistent")
    if err == nil {
        t.Fatal("expected error for missing channel")
    }
}

func TestRedisStore_Analytics_NilReturns(t *testing.T) {
    rs, mr := setupTestStore(t)
    defer teardownTestStore(rs, mr)

    now := time.Now()
    tokenStats, err := rs.GetTokenStats(now, now, "day")
    if err != nil || tokenStats != nil {
        t.Fatalf("expected nil/nil, got %v/%v", tokenStats, err)
    }
    costStats, err := rs.GetCostStats(now, now, "day")
    if err != nil || costStats != nil {
        t.Fatalf("expected nil/nil, got %v/%v", costStats, err)
    }
    modelStats, err := rs.GetModelStats(now, now)
    if err != nil || modelStats != nil {
        t.Fatalf("expected nil/nil, got %v/%v", modelStats, err)
    }
    latencyStats, err := rs.GetLatencyStats(now, now)
    if err != nil || latencyStats != nil {
        t.Fatalf("expected nil/nil, got %v/%v", latencyStats, err)
    }
    errorStats, err := rs.GetErrorStats(now, now)
    if err != nil || errorStats != nil {
        t.Fatalf("expected nil/nil, got %v/%v", errorStats, err)
    }
    overview, err := rs.GetDashboardOverview()
    if err != nil || overview == nil {
        t.Fatalf("expected non-nil/nil, got %v/%v", overview, err)
    }
    profitStats, err := rs.GetKeyProfitStats(now, now)
    if err != nil || profitStats == nil {
        t.Fatalf("expected non-nil/nil, got %v/%v", profitStats, err)
    }
}

func TestRedisStore_Quota(t *testing.T) {
    rs, mr := setupTestStore(t)
    defer teardownTestStore(rs, mr)

    _ = rs.CreateKey(&store.APIKeyEntry{Name: "k1", QuotaLimit: 100.0, QuotaUsed: 0, BudgetLimit: 100})

    used, limit, exceeded, err := rs.CheckQuota("k1")
    if err != nil {
        t.Fatalf("CheckQuota failed: %v", err)
    }
    if used != 0 || limit != 100 || exceeded {
        t.Fatalf("expected 0/100/false, got %f/%f/%v", used, limit, exceeded)
    }

    if err := rs.DeductQuota("k1", 50); err != nil {
        t.Fatalf("DeductQuota failed: %v", err)
    }
    used2, _, exceeded2, _ := rs.CheckQuota("k1")
    if used2 != 50 {
        t.Fatalf("expected 50 used, got %f", used2)
    }
    if exceeded2 {
        t.Fatal("should not be exceeded yet")
    }

    _ = rs.DeductQuota("k1", 60)
    _, _, exceeded3, _ := rs.CheckQuota("k1")
    if !exceeded3 {
        t.Fatal("should be exceeded now")
    }
}

func TestRedisStore_CheckQuota_MissingKey(t *testing.T) {
    rs, mr := setupTestStore(t)
    defer teardownTestStore(rs, mr)

    _, _, _, err := rs.CheckQuota("nonexistent")
    if err == nil {
        t.Fatal("expected error for missing key")
    }
}

func TestRedisStore_DeductQuota_MissingKey(t *testing.T) {
    rs, mr := setupTestStore(t)
    defer teardownTestStore(rs, mr)

    err := rs.DeductQuota("nonexistent", 10)
    if err == nil {
        t.Fatal("expected error for missing key")
    }
}

// TestRedisStore_DeductQuota_ConcurrentNoLostIncrement is the AH1 (audit P1)
// regression: the prior GetKey -> QuotaUsed += amount -> UpdateKey blob RMW
// lost an increment under concurrent deductions. INCRBYFLOAT is atomic, so the
// final counter must equal the exact sum of all amounts regardless of goroutine
// interleaving. miniredis is single-threaded (no real cross-instance race), so
// this proves the counter — not the blob — is authoritative and sums correctly;
// the lost-increment defect itself is structural in the prior RMW and is fixed
// by moving the mutation to a single atomic op.
func TestRedisStore_DeductQuota_ConcurrentNoLostIncrement(t *testing.T) {
    rs, mr := setupTestStore(t)
    defer teardownTestStore(rs, mr)

    _ = rs.CreateKey(&store.APIKeyEntry{Name: "k-concurrent", QuotaLimit: 1000000, QuotaUsed: 0, BudgetLimit: 1000000})

    const goroutines = 50
    const perGoroutine = 10
    var wg sync.WaitGroup
    for i := 0; i < goroutines; i++ {
        wg.Add(1)
        go func() {
            defer wg.Done()
            for j := 0; j < perGoroutine; j++ {
                if err := rs.DeductQuota("k-concurrent", 1.0); err != nil {
                    t.Errorf("DeductQuota failed: %v", err)
                }
            }
        }()
    }
    wg.Wait()

    used, _, exceeded, err := rs.CheckQuota("k-concurrent")
    if err != nil {
        t.Fatalf("CheckQuota failed: %v", err)
    }
    expected := float64(goroutines * perGoroutine)
    if used != expected {
        t.Fatalf("AH1 regression: expected %f used (no lost increment), got %f", expected, used)
    }
    if exceeded {
        t.Fatalf("should not be exceeded at %f/%f", used, float64(1000000))
    }
}

// TestRedisStore_DeductQuota_CounterAuthoritativeNotBlob confirms CheckQuota
// reads the AH1 counter, not the stale QuotaUsed baked into the key blob.
// CreateKey seeds the counter via SetNX but the blob's QuotaUsed is never
// updated by DeductQuota; the counter alone must reflect deductions.
func TestRedisStore_DeductQuota_CounterAuthoritativeNotBlob(t *testing.T) {
    rs, mr := setupTestStore(t)
    defer teardownTestStore(rs, mr)

    _ = rs.CreateKey(&store.APIKeyEntry{Name: "k-auth", QuotaLimit: 100, QuotaUsed: 5, BudgetLimit: 100})
    if err := rs.DeductQuota("k-auth", 30); err != nil {
        t.Fatalf("DeductQuota failed: %v", err)
    }
    used, limit, _, err := rs.CheckQuota("k-auth")
    if err != nil {
        t.Fatalf("CheckQuota failed: %v", err)
    }
    // Counter seeded at QuotaUsed=5 then +30 -> 35, NOT the blob's stale 5.
    if used != 35 {
        t.Fatalf("expected counter=35 (seed 5 + 30), got %f", used)
    }
    if limit != 100 {
        t.Fatalf("expected limit=100, got %f", limit)
    }
}

func TestRedisStore_Teams_CRUD(t *testing.T) {
    rs, mr := setupTestStore(t)
    defer teardownTestStore(rs, mr)

    team := &store.Team{
        ID:         "team-1",
        Name:       "Test Team",
        QuotaLimit: 1000,
        Members:    []store.TeamMember{{UserID: "admin", Role: "admin"}},
    }
    if err := rs.CreateTeam(team); err != nil {
        t.Fatalf("CreateTeam failed: %v", err)
    }

    got, err := rs.GetTeam("team-1")
    if err != nil {
        t.Fatalf("GetTeam failed: %v", err)
    }
    if got.Name != "Test Team" {
        t.Fatalf("expected Test Team, got %s", got.Name)
    }

    got.Name = "Updated"
    if err := rs.UpdateTeam(got); err != nil {
        t.Fatalf("UpdateTeam failed: %v", err)
    }

    teams, err := rs.ListTeams()
    if err != nil {
        t.Fatalf("ListTeams failed: %v", err)
    }
    if len(teams) != 1 {
        t.Fatalf("expected 1 team, got %d", len(teams))
    }

    if err := rs.DeleteTeam("team-1"); err != nil {
        t.Fatalf("DeleteTeam failed: %v", err)
    }
    _, err = rs.GetTeam("team-1")
    if err == nil {
        t.Fatal("expected error after delete")
    }
}

func TestRedisStore_GetTeam_NotFound(t *testing.T) {
    rs, mr := setupTestStore(t)
    defer teardownTestStore(rs, mr)

    _, err := rs.GetTeam("nonexistent")
    if err == nil {
        t.Fatal("expected error for missing team")
    }
}

func TestRedisStore_BindKeyToTeam(t *testing.T) {
    rs, mr := setupTestStore(t)
    defer teardownTestStore(rs, mr)

    _ = rs.CreateTeam(&store.Team{ID: "t1", Name: "Team1", QuotaLimit: 100})
    _ = rs.BindKeyToTeam("sk-test", "t1")

    team, err := rs.GetTeamByKey("sk-test")
    if err != nil {
        t.Fatalf("GetTeamByKey failed: %v", err)
    }
    if team.ID != "t1" {
        t.Fatalf("expected t1, got %s", team.ID)
    }
}

func TestRedisStore_GetTeamByKey_NotBound(t *testing.T) {
    rs, mr := setupTestStore(t)
    defer teardownTestStore(rs, mr)

    _, err := rs.GetTeamByKey("sk-unbound")
    if err == nil {
        t.Fatal("expected error for unbound key")
    }
}

func TestRedisStore_AddTeamCost(t *testing.T) {
    rs, mr := setupTestStore(t)
    defer teardownTestStore(rs, mr)

    _ = rs.CreateTeam(&store.Team{ID: "t1", Name: "Team1", QuotaLimit: 100, QuotaUsed: 0})
    if err := rs.AddTeamCost("t1", 25.0); err != nil {
        t.Fatalf("AddTeamCost failed: %v", err)
    }
    // R4: the authoritative usage is the dedicated COUNTER (read via
    // CheckTeamQuota), not the blob's QuotaUsed field (the non-atomic RMW
    // victim, deliberately no longer updated by AddTeamCost — same tradeoff
    // as AH1's per-key quota path). Assert via the counter read path.
    _, used, _, err := rs.CheckTeamQuota("t1")
    if err != nil {
        t.Fatalf("CheckTeamQuota failed: %v", err)
    }
    if used != 25.0 {
        t.Fatalf("expected counter used 25.0, got %f", used)
    }
}

func TestRedisStore_AddTeamCost_MissingTeam(t *testing.T) {
    rs, mr := setupTestStore(t)
    defer teardownTestStore(rs, mr)

    err := rs.AddTeamCost("nonexistent", 10)
    if err == nil {
        t.Fatal("expected error for missing team")
    }
}

func TestRedisStore_CheckTeamQuota(t *testing.T) {
    rs, mr := setupTestStore(t)
    defer teardownTestStore(rs, mr)

    // R4: the team blob's QuotaUsed is no longer the source of truth — the
    // dedicated counter is, starting at 0 for a fresh team. The limit still
    // comes from the blob. A fresh team (no AddTeamCost) reads used=0, ok=true.
    _ = rs.CreateTeam(&store.Team{ID: "t1", Name: "Team1", QuotaLimit: 100, QuotaUsed: 30})
    limit, used, ok, err := rs.CheckTeamQuota("t1")
    if err != nil {
        t.Fatalf("CheckTeamQuota failed: %v", err)
    }
    if limit != 100 || used != 0 || !ok {
        t.Fatalf("expected 100/0/true (counter fresh), got %f/%f/%v", limit, used, ok)
    }
    // After a deduction the counter reflects it; limit enforcement is additive.
    if err := rs.AddTeamCost("t1", 30.0); err != nil {
        t.Fatalf("AddTeamCost failed: %v", err)
    }
    _, used, ok, err = rs.CheckTeamQuota("t1")
    if err != nil {
        t.Fatalf("CheckTeamQuota after cost failed: %v", err)
    }
    if used != 30.0 || !ok {
        t.Fatalf("expected 30.0/true after one cost, got %f/%v", used, ok)
    }
}

func TestRedisStore_CheckTeamQuota_MissingTeam(t *testing.T) {
    rs, mr := setupTestStore(t)
    defer teardownTestStore(rs, mr)

    _, _, _, err := rs.CheckTeamQuota("nonexistent")
    if err == nil {
        t.Fatal("expected error for missing team")
    }
}

// TestR4_AddTeamCost_ConcurrentNoLostIncrement: R4 (audit P1) — N concurrent
// AddTeamCost calls on the same team must each land, none lost. The prior
// GetTeam -> mutate -> UpdateTeam blob RMW was non-atomic: two goroutines both
// read QuotaUsed=X, each writes X+1, net +1 not +2 (one increment lost). The
// Lua INCRBYFLOAT counter is atomic, so 50 concurrent +1.0 calls yield exactly
// 50.0. Revert AddTeamCost to the blob RMW and the assertion fails (sum < 50).
func TestR4_AddTeamCost_ConcurrentNoLostIncrement(t *testing.T) {
    rs, mr := setupTestStore(t)
    defer teardownTestStore(rs, mr)

    _ = rs.CreateTeam(&store.Team{ID: "tc", Name: "Concurrent", QuotaLimit: 1e9, QuotaUsed: 0})

    const goroutines = 50
    const perCall = 1.0
    var wg sync.WaitGroup
    start := make(chan struct{})
    wg.Add(goroutines)
    for i := 0; i < goroutines; i++ {
        go func() {
            defer wg.Done()
            <-start
            if err := rs.AddTeamCost("tc", perCall); err != nil {
                t.Errorf("AddTeamCost concurrent: %v", err)
            }
        }()
    }
    close(start)
    wg.Wait()

    _, used, _, err := rs.CheckTeamQuota("tc")
    if err != nil {
        t.Fatalf("CheckTeamQuota failed: %v", err)
    }
    want := float64(goroutines) * perCall
    if used != want {
        t.Fatalf("R4: concurrent AddTeamCost lost increments — used=%f want=%f (RMW non-atomic, billing double-spend vector open)", used, want)
    }
}

// TestR4_AddTeamCost_BothCountersConsistent: R4 — the Lua script increments
// quota-used AND cost-accumulated in one atomic op. A partial failure would
// leave them diverged (quota charged but lifetime cost not recorded). Assert
// both counters equal the sum of all AddTeamCost calls.
func TestR4_AddTeamCost_BothCountersConsistent(t *testing.T) {
    rs, mr := setupTestStore(t)
    defer teardownTestStore(rs, mr)

    _ = rs.CreateTeam(&store.Team{ID: "tb", Name: "BothCounters", QuotaLimit: 1e9, QuotaUsed: 0})

    if err := rs.AddTeamCost("tb", 10.0); err != nil {
        t.Fatalf("first AddTeamCost: %v", err)
    }
    if err := rs.AddTeamCost("tb", 5.5); err != nil {
        t.Fatalf("second AddTeamCost: %v", err)
    }

    ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
    defer cancel()
    quotaUsedStr, err := rs.client.Get(ctx, "fusion:team:quota_used:tb").Result()
    if err != nil {
        t.Fatalf("read quota_used counter: %v", err)
    }
    costAccumStr, err := rs.client.Get(ctx, "fusion:team:cost_accum:tb").Result()
    if err != nil {
        t.Fatalf("read cost_accum counter: %v", err)
    }
    quotaUsed, _ := strconv.ParseFloat(quotaUsedStr, 64)
    costAccum, _ := strconv.ParseFloat(costAccumStr, 64)
    if quotaUsed != 15.5 || costAccum != 15.5 {
        t.Fatalf("R4: counters diverged — quota_used=%f cost_accum=%f want 15.5/15.5 (Lua not atomic, partial-failure divergence)", quotaUsed, costAccum)
    }
}

func TestRedisStore_AddTeamMember(t *testing.T) {
    rs, mr := setupTestStore(t)
    defer teardownTestStore(rs, mr)

    _ = rs.CreateTeam(&store.Team{ID: "t1", Name: "Team1", Members: []store.TeamMember{}})
    if err := rs.AddTeamMember("t1", "user1", "editor"); err != nil {
        t.Fatalf("AddTeamMember failed: %v", err)
    }
    team, _ := rs.GetTeam("t1")
    if len(team.Members) != 1 {
        t.Fatalf("expected 1 member, got %d", len(team.Members))
    }
}

func TestRedisStore_AddTeamMember_MissingTeam(t *testing.T) {
    rs, mr := setupTestStore(t)
    defer teardownTestStore(rs, mr)

    err := rs.AddTeamMember("nonexistent", "user1", "editor")
    if err == nil {
        t.Fatal("expected error for missing team")
    }
}

func TestRedisStore_RemoveTeamMember(t *testing.T) {
    rs, mr := setupTestStore(t)
    defer teardownTestStore(rs, mr)

    _ = rs.CreateTeam(&store.Team{
        ID:      "t1",
        Name:    "Team1",
        Members: []store.TeamMember{{UserID: "user1", Role: "editor"}, {UserID: "user2", Role: "viewer"}},
    })
    if err := rs.RemoveTeamMember("t1", "user1"); err != nil {
        t.Fatalf("RemoveTeamMember failed: %v", err)
    }
    team, _ := rs.GetTeam("t1")
    if len(team.Members) != 1 {
        t.Fatalf("expected 1 member, got %d", len(team.Members))
    }
}

func TestRedisStore_RemoveTeamMember_MissingTeam(t *testing.T) {
    rs, mr := setupTestStore(t)
    defer teardownTestStore(rs, mr)

    err := rs.RemoveTeamMember("nonexistent", "user1")
    if err == nil {
        t.Fatal("expected error for missing team")
    }
}

func TestRedisStore_Orgs_CRUD(t *testing.T) {
    rs, mr := setupTestStore(t)
    defer teardownTestStore(rs, mr)

    org := &store.Organization{ID: "org-1", Name: "Org One"}
    if err := rs.CreateOrg(org); err != nil {
        t.Fatalf("CreateOrg failed: %v", err)
    }

    got, err := rs.GetOrg("org-1")
    if err != nil {
        t.Fatalf("GetOrg failed: %v", err)
    }
    if got.Name != "Org One" {
        t.Fatalf("expected Org One, got %s", got.Name)
    }

    orgs, err := rs.ListOrgs()
    if err != nil {
        t.Fatalf("ListOrgs failed: %v", err)
    }
    if len(orgs) != 1 {
        t.Fatalf("expected 1 org, got %d", len(orgs))
    }

    if err := rs.DeleteOrg("org-1"); err != nil {
        t.Fatalf("DeleteOrg failed: %v", err)
    }
    _, err = rs.GetOrg("org-1")
    if err == nil {
        t.Fatal("expected error after delete")
    }
}

func TestRedisStore_GetOrg_NotFound(t *testing.T) {
    rs, mr := setupTestStore(t)
    defer teardownTestStore(rs, mr)

    _, err := rs.GetOrg("nonexistent")
    if err == nil {
        t.Fatal("expected error for missing org")
    }
}

func TestRedisStore_Batch_CRUD(t *testing.T) {
    rs, mr := setupTestStore(t)
    defer teardownTestStore(rs, mr)

    reqs := []store.BatchRequest{
        {CustomID: "r1", Method: "POST", URL: "/v1/chat/completions"},
        {CustomID: "r2", Method: "POST", URL: "/v1/chat/completions"},
    }
    b, err := rs.CreateBatch(reqs, "/v1/chat/completions", "24h")
    if err != nil {
        t.Fatalf("CreateBatch failed: %v", err)
    }
    if b.Status != store.BatchStatusPending {
        t.Fatalf("expected pending, got %s", b.Status)
    }

    got, err := rs.GetBatch(b.ID)
    if err != nil {
        t.Fatalf("GetBatch failed: %v", err)
    }
    if got.ID != b.ID {
        t.Fatalf("expected %s, got %s", b.ID, got.ID)
    }

    list, err := rs.ListBatches()
    if err != nil {
        t.Fatalf("ListBatches failed: %v", err)
    }
    if len(list) != 1 {
        t.Fatalf("expected 1, got %d", len(list))
    }

    cancelled, err := rs.CancelBatch(b.ID)
    if err != nil {
        t.Fatalf("CancelBatch failed: %v", err)
    }
    if cancelled.Status != store.BatchStatusCancelled {
        t.Fatalf("expected cancelled, got %s", cancelled.Status)
    }
}

func TestRedisStore_GetBatch_NotFound(t *testing.T) {
    rs, mr := setupTestStore(t)
    defer teardownTestStore(rs, mr)

    _, err := rs.GetBatch("nonexistent")
    if err == nil {
        t.Fatal("expected error for missing batch")
    }
}

func TestRedisStore_CancelBatch_AlreadyDone(t *testing.T) {
    rs, mr := setupTestStore(t)
    defer teardownTestStore(rs, mr)

    b, _ := rs.CreateBatch([]store.BatchRequest{{CustomID: "r1"}}, "", "")
    b.Status = store.BatchStatusCompleted
    _ = rs.UpdateBatch(b)

    _, err := rs.CancelBatch(b.ID)
    if err == nil {
        t.Fatal("expected error cancelling completed batch")
    }
}

func TestRedisStore_CancelBatch_AlreadyCancelled(t *testing.T) {
    rs, mr := setupTestStore(t)
    defer teardownTestStore(rs, mr)

    b, _ := rs.CreateBatch([]store.BatchRequest{{CustomID: "r1"}}, "", "")
    b.Status = store.BatchStatusCancelled
    _ = rs.UpdateBatch(b)

    _, err := rs.CancelBatch(b.ID)
    if err == nil {
        t.Fatal("expected error cancelling already cancelled batch")
    }
}

func TestRedisStore_CancelBatch_MissingBatch(t *testing.T) {
    rs, mr := setupTestStore(t)
    defer teardownTestStore(rs, mr)

    _, err := rs.CancelBatch("nonexistent")
    if err == nil {
        t.Fatal("expected error for missing batch")
    }
}

func TestRedisStore_UpdateBatch(t *testing.T) {
    rs, mr := setupTestStore(t)
    defer teardownTestStore(rs, mr)

    b, _ := rs.CreateBatch([]store.BatchRequest{{CustomID: "r1"}}, "", "")
    b.Status = store.BatchStatusRunning
    b.Completed = 1
    if err := rs.UpdateBatch(b); err != nil {
        t.Fatalf("UpdateBatch failed: %v", err)
    }
    got, _ := rs.GetBatch(b.ID)
    if got.Status != store.BatchStatusRunning {
        t.Fatalf("expected running, got %s", got.Status)
    }
}

func TestRedisStore_Cost_RecordUsage(t *testing.T) {
    rs, mr := setupTestStore(t)
    defer teardownTestStore(rs, mr)

    if err := rs.RecordUsage("key1", "cloud", "gpt-4", 100, 50, 0.01); err != nil {
        t.Fatalf("RecordUsage failed: %v", err)
    }
}

func TestRedisStore_Cost_GetCostSummary(t *testing.T) {
    rs, mr := setupTestStore(t)
    defer teardownTestStore(rs, mr)

    _ = rs.RecordUsage("key1", "cloud", "gpt-4", 100, 50, 0.01)
    _ = rs.RecordUsage("key2", "local", "llama3", 200, 100, 0.005)
    _ = rs.RecordUsage("key1", "cloud", "gpt-4", 50, 25, 0.005)

    summary, err := rs.GetCostSummary("key1")
    if err != nil {
        t.Fatalf("GetCostSummary failed: %v", err)
    }
    if summary.TotalCostUSD != 0.015 {
        t.Fatalf("expected 0.015, got %f", summary.TotalCostUSD)
    }
    if summary.TotalRequests != 2 {
        t.Fatalf("expected 2 requests, got %d", summary.TotalRequests)
    }
}

func TestRedisStore_Cost_GetCostSummaryAll(t *testing.T) {
    rs, mr := setupTestStore(t)
    defer teardownTestStore(rs, mr)

    _ = rs.RecordUsage("key1", "cloud", "gpt-4", 100, 50, 0.01)
    _ = rs.RecordUsage("key2", "local", "llama3", 200, 100, 0.005)

    summary, err := rs.GetCostSummaryAll()
    if err != nil {
        t.Fatalf("GetCostSummaryAll failed: %v", err)
    }
    if summary.TotalCostUSD != 0.015 {
        t.Fatalf("expected 0.015, got %f", summary.TotalCostUSD)
    }
    if summary.TotalRequests != 2 {
        t.Fatalf("expected 2, got %d", summary.TotalRequests)
    }
    if summary.ByBackend["cloud"] != 0.01 {
        t.Fatalf("expected cloud=0.01, got %f", summary.ByBackend["cloud"])
    }
    if summary.ByModel["gpt-4"] != 0.01 {
        t.Fatalf("expected gpt-4=0.01, got %f", summary.ByModel["gpt-4"])
    }
}

func TestRedisStore_QueryLogs_Empty(t *testing.T) {
    rs, mr := setupTestStore(t)
    defer teardownTestStore(rs, mr)

    logs, total, err := rs.QueryLogs(store.LogFilter{Page: 0, PageSize: 10})
    if err != nil {
        t.Fatalf("QueryLogs on empty store failed: %v", err)
    }
    if total != 0 {
        t.Fatalf("expected 0, got %d", total)
    }
    if len(logs) != 0 {
        t.Fatalf("expected 0 logs, got %d", len(logs))
    }
}

func TestRedisStore_ListKeys_Empty(t *testing.T) {
    rs, mr := setupTestStore(t)
    defer teardownTestStore(rs, mr)

    keys, err := rs.ListKeys()
    if err != nil {
        t.Fatalf("ListKeys on empty store failed: %v", err)
    }
    if len(keys) != 0 {
        t.Fatalf("expected 0, got %d", len(keys))
    }
}

func TestRedisStore_ListChannels_Empty(t *testing.T) {
    rs, mr := setupTestStore(t)
    defer teardownTestStore(rs, mr)

    channels, err := rs.ListChannels()
    if err != nil {
        t.Fatalf("ListChannels on empty store failed: %v", err)
    }
    if len(channels) != 0 {
        t.Fatalf("expected 0, got %d", len(channels))
    }
}

func TestRedisStore_ListTeams_Empty(t *testing.T) {
    rs, mr := setupTestStore(t)
    defer teardownTestStore(rs, mr)

    teams, err := rs.ListTeams()
    if err != nil {
        t.Fatalf("ListTeams on empty store failed: %v", err)
    }
    if len(teams) != 0 {
        t.Fatalf("expected 0, got %d", len(teams))
    }
}

func TestRedisStore_ListOrgs_Empty(t *testing.T) {
    rs, mr := setupTestStore(t)
    defer teardownTestStore(rs, mr)

    orgs, err := rs.ListOrgs()
    if err != nil {
        t.Fatalf("ListOrgs on empty store failed: %v", err)
    }
    if len(orgs) != 0 {
        t.Fatalf("expected 0, got %d", len(orgs))
    }
}

func TestRedisStore_ListBatches_Empty(t *testing.T) {
    rs, mr := setupTestStore(t)
    defer teardownTestStore(rs, mr)

    batches, err := rs.ListBatches()
    if err != nil {
        t.Fatalf("ListBatches on empty store failed: %v", err)
    }
    if len(batches) != 0 {
        t.Fatalf("expected 0, got %d", len(batches))
    }
}

func TestRedisStore_Cost_GetCostSummary_Empty(t *testing.T) {
    rs, mr := setupTestStore(t)
    defer teardownTestStore(rs, mr)

    summary, err := rs.GetCostSummary("")
    if err != nil {
        t.Fatalf("GetCostSummary on empty store failed: %v", err)
    }
    if summary.TotalCostUSD != 0 {
        t.Fatalf("expected 0, got %f", summary.TotalCostUSD)
    }
}

func TestRedisStore_DeleteKey_Nonexistent(t *testing.T) {
    rs, mr := setupTestStore(t)
    defer teardownTestStore(rs, mr)

    err := rs.DeleteKey("nonexistent")
    if err != nil {
        t.Logf("DeleteKey on nonexistent returned: %v (ok)", err)
    }
}

func TestRedisStore_DeleteChannel_Nonexistent(t *testing.T) {
    rs, mr := setupTestStore(t)
    defer teardownTestStore(rs, mr)

    err := rs.DeleteChannel("nonexistent")
    if err != nil {
        t.Logf("DeleteChannel on nonexistent returned: %v (ok)", err)
    }
}

func TestRedisStore_DeleteTeam_Nonexistent(t *testing.T) {
    rs, mr := setupTestStore(t)
    defer teardownTestStore(rs, mr)

    err := rs.DeleteTeam("nonexistent")
    if err != nil {
        t.Logf("DeleteTeam on nonexistent returned: %v (ok)", err)
    }
}

func TestRedisStore_DeleteOrg_Nonexistent(t *testing.T) {
    rs, mr := setupTestStore(t)
    defer teardownTestStore(rs, mr)

    err := rs.DeleteOrg("nonexistent")
    if err != nil {
        t.Logf("DeleteOrg on nonexistent returned: %v (ok)", err)
    }
}

func TestRedisStore_ListKeys_InvalidData(t *testing.T) {
    rs, mr := setupTestStore(t)
    defer teardownTestStore(rs, mr)

    rs.client.Set(context.Background(), apiKeyPrefix+"bad", "not-json", 0)
    _ = rs.CreateKey(&store.APIKeyEntry{Name: "good"})
    keys, err := rs.ListKeys()
    if err != nil {
        t.Fatalf("ListKeys failed: %v", err)
    }
    if len(keys) != 1 || keys[0].Name != "good" {
        t.Fatalf("expected 1 good key, got %d", len(keys))
    }
}

func TestRedisStore_ListChannels_InvalidData(t *testing.T) {
    rs, mr := setupTestStore(t)
    defer teardownTestStore(rs, mr)

    rs.client.Set(context.Background(), channelPrefix+"bad", "not-json", 0)
    _ = rs.CreateChannel(&store.ChannelEntry{Name: "good"})
    channels, err := rs.ListChannels()
    if err != nil {
        t.Fatalf("ListChannels failed: %v", err)
    }
    if len(channels) != 1 || channels[0].Name != "good" {
        t.Fatalf("expected 1 good channel, got %d", len(channels))
    }
}

func TestRedisStore_ListTeams_InvalidData(t *testing.T) {
    rs, mr := setupTestStore(t)
    defer teardownTestStore(rs, mr)

    rs.client.Set(context.Background(), teamPrefix+"bad", "not-json", 0)
    _ = rs.CreateTeam(&store.Team{ID: "good", Name: "Good"})
    teams, err := rs.ListTeams()
    if err != nil {
        t.Fatalf("ListTeams failed: %v", err)
    }
    if len(teams) != 1 || teams[0].ID != "good" {
        t.Fatalf("expected 1 good team, got %d", len(teams))
    }
}

func TestRedisStore_ListOrgs_InvalidData(t *testing.T) {
    rs, mr := setupTestStore(t)
    defer teardownTestStore(rs, mr)

    rs.client.Set(context.Background(), orgPrefix+"bad", "not-json", 0)
    _ = rs.CreateOrg(&store.Organization{ID: "good", Name: "Good"})
    orgs, err := rs.ListOrgs()
    if err != nil {
        t.Fatalf("ListOrgs failed: %v", err)
    }
    if len(orgs) != 1 || orgs[0].ID != "good" {
        t.Fatalf("expected 1 good org, got %d", len(orgs))
    }
}

func TestRedisStore_ListBatches_InvalidData(t *testing.T) {
    rs, mr := setupTestStore(t)
    defer teardownTestStore(rs, mr)

    rs.client.Set(context.Background(), batchPrefix+"bad", "not-json", 0)
    b, _ := rs.CreateBatch([]store.BatchRequest{{CustomID: "r1"}}, "", "")
    batches, err := rs.ListBatches()
    if err != nil {
        t.Fatalf("ListBatches failed: %v", err)
    }
    found := false
    for _, batch := range batches {
        if batch.ID == b.ID {
            found = true
        }
    }
    if !found {
        t.Fatal("expected to find good batch in listing")
    }
}

func TestRedisStore_GetKey_InvalidJSON(t *testing.T) {
    rs, mr := setupTestStore(t)
    defer teardownTestStore(rs, mr)

    rs.client.Set(context.Background(), apiKeyPrefix+"bad", "not-json", 0)
    _, err := rs.GetKey("bad")
    if err == nil {
        t.Fatal("expected error for invalid JSON")
    }
}

func TestRedisStore_GetChannel_InvalidJSON(t *testing.T) {
    rs, mr := setupTestStore(t)
    defer teardownTestStore(rs, mr)

    rs.client.Set(context.Background(), channelPrefix+"bad", "not-json", 0)
    _, err := rs.GetChannel("bad")
    if err == nil {
        t.Fatal("expected error for invalid JSON")
    }
}

func TestRedisStore_GetTeam_InvalidJSON(t *testing.T) {
    rs, mr := setupTestStore(t)
    defer teardownTestStore(rs, mr)

    rs.client.Set(context.Background(), teamPrefix+"bad", "not-json", 0)
    _, err := rs.GetTeam("bad")
    if err == nil {
        t.Fatal("expected error for invalid JSON")
    }
}

func TestRedisStore_GetOrg_InvalidJSON(t *testing.T) {
    rs, mr := setupTestStore(t)
    defer teardownTestStore(rs, mr)

    rs.client.Set(context.Background(), orgPrefix+"bad", "not-json", 0)
    _, err := rs.GetOrg("bad")
    if err == nil {
        t.Fatal("expected error for invalid JSON")
    }
}

func TestRedisStore_GetBatch_InvalidJSON(t *testing.T) {
    rs, mr := setupTestStore(t)
    defer teardownTestStore(rs, mr)

    rs.client.Set(context.Background(), batchPrefix+"bad", "not-json", 0)
    _, err := rs.GetBatch("bad")
    if err == nil {
        t.Fatal("expected error for invalid JSON")
    }
}

func TestRedisStore_GetLog_InvalidJSON(t *testing.T) {
    rs, mr := setupTestStore(t)
    defer teardownTestStore(rs, mr)

    rs.client.Set(context.Background(), logPrefix+"bad", "not-json", 0)
    _, err := rs.GetLog("bad")
    if err == nil {
        t.Fatal("expected error for invalid JSON")
    }
}

func TestRedisStore_QueryLogs_InvalidLogData(t *testing.T) {
    rs, mr := setupTestStore(t)
    defer teardownTestStore(rs, mr)

    rs.client.ZAdd(context.Background(), logPrefix+"index", redis.Z{Score: 1, Member: "bad"})
    rs.client.Set(context.Background(), logPrefix+"bad", "not-json", 0)
    logs, total, err := rs.QueryLogs(store.LogFilter{Page: 0, PageSize: 10})
    if err != nil {
        t.Fatalf("QueryLogs failed: %v", err)
    }
    if total != 1 {
        t.Fatalf("expected 1 in index, got %d", total)
    }
    if len(logs) != 0 {
        t.Fatalf("expected 0 valid logs, got %d", len(logs))
    }
}

func TestRedisStore_QueryLogs_MissingLogData(t *testing.T) {
    rs, mr := setupTestStore(t)
    defer teardownTestStore(rs, mr)

    rs.client.ZAdd(context.Background(), logPrefix+"index", redis.Z{Score: 1, Member: "ghost"})
    logs, total, err := rs.QueryLogs(store.LogFilter{Page: 0, PageSize: 10})
    if err != nil {
        t.Fatalf("QueryLogs failed: %v", err)
    }
    if total != 1 {
        t.Fatalf("expected 1, got %d", total)
    }
    if len(logs) != 0 {
        t.Fatalf("expected 0 logs (missing data), got %d", len(logs))
    }
}

func TestRedisStore_ExportLogs_QueryError(t *testing.T) {
    rs, mr := setupTestStore(t)
    defer teardownTestStore(rs, mr)

    mr.Close()
    _, err := rs.ExportLogs(store.LogFilter{Page: 0, PageSize: 10}, "json")
    if err == nil {
        t.Fatal("expected error with closed redis")
    }
}

func TestRedisStore_AppendLog_MarshalError(t *testing.T) {
    rs, mr := setupTestStore(t)
    defer teardownTestStore(rs, mr)

    badLog := &store.RequestLog{}
    data, _ := json.Marshal(badLog)
    slog.Info("normal marshal works", "data", string(data))
}

func TestRedisStore_GetCostSummary_InvalidRecord(t *testing.T) {
    rs, mr := setupTestStore(t)
    defer teardownTestStore(rs, mr)

    rs.client.ZAdd(context.Background(), costByTime, redis.Z{Score: 1, Member: "bad"})
    rs.client.Set(context.Background(), costPrefix+"rec:bad", "not-json", 0)
    summary, err := rs.GetCostSummary("")
    if err != nil {
        t.Fatalf("GetCostSummary failed: %v", err)
    }
    if summary.TotalRequests != 0 {
        t.Fatalf("expected 0, got %d", summary.TotalRequests)
    }
}

func TestRedisStore_GetCostSummary_MissingRecord(t *testing.T) {
    rs, mr := setupTestStore(t)
    defer teardownTestStore(rs, mr)

    rs.client.ZAdd(context.Background(), costByTime, redis.Z{Score: 1, Member: "ghost"})
    summary, err := rs.GetCostSummary("")
    if err != nil {
        t.Fatalf("GetCostSummary failed: %v", err)
    }
    if summary.TotalRequests != 0 {
        t.Fatalf("expected 0, got %d", summary.TotalRequests)
    }
}

func TestRedisStore_QueryLogs_Pagination(t *testing.T) {
    rs, mr := setupTestStore(t)
    defer teardownTestStore(rs, mr)

    now := time.Now()
    for i := 0; i < 5; i++ {
        _ = rs.AppendLog(&store.RequestLog{
            ID:        fmt.Sprintf("log-%d", i),
            Model:     "gpt-4",
            Timestamp: now.Add(time.Duration(i) * time.Minute),
        })
    }

    logs, total, err := rs.QueryLogs(store.LogFilter{Page: 0, PageSize: 2})
    if err != nil {
        t.Fatalf("QueryLogs page 0 failed: %v", err)
    }
    if total != 5 {
        t.Fatalf("expected total 5, got %d", total)
    }
    if len(logs) != 2 {
        t.Fatalf("expected 2 on page 0, got %d", len(logs))
    }

    logs2, _, _ := rs.QueryLogs(store.LogFilter{Page: 1, PageSize: 2})
    if len(logs2) != 2 {
        t.Fatalf("expected 2 on page 1, got %d", len(logs2))
    }
}

func TestRedisStore_CreateKey_MarshalError(t *testing.T) {
    rs, mr := setupTestStore(t)
    defer teardownTestStore(rs, mr)

    key := &store.APIKeyEntry{Name: "test"}
    err := rs.CreateKey(key)
    if err != nil {
        t.Fatalf("CreateKey normal should work: %v", err)
    }
}

func TestRedisStore_CreateChannel_MarshalError(t *testing.T) {
    rs, mr := setupTestStore(t)
    defer teardownTestStore(rs, mr)

    ch := &store.ChannelEntry{Name: "test"}
    err := rs.CreateChannel(ch)
    if err != nil {
        t.Fatalf("CreateChannel normal should work: %v", err)
    }
}

func TestRedisStore_CreateTeam_MarshalError(t *testing.T) {
    rs, mr := setupTestStore(t)
    defer teardownTestStore(rs, mr)

    team := &store.Team{ID: "t1", Name: "test"}
    err := rs.CreateTeam(team)
    if err != nil {
        t.Fatalf("CreateTeam normal should work: %v", err)
    }
}

func TestRedisStore_CreateOrg_MarshalError(t *testing.T) {
    rs, mr := setupTestStore(t)
    defer teardownTestStore(rs, mr)

    org := &store.Organization{ID: "o1", Name: "test"}
    err := rs.CreateOrg(org)
    if err != nil {
        t.Fatalf("CreateOrg normal should work: %v", err)
    }
}

func TestRedisStore_UpdateBatch_MarshalError(t *testing.T) {
    rs, mr := setupTestStore(t)
    defer teardownTestStore(rs, mr)

    b, _ := rs.CreateBatch([]store.BatchRequest{{CustomID: "r1"}}, "", "")
    b.Status = store.BatchStatusRunning
    err := rs.UpdateBatch(b)
    if err != nil {
        t.Fatalf("UpdateBatch normal should work: %v", err)
    }
}

func TestRedisStore_RecordUsage_MarshalError(t *testing.T) {
    rs, mr := setupTestStore(t)
    defer teardownTestStore(rs, mr)

    err := rs.RecordUsage("key1", "cloud", "gpt-4", 100, 50, 0.01)
    if err != nil {
        t.Fatalf("RecordUsage normal should work: %v", err)
    }
}
