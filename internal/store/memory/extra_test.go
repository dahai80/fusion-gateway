package memory

import (
    "fmt"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/store"
)

func TestNewMemoryStoreWithConfig(t *testing.T) {
    cfg := config.BatchConfig{Enabled: true, MaxBatchSize: 50}
    ms := NewMemoryStoreWithConfig(100, cfg)
    if ms == nil {
        t.Fatal("expected non-nil store")
    }
}

func TestNewMemoryStoreWithConfig_DefaultBatchSize(t *testing.T) {
    cfg := config.BatchConfig{Enabled: true, MaxBatchSize: 0}
    ms := NewMemoryStoreWithConfig(100, cfg)
    b, err := ms.CreateBatch(
        []store.BatchRequest{{CustomID: "r1"}},
        "/v1/chat/completions", "24h",
    )
    if err != nil {
        t.Fatal(err)
    }
    if b == nil {
        t.Fatal("expected non-nil batch")
    }
}

func TestMemoryStore_Batch_NilBatch(t *testing.T) {
    ms := NewMemoryStore(100)
    b, err := ms.CreateBatch(nil, "", "")
    if b != nil || err != nil {
        t.Fatalf("expected nil batch with nil store, got %v err=%v", b, err)
    }
    b2, err2 := ms.GetBatch("any")
    if b2 != nil || err2 != nil {
        t.Fatalf("expected nil, got %v err=%v", b2, err2)
    }
    list, err3 := ms.ListBatches()
    if list != nil || err3 != nil {
        t.Fatalf("expected nil, got %v err=%v", list, err3)
    }
    b4, err4 := ms.CancelBatch("any")
    if b4 != nil || err4 != nil {
        t.Fatalf("expected nil, got %v err=%v", b4, err4)
    }
    err5 := ms.UpdateBatch(&store.Batch{ID: "x"})
    if err5 != nil {
        t.Fatalf("expected nil err, got %v", err5)
    }
}

func TestMemoryStore_RecordUsage(t *testing.T) {
    ms := NewMemoryStore(100)
    err := ms.RecordUsage("key1", "cloud", "gpt-4", 100, 50, 0.01)
    if err != nil {
        t.Fatal(err)
    }
    summary, err := ms.GetCostSummary("key1")
    if err != nil {
        t.Fatal(err)
    }
    if summary.TotalCostUSD != 0.01 {
        t.Fatalf("expected 0.01, got %f", summary.TotalCostUSD)
    }
}

func TestMemoryStore_GetCostSummaryAll(t *testing.T) {
    ms := NewMemoryStore(100)
    _ = ms.RecordUsage("key1", "cloud", "gpt-4", 100, 50, 0.01)
    _ = ms.RecordUsage("key2", "local", "llama3", 200, 100, 0.005)
    summary, err := ms.GetCostSummaryAll()
    if err != nil {
        t.Fatal(err)
    }
    if summary.TotalCostUSD != 0.015 {
        t.Fatalf("expected 0.015, got %f", summary.TotalCostUSD)
    }
    if summary.TotalRequests != 2 {
        t.Fatalf("expected 2 requests, got %d", summary.TotalRequests)
    }
}

func TestMemoryStore_Teams_CRUD(t *testing.T) {
    ms := NewMemoryStore(100)
    err := ms.CreateTeam(&store.Team{ID: "team1", Name: "Team One"})
    if err != nil {
        t.Fatal(err)
    }
    team, err := ms.GetTeam("team1")
    if err != nil {
        t.Fatal(err)
    }
    if team.Name != "Team One" {
        t.Fatalf("expected Team One, got %s", team.Name)
    }
    teams, err := ms.ListTeams()
    if err != nil {
        t.Fatal(err)
    }
    if len(teams) < 2 {
        t.Fatalf("expected at least 2 teams, got %d", len(teams))
    }
    err = ms.UpdateTeam(&store.Team{ID: "team1", Name: "Updated"})
    if err != nil {
        t.Fatal(err)
    }
    updated, _ := ms.GetTeam("team1")
    if updated.Name != "Updated" {
        t.Fatalf("expected Updated, got %s", updated.Name)
    }
    err = ms.DeleteTeam("team1")
    if err != nil {
        t.Fatal(err)
    }
}

