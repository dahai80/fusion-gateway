package store

import (
    "encoding/json"
    "testing"
    "time"
)

func TestRequestLog_Fields(t *testing.T) {
    now := time.Now()
    log := &RequestLog{
        ID:           "log-1",
        RequestID:    "req-1",
        Timestamp:    now,
        APIKeyName:   "test-key",
        Model:        "test-model",
        RequestType:  "chat",
        IsStream:     true,
        ChannelName:  "ch-1",
        ChannelType:  "cloud",
        RouteReason:  "token_budget",
        ProjectID:    "proj-1",
        ChatID:       "chat-1",
        SpaceID:      "space-1",
        InputTokens:  100,
        OutputTokens: 50,
        TotalTokens:  150,
        Cost:         0.01,
        CostUSD:      0.001,
        LocalSavings: 0.009,
        Latency:      120.5,
        TTFT:         50.3,
        StatusCode:   200,
        IsSuccess:    true,
    }
    if log.ID != "log-1" {
        t.Fatalf("expected log-1, got %s", log.ID)
    }
    if log.TotalTokens != 150 {
        t.Fatalf("expected 150, got %d", log.TotalTokens)
    }
}

func TestAPIKeyEntry_Fields(t *testing.T) {
    now := time.Now()
    key := &APIKeyEntry{
        Name:            "test-key",
        KeyPrefix:       "fk-",
        Status:          "active",
        QuotaLimit:      100.0,
        QuotaUsed:       50.0,
        QuotaRemaining:  50.0,
        AllowedModels:   []string{"model-1", "model-2"},
        RPM:             60,
        TPM:             100000,
        ExpiresAt:       &now,
        BudgetLimit:     200.0,
        AllowedBackends: []string{"cloud-1"},
        Metadata:        map[string]string{"env": "test"},
        CreatedAt:       now,
        UpdatedAt:       now,
    }
    if key.Name != "test-key" {
        t.Fatalf("expected test-key, got %s", key.Name)
    }
    if len(key.AllowedModels) != 2 {
        t.Fatalf("expected 2 models, got %d", len(key.AllowedModels))
    }
}

func TestChannelEntry_Fields(t *testing.T) {
    ch := &ChannelEntry{
        Name:     "test-ch",
        Type:     "cloud",
        Provider: "openai",
        BaseURL:  "https://api.openai.com",
        Status:   "active",
        Priority: 1,
        Weight:   100,
        Models:   []string{"gpt-4"},
        Enabled:  true,
    }
    if ch.Name != "test-ch" {
        t.Fatalf("expected test-ch, got %s", ch.Name)
    }
    if !ch.Enabled {
        t.Fatal("expected enabled")
    }
}

func TestLogFilter_Fields(t *testing.T) {
    now := time.Now()
    f := LogFilter{
        StartTime: &now,
        EndTime:   &now,
        KeyName:   "key-1",
        Model:     "model-1",
        Channel:   "ch-1",
        Status:    "success",
        MinTokens: 100,
        MinCost:   0.01,
        Page:      1,
        PageSize:  20,
    }
    if f.Page != 1 {
        t.Fatalf("expected page 1, got %d", f.Page)
    }
}

func TestStatTypes(t *testing.T) {
    ts := TokenStat{Time: "2024-01-01", InputTokens: 100, OutputTokens: 50, TotalTokens: 150}
    if ts.TotalTokens != 150 {
        t.Fatalf("expected 150, got %d", ts.TotalTokens)
    }

    cs := CostStat{Time: "2024-01-01", Cost: 1.0, CostUSD: 0.1, Savings: 0.9}
    if cs.Savings != 0.9 {
        t.Fatalf("expected 0.9, got %f", cs.Savings)
    }

    ms := ModelStat{Model: "gpt-4", RequestCount: 100, InputTokens: 500, OutputTokens: 200, Cost: 1.5}
    if ms.RequestCount != 100 {
        t.Fatalf("expected 100, got %d", ms.RequestCount)
    }

    ls := LatencyStat{Channel: "cloud", P50: 100, P90: 200, P99: 500}
    if ls.P99 != 500 {
        t.Fatalf("expected 500, got %f", ls.P99)
    }

    es := ErrorStat{Channel: "cloud", Model: "gpt-4", ErrorType: "timeout", Count: 3}
    if es.Count != 3 {
        t.Fatalf("expected 3, got %d", es.Count)
    }
}

func TestDashboardOverview(t *testing.T) {
    d := &DashboardOverview{
        TotalRequests:     1000,
        TotalTokens:       500000,
        TotalCost:         50.0,
        LocalHitRate:      0.75,
        RequestsTrend:     []TokenStat{{Time: "2024-01-01"}},
        TokensTrend:       []TokenStat{{Time: "2024-01-01", TotalTokens: 1000}},
        CostTrend:         []CostTrendItem{{Date: "2024-01-01", Cost: 10.0}},
        ModelDistribution: []ModelDistItem{{Model: "gpt-4", Count: 500}},
        RouteDistribution: map[string]float64{"local": 0.75, "cloud": 0.25},
    }
    if d.TotalRequests != 1000 {
        t.Fatalf("expected 1000, got %d", d.TotalRequests)
    }
    if d.LocalHitRate != 0.75 {
        t.Fatalf("expected 0.75, got %f", d.LocalHitRate)
    }
}

