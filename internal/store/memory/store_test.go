package memory

import (
    "encoding/json"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/store"
)

func TestLogStore_AppendAndQuery(t *testing.T) {
    ls := NewLogStore(100)

    if err := ls.Append(&store.RequestLog{Model: "gpt-4", InputTokens: 100, OutputTokens: 50, TotalTokens: 150}); err != nil {
        t.Fatalf("Append failed: %v", err)
    }
    if err := ls.Append(&store.RequestLog{Model: "claude-3", InputTokens: 200, OutputTokens: 100, TotalTokens: 300}); err != nil {
        t.Fatalf("Append failed: %v", err)
    }

    filter := store.LogFilter{Page: 1, PageSize: 10}
    logs, total, err := ls.Query(filter)
    if err != nil {
        t.Fatalf("Query failed: %v", err)
    }
    if total != 2 {
        t.Fatalf("expected 2 logs, got %d", total)
    }
    if len(logs) != 2 {
        t.Fatalf("expected 2 logs in page, got %d", len(logs))
    }
}

func TestLogStore_RingBuffer(t *testing.T) {
    ls := NewLogStore(5)

    for i := 0; i < 10; i++ {
        if err := ls.Append(&store.RequestLog{Model: "test", InputTokens: i}); err != nil {
            t.Fatalf("Append failed: %v", err)
        }
    }

    filter := store.LogFilter{Page: 1, PageSize: 10}
    _, total, _ := ls.Query(filter)
    if total != 5 {
        t.Fatalf("expected 5 (ring buffer limit), got %d", total)
    }
}

func TestLogStore_Filter(t *testing.T) {
    ls := NewLogStore(100)

    if err := ls.Append(&store.RequestLog{Model: "gpt-4", ChannelType: "cloud", IsSuccess: true}); err != nil {
        t.Fatalf("Append failed: %v", err)
    }
    if err := ls.Append(&store.RequestLog{Model: "claude-3", ChannelType: "local", IsSuccess: false, StatusCode: 500}); err != nil {
        t.Fatalf("Append failed: %v", err)
    }

    filter := store.LogFilter{Model: "gpt-4", Page: 1, PageSize: 10}
    logs, total, _ := ls.Query(filter)
    if total != 1 {
        t.Fatalf("expected 1 filtered log, got %d", total)
    }
    if logs[0].Model != "gpt-4" {
        t.Fatalf("expected gpt-4, got %s", logs[0].Model)
    }
}

func TestLogStore_Export(t *testing.T) {
    ls := NewLogStore(100)
    if err := ls.Append(&store.RequestLog{Model: "gpt-4", InputTokens: 100}); err != nil {
        t.Fatalf("Append failed: %v", err)
    }

    filter := store.LogFilter{Page: 1, PageSize: 10}

    jsonData, err := ls.Export(filter, "json")
    if err != nil {
        t.Fatalf("JSON export failed: %v", err)
    }
    var parsed []map[string]interface{}
    if err := json.Unmarshal(jsonData, &parsed); err != nil {
        t.Fatalf("JSON parse failed: %v", err)
    }

    csvData, err := ls.Export(filter, "csv")
    if err != nil {
        t.Fatalf("CSV export failed: %v", err)
    }
    if len(csvData) == 0 {
        t.Fatal("CSV export empty")
    }
}

func TestKeyStore_CRUD(t *testing.T) {
    ks := NewKeyStore()

    key := &store.APIKeyEntry{Name: "test-key", BudgetLimit: 100}
    if err := ks.Create(key); err != nil {
        t.Fatalf("Create failed: %v", err)
    }

    got, err := ks.Get("test-key")
    if err != nil {
        t.Fatalf("Get failed: %v", err)
    }
    if got.Name != "test-key" {
        t.Fatalf("expected test-key, got %s", got.Name)
    }

    got.BudgetLimit = 200
    if err := ks.Update(got); err != nil {
        t.Fatalf("Update failed: %v", err)
    }

    updated, _ := ks.Get("test-key")
    if updated.BudgetLimit != 200 {
        t.Fatalf("expected 200, got %f", updated.BudgetLimit)
    }

    if err := ks.Delete("test-key"); err != nil {
        t.Fatalf("Delete failed: %v", err)
    }

    _, err = ks.Get("test-key")
    if err == nil {
        t.Fatal("expected error after delete")
    }
}

func TestChannelStore_CRUD(t *testing.T) {
    cs := NewChannelStore()

    ch := &store.ChannelEntry{Name: "openai", Type: "cloud"}
    if err := cs.Create(ch); err != nil {
        t.Fatalf("Create failed: %v", err)
    }

    got, err := cs.Get("openai")
    if err != nil {
        t.Fatalf("Get failed: %v", err)
    }
    if got.Name != "openai" {
        t.Fatalf("expected openai, got %s", got.Name)
    }

    if err := cs.Delete("openai"); err != nil {
        t.Fatalf("Delete failed: %v", err)
    }
}