func TestMemoryStore_Teams_Members(t *testing.T) {
    ms := NewMemoryStore(100)
    err := ms.AddTeamMember("default", "user1", "editor")
    if err != nil {
        t.Fatal(err)
    }
    err = ms.RemoveTeamMember("default", "user1")
    if err != nil {
        t.Fatal(err)
    }
}

func TestMemoryStore_Teams_QuotaAndCost(t *testing.T) {
    ms := NewMemoryStore(100)
    _ = ms.AddTeamCost("default", 5.0)
    limit, used, ok, err := ms.CheckTeamQuota("default")
    if err != nil {
        t.Fatal(err)
    }
    if used != 5.0 {
        t.Fatalf("expected 5.0 used, got %f", used)
    }
    if !ok {
        t.Fatal("should have quota remaining")
    }
    t.Logf("limit=%f used=%f ok=%v", limit, used, ok)
}

func TestMemoryStore_Orgs(t *testing.T) {
    ms := NewMemoryStore(100)
    _ = ms.CreateOrg(&store.Organization{ID: "org2", Name: "Org Two"})
    org, err := ms.GetOrg("org2")
    if err != nil {
        t.Fatal(err)
    }
    if org.Name != "Org Two" {
        t.Fatalf("expected Org Two, got %s", org.Name)
    }
    orgs, err := ms.ListOrgs()
    if err != nil {
        t.Fatal(err)
    }
    if len(orgs) < 2 {
        t.Fatalf("expected at least 2, got %d", len(orgs))
    }
}

func TestMemoryStore_Batch_CRUD(t *testing.T) {
    cfg := config.BatchConfig{Enabled: true, MaxBatchSize: 10}
    ms := NewMemoryStoreWithConfig(100, cfg)
    reqs := []store.BatchRequest{
        {CustomID: "r1", Method: "POST", URL: "/v1/chat/completions"},
        {CustomID: "r2", Method: "POST", URL: "/v1/chat/completions"},
    }
    b, err := ms.CreateBatch(reqs, "/v1/chat/completions", "24h")
    if err != nil {
        t.Fatal(err)
    }
    if b.Status != store.BatchStatusPending {
        t.Fatalf("expected pending, got %s", b.Status)
    }
    got, err := ms.GetBatch(b.ID)
    if err != nil {
        t.Fatal(err)
    }
    if got.ID != b.ID {
        t.Fatalf("expected %s, got %s", b.ID, got.ID)
    }
    list, err := ms.ListBatches()
    if err != nil {
        t.Fatal(err)
    }
    if len(list) != 1 {
        t.Fatalf("expected 1, got %d", len(list))
    }
    cancelled, err := ms.CancelBatch(b.ID)
    if err != nil {
        t.Fatal(err)
    }
    if cancelled.Status != store.BatchStatusCancelled {
        t.Fatalf("expected cancelled, got %s", cancelled.Status)
    }
}

func TestMemoryStore_Batch_ExceedMaxSize(t *testing.T) {
    cfg := config.BatchConfig{Enabled: true, MaxBatchSize: 2}
    ms := NewMemoryStoreWithConfig(100, cfg)
    reqs := []store.BatchRequest{
        {CustomID: "r1"}, {CustomID: "r2"}, {CustomID: "r3"},
    }
    _, err := ms.CreateBatch(reqs, "", "")
    if err == nil {
        t.Fatal("expected error for exceeding max batch size")
    }
    t.Logf("error: %v", err)
}

func TestMemoryStore_Batch_CancelAlreadyDone(t *testing.T) {
    cfg := config.BatchConfig{Enabled: true, MaxBatchSize: 10}
    ms := NewMemoryStoreWithConfig(100, cfg)
    b, _ := ms.CreateBatch([]store.BatchRequest{{CustomID: "r1"}}, "", "")
    b.Status = store.BatchStatusCompleted
    _ = ms.UpdateBatch(b)
    _, err := ms.CancelBatch(b.ID)
    if err == nil {
        t.Fatal("should fail cancelling completed batch")
    }
}

func TestMemoryStore_Batch_GetNotFound(t *testing.T) {
    cfg := config.BatchConfig{Enabled: true, MaxBatchSize: 10}
    ms := NewMemoryStoreWithConfig(100, cfg)
    _, err := ms.GetBatch("nonexistent")
    if err == nil {
        t.Fatal("expected error for missing batch")
    }
}