func TestKeyProfitStat(t *testing.T) {
    kps := KeyProfitStat{
        KeyName:      "key-1",
        TotalInput:   1000,
        TotalOutput:  500,
        Ratio:        2.0,
        TotalCost:    10.0,
        RequestCount: 50,
    }
    if kps.Ratio != 2.0 {
        t.Fatalf("expected 2.0, got %f", kps.Ratio)
    }
}

func TestBatchConstants(t *testing.T) {
    if BatchStatusPending != "pending" {
        t.Fatalf("expected pending, got %s", BatchStatusPending)
    }
    if BatchStatusRunning != "running" {
        t.Fatalf("expected running, got %s", BatchStatusRunning)
    }
    if BatchStatusCompleted != "completed" {
        t.Fatalf("expected completed, got %s", BatchStatusCompleted)
    }
    if BatchStatusFailed != "failed" {
        t.Fatalf("expected failed, got %s", BatchStatusFailed)
    }
    if BatchStatusCancelled != "cancelled" {
        t.Fatalf("expected cancelled, got %s", BatchStatusCancelled)
    }
}

func TestBatchStructs(t *testing.T) {
    br := BatchRequest{
        CustomID: "req-1",
        Method:   "POST",
        URL:      "/v1/chat/completions",
        Body:     json.RawMessage(`{"model":"gpt-4"}`),
    }
    if br.CustomID != "req-1" {
        t.Fatalf("expected req-1, got %s", br.CustomID)
    }

    bresp := BatchResponse{
        StatusCode: 200,
        Body:       json.RawMessage(`{"id":"chat-1"}`),
    }
    if bresp.StatusCode != 200 {
        t.Fatalf("expected 200, got %d", bresp.StatusCode)
    }

    br2 := BatchResult{
        CustomID: "req-1",
        Response: &BatchResponse{StatusCode: 200},
        Error:    "",
    }
    if br2.CustomID != "req-1" {
        t.Fatalf("expected req-1, got %s", br2.CustomID)
    }

    now := time.Now()
    b := &Batch{
        ID:               "batch-1",
        Status:           BatchStatusPending,
        Requests:         []BatchRequest{br},
        Total:            1,
        Completed:        0,
        Failed:           0,
        CreatedAt:        now,
        Endpoint:         "/v1/chat/completions",
        CompletionWindow: "24h",
    }
    if b.ID != "batch-1" {
        t.Fatalf("expected batch-1, got %s", b.ID)
    }
    if b.Status != BatchStatusPending {
        t.Fatalf("expected pending, got %s", b.Status)
    }
}

func TestTeamStructs(t *testing.T) {
    tm := TeamMember{UserID: "user-1", Role: "admin"}
    if tm.Role != "admin" {
        t.Fatalf("expected admin, got %s", tm.Role)
    }

    now := time.Now()
    team := &Team{
        ID:              "team-1",
        Name:            "Test Team",
        OrgID:           "org-1",
        QuotaLimit:      100.0,
        QuotaUsed:       50.0,
        Members:         []TeamMember{tm},
        AllowedModels:   []string{"gpt-4"},
        CostAccumulated: 25.0,
        CreatedAt:       now,
        UpdatedAt:       now,
    }
    if team.ID != "team-1" {
        t.Fatalf("expected team-1, got %s", team.ID)
    }
    if len(team.Members) != 1 {
        t.Fatalf("expected 1 member, got %d", len(team.Members))
    }
}

func TestOrganizationStruct(t *testing.T) {
    org := &Organization{
        ID:        "org-1",
        Name:      "Test Org",
        CreatedAt: time.Now(),
        UpdatedAt: time.Now(),
    }
    if org.ID != "org-1" {
        t.Fatalf("expected org-1, got %s", org.ID)
    }
}

func TestUsageRecord(t *testing.T) {
    ur := UsageRecord{
        Timestamp:        time.Now(),
        KeyName:          "key-1",
        Backend:          "cloud",
        Model:            "gpt-4",
        PromptTokens:     100,
        CompletionTokens: 50,
        TotalTokens:      150,
        CostUSD:          0.01,
    }
    if ur.TotalTokens != 150 {
        t.Fatalf("expected 150, got %d", ur.TotalTokens)
    }
}

func TestCostSummary(t *testing.T) {
    cs := &CostSummary{
        TotalCostUSD:  100.0,
        ByKey:         map[string]float64{"key-1": 50.0},
        ByBackend:     map[string]float64{"cloud": 80.0},
        ByModel:       map[string]float64{"gpt-4": 60.0},
        TotalTokens:   50000,
        TotalRequests: 1000,
    }
    if cs.TotalCostUSD != 100.0 {
        t.Fatalf("expected 100.0, got %f", cs.TotalCostUSD)
    }
}

func TestRequestLog_JSONRoundtrip(t *testing.T) {
    log := &RequestLog{
        ID:         "log-1",
        RequestID:  "req-1",
        Timestamp:  time.Now(),
        Model:      "gpt-4",
        IsSuccess:  true,
        StatusCode: 200,
    }
    data, err := json.Marshal(log)
    if err != nil {
        t.Fatalf("marshal failed: %v", err)
    }
    var decoded RequestLog
    if err := json.Unmarshal(data, &decoded); err != nil {
        t.Fatalf("unmarshal failed: %v", err)
    }
    if decoded.ID != "log-1" {
        t.Fatalf("expected log-1, got %s", decoded.ID)
    }
}