func TestAnalyticsStore(t *testing.T) {
    ls := NewLogStore(100)
    as := NewAnalyticsStore(ls)

    now := time.Now()
    if err := ls.Append(&store.RequestLog{
        Model:        "gpt-4",
        ChannelType:  "cloud",
        InputTokens:  100,
        OutputTokens: 50,
        TotalTokens:  150,
        Cost:         0.01,
        Latency:      1.5,
        IsSuccess:    true,
        Timestamp:    now,
    }); err != nil {
        t.Fatalf("Append failed: %v", err)
    }
    if err := ls.Append(&store.RequestLog{
        Model:        "gpt-4",
        ChannelType:  "cloud",
        InputTokens:  200,
        OutputTokens: 100,
        TotalTokens:  300,
        Cost:         0.02,
        Latency:      2.5,
        IsSuccess:    true,
        Timestamp:    now,
    }); err != nil {
        t.Fatalf("Append failed: %v", err)
    }

    from := now.Add(-time.Hour)
    to := now.Add(time.Hour)

    tokenStats, err := as.GetTokenStats(from, to, "day")
    if err != nil {
        t.Fatalf("GetTokenStats failed: %v", err)
    }
    if len(tokenStats) == 0 {
        t.Fatal("expected token stats")
    }

    modelStats, err := as.GetModelStats(from, to)
    if err != nil {
        t.Fatalf("GetModelStats failed: %v", err)
    }
    if len(modelStats) != 1 || modelStats[0].Model != "gpt-4" {
        t.Fatalf("expected 1 model stat for gpt-4, got %v", modelStats)
    }
    if modelStats[0].RequestCount != 2 {
        t.Fatalf("expected 2 requests, got %d", modelStats[0].RequestCount)
    }

    latencyStats, err := as.GetLatencyStats(from, to)
    if err != nil {
        t.Fatalf("GetLatencyStats failed: %v", err)
    }
    if len(latencyStats) != 1 {
        t.Fatalf("expected 1 latency stat, got %d", len(latencyStats))
    }
}

func TestDashboardStore(t *testing.T) {
    ls := NewLogStore(100)
    ds := NewDashboardStore(ls)

    if err := ls.Append(&store.RequestLog{ChannelType: "local", TotalTokens: 100, Cost: 0.01}); err != nil {
        t.Fatalf("Append failed: %v", err)
    }
    if err := ls.Append(&store.RequestLog{ChannelType: "cloud", TotalTokens: 200, Cost: 0.02}); err != nil {
        t.Fatalf("Append failed: %v", err)
    }

    overview, err := ds.Overview()
    if err != nil {
        t.Fatalf("Overview failed: %v", err)
    }
    if overview.TotalRequests != 2 {
        t.Fatalf("expected 2 requests, got %d", overview.TotalRequests)
    }
    if overview.LocalHitRate != 0.5 {
        t.Fatalf("expected 0.5 local hit rate, got %f", overview.LocalHitRate)
    }
}

func TestQuotaStore(t *testing.T) {
    ks := NewKeyStore()
    qs := NewQuotaStore(ks)

    if err := ks.Create(&store.APIKeyEntry{Name: "test-key", BudgetLimit: 100}); err != nil {
        t.Fatalf("Create failed: %v", err)
    }

    used, limit, exceeded, err := qs.Check("test-key")
    if err != nil {
        t.Fatalf("Check failed: %v", err)
    }
    if used != 0 || limit != 100 || exceeded {
        t.Fatalf("expected 0/100/not exceeded, got %f/%f/%v", used, limit, exceeded)
    }

    if err := qs.Deduct("test-key", 50); err != nil {
        t.Fatalf("Deduct failed: %v", err)
    }

    used, _, exceeded, _ = qs.Check("test-key")
    if used != 50 {
        t.Fatalf("expected 50 used, got %f", used)
    }
    if exceeded {
        t.Fatal("should not be exceeded yet")
    }

    if err := qs.Deduct("test-key", 60); err != nil {
        t.Fatalf("Deduct failed: %v", err)
    }
    _, _, exceeded, _ = qs.Check("test-key")
    if !exceeded {
        t.Fatal("should be exceeded now")
    }
}

func TestMemoryStore_FullIntegration(t *testing.T) {
    ms := NewMemoryStore(100)

    if err := ms.AppendLog(&store.RequestLog{Model: "gpt-4", InputTokens: 100, OutputTokens: 50, TotalTokens: 150}); err != nil {
        t.Fatalf("AppendLog failed: %v", err)
    }

    filter := store.LogFilter{Page: 1, PageSize: 10}
    _, total, _ := ms.QueryLogs(filter)
    if total != 1 {
        t.Fatalf("expected 1, got %d", total)
    }

    if err := ms.CreateKey(&store.APIKeyEntry{Name: "key1"}); err != nil {
        t.Fatalf("CreateKey failed: %v", err)
    }
    keys, _ := ms.ListKeys()
    if len(keys) != 1 {
        t.Fatalf("expected 1 key, got %d", len(keys))
    }

    if err := ms.CreateChannel(&store.ChannelEntry{Name: "ch1"}); err != nil {
        t.Fatalf("CreateChannel failed: %v", err)
    }
    channels, _ := ms.ListChannels()
    if len(channels) != 1 {
        t.Fatalf("expected 1 channel, got %d", len(channels))
    }

    overview, _ := ms.GetDashboardOverview()
    if overview.TotalRequests != 1 {
        t.Fatalf("expected 1 request, got %d", overview.TotalRequests)
    }
}