func TestMemoryStore_Batch_UpdateNotFound(t *testing.T) {
    cfg := config.BatchConfig{Enabled: true, MaxBatchSize: 10}
    ms := NewMemoryStoreWithConfig(100, cfg)
    err := ms.UpdateBatch(&store.Batch{ID: "nonexistent"})
    if err == nil {
        t.Fatal("expected error for missing batch update")
    }
}

func TestLogStore_DefaultMaxLen(t *testing.T) {
    ls := NewLogStore(0)
    if ls.maxLen != 10000 {
        t.Fatalf("expected default 10000, got %d", ls.maxLen)
    }
}

func TestLogStore_GetByID(t *testing.T) {
    ls := NewLogStore(100)
    _ = ls.Append(&store.RequestLog{ID: "log-1", Model: "gpt-4"})
    got, err := ls.Get("log-1")
    if err != nil {
        t.Fatal(err)
    }
    if got.Model != "gpt-4" {
        t.Fatalf("expected gpt-4, got %s", got.Model)
    }
}

func TestLogStore_Get_NotFound(t *testing.T) {
    ls := NewLogStore(100)
    _, err := ls.Get("nonexistent")
    if err == nil {
        t.Fatal("expected error for missing log")
    }
}

func TestLogStore_Filter_Status(t *testing.T) {
    ls := NewLogStore(100)
    _ = ls.Append(&store.RequestLog{Model: "gpt-4", IsSuccess: true})
    _ = ls.Append(&store.RequestLog{Model: "claude-3", IsSuccess: false, StatusCode: 500})
    filter := store.LogFilter{Status: "error", Page: 1, PageSize: 10}
    logs, total, _ := ls.Query(filter)
    if total != 1 {
        t.Fatalf("expected 1 error, got %d", total)
    }
    if len(logs) > 0 && logs[0].Model != "claude-3" {
        t.Fatalf("expected claude-3, got %s", logs[0].Model)
    }
}

func TestLogStore_Filter_KeyName(t *testing.T) {
    ls := NewLogStore(100)
    _ = ls.Append(&store.RequestLog{APIKeyName: "key1", Model: "gpt-4"})
    _ = ls.Append(&store.RequestLog{APIKeyName: "key2", Model: "claude-3"})
    filter := store.LogFilter{KeyName: "key1", Page: 1, PageSize: 10}
    _, total, _ := ls.Query(filter)
    if total != 1 {
        t.Fatalf("expected 1, got %d", total)
    }
}

func TestLogStore_Filter_MinTokens(t *testing.T) {
    ls := NewLogStore(100)
    _ = ls.Append(&store.RequestLog{TotalTokens: 50})
    _ = ls.Append(&store.RequestLog{TotalTokens: 200})
    filter := store.LogFilter{MinTokens: 100, Page: 1, PageSize: 10}
    _, total, _ := ls.Query(filter)
    if total != 1 {
        t.Fatalf("expected 1, got %d", total)
    }
}

func TestLogStore_Filter_MinCost(t *testing.T) {
    ls := NewLogStore(100)
    _ = ls.Append(&store.RequestLog{Cost: 0.001})
    _ = ls.Append(&store.RequestLog{Cost: 0.05})
    filter := store.LogFilter{MinCost: 0.01, Page: 1, PageSize: 10}
    _, total, _ := ls.Query(filter)
    if total != 1 {
        t.Fatalf("expected 1, got %d", total)
    }
}

func TestLogStore_Filter_TimeRange(t *testing.T) {
    ls := NewLogStore(100)
    now := time.Now()
    _ = ls.Append(&store.RequestLog{Model: "old", Timestamp: now.Add(-2 * time.Hour)})
    _ = ls.Append(&store.RequestLog{Model: "new", Timestamp: now})
    start := now.Add(-time.Hour)
    filter := store.LogFilter{StartTime: &start, Page: 1, PageSize: 10}
    _, total, _ := ls.Query(filter)
    if total != 1 {
        t.Fatalf("expected 1, got %d", total)
    }
}

func TestLogStore_Filter_Channel(t *testing.T) {
    ls := NewLogStore(100)
    _ = ls.Append(&store.RequestLog{ChannelName: "ch1"})
    _ = ls.Append(&store.RequestLog{ChannelName: "ch2"})
    filter := store.LogFilter{Channel: "ch1", Page: 1, PageSize: 10}
    _, total, _ := ls.Query(filter)
    if total != 1 {
        t.Fatalf("expected 1, got %d", total)
    }
}

func TestLogStore_Pagination(t *testing.T) {
    ls := NewLogStore(100)
    for i := 0; i < 25; i++ {
        _ = ls.Append(&store.RequestLog{Model: "gpt-4"})
    }
    filter := store.LogFilter{Page: 2, PageSize: 10}
    logs, total, _ := ls.Query(filter)
    if total != 25 {
        t.Fatalf("expected total 25, got %d", total)
    }
    if len(logs) != 10 {
        t.Fatalf("expected 10 on page 2, got %d", len(logs))
    }
}

func TestLogStore_Pagination_BeyondEnd(t *testing.T) {
    ls := NewLogStore(100)
    _ = ls.Append(&store.RequestLog{Model: "gpt-4"})
    filter := store.LogFilter{Page: 99, PageSize: 10}
    logs, total, _ := ls.Query(filter)
    if total != 1 {
        t.Fatalf("expected total 1, got %d", total)
    }
    if len(logs) != 0 {
        t.Fatalf("expected 0 on page 99, got %d", len(logs))
    }
}

func TestLogStore_AutoID(t *testing.T) {
    ls := NewLogStore(100)
    _ = ls.Append(&store.RequestLog{Model: "gpt-4"})
    logs, _, _ := ls.Query(store.LogFilter{Page: 1, PageSize: 10})
    if len(logs) == 0 || logs[0].ID == "" {
        t.Fatal("expected auto-generated ID")
    }
}

func TestLogStore_AutoTimestamp(t *testing.T) {
    ls := NewLogStore(100)
    _ = ls.Append(&store.RequestLog{Model: "gpt-4"})
    logs, _, _ := ls.Query(store.LogFilter{Page: 1, PageSize: 10})
    if len(logs) == 0 || logs[0].Timestamp.IsZero() {
        t.Fatal("expected auto-generated timestamp")
    }
}

func TestKeyStore_CreateDuplicate(t *testing.T) {
    ks := NewKeyStore()
    _ = ks.Create(&store.APIKeyEntry{Name: "dup"})
    err := ks.Create(&store.APIKeyEntry{Name: "dup"})
    if err == nil {
        t.Fatal("expected error on duplicate key")
    }
}

func TestKeyStore_UpdateNotFound(t *testing.T) {
    ks := NewKeyStore()
    err := ks.Update(&store.APIKeyEntry{Name: "missing"})
    if err == nil {
        t.Fatal("expected error for missing key")
    }
}

func TestKeyStore_DeleteNotFound(t *testing.T) {
    ks := NewKeyStore()
    err := ks.Delete("missing")
    if err == nil {
        t.Fatal("expected error for missing key")
    }
}

func TestKeyStore_GetNotFound(t *testing.T) {
    ks := NewKeyStore()
    _, err := ks.Get("missing")
    if err == nil {
        t.Fatal("expected error for missing key")
    }
}

func TestKeyStore_LoadFromConfig(t *testing.T) {
    ks := NewKeyStore()
    ks.LoadFromConfig(map[string]*store.APIKeyEntry{
        "cfg-key": {Name: "cfg-key", Status: "active"},
    })
    got, err := ks.Get("cfg-key")
    if err != nil {
        t.Fatal(err)
    }
    if got.Name != "cfg-key" {
        t.Fatalf("expected cfg-key, got %s", got.Name)
    }
}

func TestKeyStore_DefaultStatus(t *testing.T) {
    ks := NewKeyStore()
    _ = ks.Create(&store.APIKeyEntry{Name: "k1"})
    got, _ := ks.Get("k1")
    if got.Status != "active" {
        t.Fatalf("expected active, got %s", got.Status)
    }
}

func TestChannelStore_CreateDuplicate(t *testing.T) {
    cs := NewChannelStore()
    _ = cs.Create(&store.ChannelEntry{Name: "ch1"})
    err := cs.Create(&store.ChannelEntry{Name: "ch1"})
    if err == nil {
        t.Fatal("expected error on duplicate channel")
    }
}

func TestChannelStore_UpdateNotFound(t *testing.T) {
    cs := NewChannelStore()
    err := cs.Update(&store.ChannelEntry{Name: "missing"})
    if err == nil {
        t.Fatal("expected error for missing channel")
    }
}

func TestChannelStore_DeleteNotFound(t *testing.T) {
    cs := NewChannelStore()
    err := cs.Delete("missing")
    if err == nil {
        t.Fatal("expected error for missing channel")
    }
}

func TestChannelStore_DefaultStatus(t *testing.T) {
    cs := NewChannelStore()
    _ = cs.Create(&store.ChannelEntry{Name: "ch1"})
    got, _ := cs.Get("ch1")
    if got.Status != "online" {
        t.Fatalf("expected online, got %s", got.Status)
    }
}

func TestChannelStore_LoadFromConfig(t *testing.T) {
    cs := NewChannelStore()
    cs.LoadFromConfig(map[string]*store.ChannelEntry{
        "cfg-ch": {Name: "cfg-ch"},
    })
    got, err := cs.Get("cfg-ch")
    if err != nil {
        t.Fatal(err)
    }
    if got.Name != "cfg-ch" {
        t.Fatalf("expected cfg-ch, got %s", got.Name)
    }
}

func TestQuotaStore_CheckNoKey(t *testing.T) {
    ks := NewKeyStore()
    qs := NewQuotaStore(ks)
    used, limit, _, err := qs.Check("nonexistent")
    if err == nil {
        t.Fatal("expected error for missing key")
    }
    t.Logf("used=%f limit=%f err=%v", used, limit, err)
}

func TestQuotaStore_GetSetUsage(t *testing.T) {
    ks := NewKeyStore()
    qs := NewQuotaStore(ks)
    qs.SetUsage("k1", 42.0)
    got := qs.GetUsage("k1")
    if got != 42.0 {
        t.Fatalf("expected 42, got %f", got)
    }
}

func TestAnalyticsStore_CostStats(t *testing.T) {
    ls := NewLogStore(100)
    as := NewAnalyticsStore(ls)
    now := time.Now()
    _ = ls.Append(&store.RequestLog{Cost: 0.01, CostUSD: 0.01, LocalSavings: 0.005, Timestamp: now})
    stats, err := as.GetCostStats(now.Add(-time.Hour), now.Add(time.Hour), "day")
    if err != nil {
        t.Fatal(err)
    }
    if len(stats) == 0 {
        t.Fatal("expected cost stats")
    }
    t.Logf("cost stat: %+v", stats[0])
}

func TestAnalyticsStore_ErrorStats(t *testing.T) {
    ls := NewLogStore(100)
    as := NewAnalyticsStore(ls)
    now := time.Now()
    _ = ls.Append(&store.RequestLog{ChannelName: "cloud", Model: "gpt-4", IsSuccess: false, StatusCode: 500, Timestamp: now})
    stats, err := as.GetErrorStats(now.Add(-time.Hour), now.Add(time.Hour))
    if err != nil {
        t.Fatal(err)
    }
    if len(stats) == 0 {
        t.Fatal("expected error stats")
    }
    if stats[0].ErrorType != "5xx" {
        t.Fatalf("expected 5xx, got %s", stats[0].ErrorType)
    }
}

func TestAnalyticsStore_BucketKey(t *testing.T) {
    now := time.Now()
    cases := []struct {
        groupBy  string
        expected string
    }{
        {"hour", now.Format("2006-01-02T15:04")},
        {"day", now.Format("2006-01-02")},
        {"month", now.Format("2006-01")},
        {"unknown", now.Format("2006-01-02")},
    }
    for _, c := range cases {
        got := bucketKey(now, c.groupBy)
        if got != c.expected {
            t.Errorf("bucketKey(%v, %q) = %q, want %q", now, c.groupBy, got, c.expected)
        }
    }
}

func TestAnalyticsStore_BucketKey_Week(t *testing.T) {
    now := time.Now()
    got := bucketKey(now, "week")
    y, w := now.ISOWeek()
    expected := fmt.Sprintf("%04d-W%02d", y, w)
    if got != expected {
        t.Errorf("bucketKey(week) = %q, want %q", got, expected)
    }
}

func TestPercentile_Empty(t *testing.T) {
    p := percentile([]float64{}, 50)
    if p != 0 {
        t.Fatalf("expected 0 for empty, got %f", p)
    }
}

func TestErrorType(t *testing.T) {
    cases := []struct {
        code int
        want string
    }{
        {200, "unknown"},
        {400, "4xx"},
        {404, "4xx"},
        {500, "5xx"},
        {503, "5xx"},
    }
    for _, c := range cases {
        got := errorType(c.code)
        if got != c.want {
            t.Errorf("errorType(%d) = %q, want %q", c.code, got, c.want)
        }
    }
}

func TestProfitStore(t *testing.T) {
    ls := NewLogStore(100)
    ps := NewProfitStore(ls)
    now := time.Now()
    _ = ls.Append(&store.RequestLog{APIKeyName: "key1", InputTokens: 100, OutputTokens: 50, Cost: 0.01, Timestamp: now})
    _ = ls.Append(&store.RequestLog{APIKeyName: "key1", InputTokens: 200, OutputTokens: 100, Cost: 0.02, Timestamp: now})
    _ = ls.Append(&store.RequestLog{APIKeyName: "", InputTokens: 50, OutputTokens: 25, Cost: 0.005, Timestamp: now})

    stats, err := ps.GetKeyProfitStats(now.Add(-time.Hour), now.Add(time.Hour))
    if err != nil {
        t.Fatal(err)
    }
    if len(stats) == 0 {
        t.Fatal("expected profit stats")
    }
    for _, s := range stats {
        t.Logf("key=%s ratio=%f cost=%f", s.KeyName, s.Ratio, s.TotalCost)
    }
}

func TestTeamsStore_UpdateNotFound(t *testing.T) {
    s := NewTeamsStore()
    err := s.UpdateTeam(&store.Team{ID: "nonexistent"})
    if err == nil {
        t.Fatal("expected error for missing team")
    }
}

func TestTeamsStore_DeleteNotFound(t *testing.T) {
    s := NewTeamsStore()
    err := s.DeleteTeam("nonexistent")
    if err == nil {
        t.Fatal("expected error for missing team")
    }
}

func TestTeamsStore_DeleteTeam_CleansKeyBindings(t *testing.T) {
    s := NewTeamsStore()
    _ = s.CreateTeam(&store.Team{ID: "t1", Name: "Temp"})
    _ = s.BindKeyToTeam("sk-x", "t1")
    _ = s.DeleteTeam("t1")
    _, err := s.GetTeamByKey("sk-x")
    if err == nil {
        t.Fatal("key binding should be removed with team")
    }
}

func TestTeamsStore_BindKeyToTeam_MissingTeam(t *testing.T) {
    s := NewTeamsStore()
    err := s.BindKeyToTeam("sk-x", "nonexistent")
    if err == nil {
        t.Fatal("expected error for missing team")
    }
}

func TestTeamsStore_GetTeamByKey_NotBound(t *testing.T) {
    s := NewTeamsStore()
    _, err := s.GetTeamByKey("sk-unbound")
    if err == nil {
        t.Fatal("expected error for unbound key")
    }
}

func TestTeamsStore_AddMember_Duplicate(t *testing.T) {
    s := NewTeamsStore()
    _ = s.AddMember("default", "user1", "editor")
    err := s.AddMember("default", "user1", "admin")
    if err == nil {
        t.Fatal("expected error for duplicate member")
    }
}

func TestTeamsStore_RemoveMember_NotInTeam(t *testing.T) {
    s := NewTeamsStore()
    err := s.RemoveMember("default", "nonexistent-user")
    if err == nil {
        t.Fatal("expected error for missing member")
    }
}

func TestTeamsStore_AddCost_MissingTeam(t *testing.T) {
    s := NewTeamsStore()
    err := s.AddCost("nonexistent", 1.0)
    if err == nil {
        t.Fatal("expected error for missing team")
    }
}

func TestTeamsStore_CheckQuota_MissingTeam(t *testing.T) {
    s := NewTeamsStore()
    _, _, ok := s.CheckQuota("nonexistent")
    if ok {
        t.Fatal("expected false for missing team")
    }
}

func TestTeamsStore_CreateOrg_Duplicate(t *testing.T) {
    s := NewTeamsStore()
    err := s.CreateOrg(&store.Organization{ID: "default", Name: "dup"})
    if err == nil {
        t.Fatal("expected error for duplicate org")
    }
}

func TestTeamsStore_GetOrg_NotFound(t *testing.T) {
    s := NewTeamsStore()
    _, err := s.GetOrg("nonexistent")
    if err == nil {
        t.Fatal("expected error for missing org")
    }
}

func TestTeamsStore_DeleteOrg_NotFound(t *testing.T) {
    s := NewTeamsStore()
    err := s.DeleteOrg("nonexistent")
    if err == nil {
        t.Fatal("expected error for missing org")
    }
}

func TestMemoryStore_Accessors(t *testing.T) {
    ms := NewMemoryStore(100)
    if ms.KeyStore() == nil {
        t.Fatal("KeyStore should not be nil")
    }
    if ms.ChannelStore() == nil {
        t.Fatal("ChannelStore should not be nil")
    }
    if ms.QuotaStore() == nil {
        t.Fatal("QuotaStore should not be nil")
    }
    if ms.TeamsStore() == nil {
        t.Fatal("TeamsStore should not be nil")
    }
}

func TestMemoryStore_KeyCRUD_ViaStore(t *testing.T) {
    ms := NewMemoryStore(100)
    _ = ms.CreateKey(&store.APIKeyEntry{Name: "k1", BudgetLimit: 100})
    k, err := ms.GetKey("k1")
    if err != nil {
        t.Fatal(err)
    }
    if k.Name != "k1" {
        t.Fatalf("expected k1, got %s", k.Name)
    }
    _ = ms.UpdateKey(&store.APIKeyEntry{Name: "k1", BudgetLimit: 200})
    updated, _ := ms.GetKey("k1")
    if updated.BudgetLimit != 200 {
        t.Fatalf("expected 200, got %f", updated.BudgetLimit)
    }
    _ = ms.DeleteKey("k1")
    _, err = ms.GetKey("k1")
    if err == nil {
        t.Fatal("expected error after delete")
    }
}

func TestMemoryStore_ChannelCRUD_ViaStore(t *testing.T) {
    ms := NewMemoryStore(100)
    _ = ms.CreateChannel(&store.ChannelEntry{Name: "ch1", Type: "cloud"})
    ch, err := ms.GetChannel("ch1")
    if err != nil {
        t.Fatal(err)
    }
    if ch.Name != "ch1" {
        t.Fatalf("expected ch1, got %s", ch.Name)
    }
    _ = ms.UpdateChannel(&store.ChannelEntry{Name: "ch1", Type: "local"})
    updated, _ := ms.GetChannel("ch1")
    if updated.Type != "local" {
        t.Fatalf("expected local, got %s", updated.Type)
    }
    _ = ms.DeleteChannel("ch1")
    _, err = ms.GetChannel("ch1")
    if err == nil {
        t.Fatal("expected error after delete")
    }
}

func TestMemoryStore_LogExport_CSV(t *testing.T) {
    ms := NewMemoryStore(100)
    _ = ms.AppendLog(&store.RequestLog{Model: "gpt-4", ID: "=INJECT"})
    data, err := ms.ExportLogs(store.LogFilter{Page: 1, PageSize: 10}, "csv")
    if err != nil {
        t.Fatal(err)
    }
    t.Logf("csv output length: %d", len(data))
}

func TestCostSubStore_MaxRecords(t *testing.T) {
    cs := NewCostSubStore(3)
    cs.Record("k1", "cloud", "gpt-4", 100, 50, 0.01)
    cs.Record("k1", "cloud", "gpt-4", 100, 50, 0.02)
    cs.Record("k1", "cloud", "gpt-4", 100, 50, 0.03)
    cs.Record("k1", "cloud", "gpt-4", 100, 50, 0.04)
    s := cs.Summary("")
    if s.TotalRequests != 3 {
        t.Fatalf("expected 3 (ring buffer), got %d", s.TotalRequests)
    }
}

func TestNewBatchSubStore_DefaultMaxBatch(t *testing.T) {
    bs := NewBatchSubStore(0)
    if bs.maxBatch != 100 {
        t.Fatalf("expected default 100, got %d", bs.maxBatch)
    }
}
