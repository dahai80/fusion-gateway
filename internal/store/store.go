package store

import (
    "encoding/json"
    "time"
)

type RequestLog struct {
    ID           string    `json:"id"`
    RequestID    string    `json:"request_id"`
    Timestamp    time.Time `json:"timestamp"`

    APIKeyName   string `json:"api_key_name"`
    Model        string `json:"model"`
    RequestType  string `json:"request_type"`
    IsStream     bool   `json:"is_stream"`

    ChannelName  string `json:"channel_name"`
    ChannelType  string `json:"channel_type"`
    RouteReason  string `json:"route_reason"`

    ProjectID    string `json:"project_id,omitempty"`
    ChatID       string `json:"chat_id,omitempty"`
    SpaceID      string `json:"space_id,omitempty"`

    InputTokens  int `json:"input_tokens"`
    OutputTokens int `json:"output_tokens"`
    TotalTokens  int `json:"total_tokens"`

    Cost         float64 `json:"cost"`
    CostUSD      float64 `json:"cost_usd"`
    LocalSavings float64 `json:"local_savings"`

    Latency      float64 `json:"latency_ms"`
    TTFT         float64 `json:"ttft_ms"`

    StatusCode   int    `json:"status_code"`
    IsSuccess    bool   `json:"is_success"`
    ErrorMessage string `json:"error_message,omitempty"`
}

type APIKeyEntry struct {
    Name            string            `json:"name"`
    KeyPrefix       string            `json:"key_prefix"`
    KeyHash         string            `json:"key_hash,omitempty"`
    Status          string            `json:"status"`
    QuotaLimit      float64           `json:"quota_limit"`
    QuotaUsed       float64           `json:"quota_used"`
    QuotaRemaining  float64           `json:"quota_remaining"`
    AllowedModels   []string          `json:"allowed_models"`
    RPM             int               `json:"rpm"`
    TPM             int               `json:"tpm"`
    ExpiresAt       *time.Time        `json:"expires_at"`
    BudgetLimit     float64           `json:"budget_limit"`
    AllowedBackends []string          `json:"allowed_backends"`
    ModelModules    []string          `json:"model_modules"`
    Metadata        map[string]string `json:"metadata"`
    CreatedAt       time.Time         `json:"created_at"`
    UpdatedAt       time.Time         `json:"updated_at"`
}

type ChannelEntry struct {
    Name      string    `json:"name"`
    Type      string    `json:"type"`
    Provider  string    `json:"provider"`
    BaseURL   string    `json:"base_url"`
    Status    string    `json:"status"`
    Priority  int       `json:"priority"`
    Weight    int       `json:"weight"`
    Models    []string  `json:"models"`
    Enabled   bool      `json:"enabled"`
    CreatedAt time.Time `json:"created_at"`
    UpdatedAt time.Time `json:"updated_at"`
}

type LogFilter struct {
    StartTime *time.Time `json:"start_time,omitempty"`
    EndTime   *time.Time `json:"end_time,omitempty"`
    KeyName   string     `json:"key_name,omitempty"`
    Model     string     `json:"model,omitempty"`
    Channel   string     `json:"channel,omitempty"`
    Status    string     `json:"status,omitempty"`
    MinTokens int        `json:"min_tokens,omitempty"`
    MinCost   float64    `json:"min_cost,omitempty"`
    Page      int        `json:"page"`
    PageSize  int        `json:"page_size"`
}

type LogFilters struct {
    Models   []string `json:"models"`
    Channels []string `json:"channels"`
}

type TokenStat struct {
    Time         string `json:"time"`
    InputTokens  int64  `json:"input_tokens"`
    OutputTokens int64  `json:"output_tokens"`
    TotalTokens  int64  `json:"total_tokens"`
}

type CostStat struct {
    Time    string  `json:"time"`
    Cost    float64 `json:"cost"`
    CostUSD float64 `json:"cost_usd"`
    Savings float64 `json:"savings"`
}

type ModelStat struct {
    Model        string  `json:"model"`
    RequestCount int64   `json:"request_count"`
    InputTokens  int64   `json:"input_tokens"`
    OutputTokens int64   `json:"output_tokens"`
    Cost         float64 `json:"cost"`
}

type LatencyStat struct {
    Channel string  `json:"channel"`
    P50     float64 `json:"p50_ms"`
    P90     float64 `json:"p90_ms"`
    P99     float64 `json:"p99_ms"`
}

type ErrorStat struct {
    Channel   string `json:"channel"`
    Model     string `json:"model"`
    ErrorType string `json:"error_type"`
    Count     int64  `json:"count"`
}

type DashboardOverview struct {
    TotalRequests      int64              `json:"total_requests"`
    TotalTokens        int64              `json:"total_tokens"`
    TotalCost          float64            `json:"total_cost"`
    LocalHitRate       float64            `json:"local_hit_rate"`
    RequestsTrend      []TokenStat        `json:"requests_trend"`
    TokensTrend        []TokenStat        `json:"tokens_trend"`
    CostTrend          []CostTrendItem    `json:"cost_trend"`
    ModelDistribution  []ModelDistItem    `json:"model_distribution"`
    RouteDistribution  map[string]float64 `json:"route_distribution"`
}

type CostTrendItem struct {
    Date string  `json:"date"`
    Cost float64 `json:"cost"`
}

type ModelDistItem struct {
    Model string  `json:"model"`
    Count float64 `json:"count"`
}

type KeyProfitStat struct {
    KeyName      string  `json:"key_name"`
    TotalInput   int64   `json:"total_input"`
    TotalOutput  int64   `json:"total_output"`
    Ratio        float64 `json:"ratio"`
    TotalCost    float64 `json:"total_cost"`
    RequestCount int     `json:"request_count"`
}

// A3 fix: domain types migrated from sub-packages for shared Store interface

type TeamMember struct {
    UserID string `json:"user_id"`
    Role   string `json:"role"`
}

type Team struct {
    ID              string       `json:"id"`
    Name            string       `json:"name"`
    OrgID           string       `json:"org_id,omitempty"`
    QuotaLimit      float64      `json:"quota_limit"`
    QuotaUsed       float64      `json:"quota_used"`
    Members         []TeamMember `json:"members"`
    AllowedModels   []string     `json:"allowed_models,omitempty"`
    CostAccumulated float64      `json:"cost_accumulated"`
    CreatedAt       time.Time    `json:"created_at"`
    UpdatedAt       time.Time    `json:"updated_at"`
}

type Organization struct {
    ID        string    `json:"id"`
    Name      string    `json:"name"`
    CreatedAt time.Time `json:"created_at"`
    UpdatedAt time.Time `json:"updated_at"`
}

type BatchStatus string

const (
    BatchStatusPending   BatchStatus = "pending"
    BatchStatusRunning   BatchStatus = "running"
    BatchStatusCompleted BatchStatus = "completed"
    BatchStatusFailed    BatchStatus = "failed"
    BatchStatusCancelled BatchStatus = "cancelled"
)

type BatchRequest struct {
    CustomID string          `json:"custom_id"`
    Method   string          `json:"method"`
    URL      string          `json:"url"`
    Body     json.RawMessage `json:"body"`
}

type BatchResponse struct {
    StatusCode int             `json:"status_code"`
    Body       json.RawMessage `json:"body"`
}

type BatchResult struct {
    CustomID string         `json:"custom_id"`
    Response *BatchResponse `json:"response,omitempty"`
    Error    string         `json:"error,omitempty"`
}

type Batch struct {
    ID               string         `json:"id"`
    Status           BatchStatus    `json:"status"`
    Requests         []BatchRequest `json:"requests"`
    Results          []BatchResult  `json:"results,omitempty"`
    Total            int            `json:"total"`
    Completed        int            `json:"completed"`
    Failed           int            `json:"failed"`
    CreatedAt        time.Time      `json:"created_at"`
    CompletedAt      *time.Time     `json:"completed_at,omitempty"`
    Endpoint         string         `json:"endpoint,omitempty"`
    CompletionWindow string         `json:"completion_window,omitempty"`
}

type UsageRecord struct {
    Timestamp        time.Time `json:"timestamp"`
    KeyName          string    `json:"key_name"`
    Backend          string    `json:"backend"`
    Model            string    `json:"model"`
    PromptTokens     int       `json:"prompt_tokens"`
    CompletionTokens int       `json:"completion_tokens"`
    TotalTokens      int       `json:"total_tokens"`
    CostUSD          float64   `json:"cost_usd"`
}

type CostSummary struct {
    TotalCostUSD  float64           `json:"total_cost_usd"`
    ByKey         map[string]float64 `json:"by_key"`
    ByBackend     map[string]float64 `json:"by_backend"`
    ByModel       map[string]float64 `json:"by_model"`
    TotalTokens   int               `json:"total_tokens"`
    TotalRequests int               `json:"total_requests"`
}

type Store interface {
    AppendLog(log *RequestLog) error
    QueryLogs(filter LogFilter) ([]*RequestLog, int, error)
    GetLog(id string) (*RequestLog, error)
    ExportLogs(filter LogFilter, format string) ([]byte, error)
    DistinctLogFilters() (*LogFilters, error)

    ListKeys() ([]*APIKeyEntry, error)
    GetKey(name string) (*APIKeyEntry, error)
    GetKeyByHash(hash string) (*APIKeyEntry, error)
    CreateKey(key *APIKeyEntry) error
    UpdateKey(key *APIKeyEntry) error
    DeleteKey(name string) error

    ListChannels() ([]*ChannelEntry, error)
    GetChannel(name string) (*ChannelEntry, error)
    CreateChannel(ch *ChannelEntry) error
    UpdateChannel(ch *ChannelEntry) error
    DeleteChannel(name string) error

    GetTokenStats(from, to time.Time, groupBy string) ([]*TokenStat, error)
    GetCostStats(from, to time.Time, groupBy string) ([]*CostStat, error)
    GetModelStats(from, to time.Time) ([]*ModelStat, error)
    GetLatencyStats(from, to time.Time) ([]*LatencyStat, error)
    GetErrorStats(from, to time.Time) ([]*ErrorStat, error)
    GetDashboardOverview() (*DashboardOverview, error)
    GetKeyProfitStats(from, to time.Time) ([]*KeyProfitStat, error)

    CheckQuota(keyName string) (used, limit float64, exceeded bool, err error)
    DeductQuota(keyName string, amount float64) error

    // A3 fix: teams/orgs/batch/cost methods for multi-instance persistence

    // Teams
    CreateTeam(team *Team) error
    GetTeam(id string) (*Team, error)
    ListTeams() ([]*Team, error)
    UpdateTeam(team *Team) error
    DeleteTeam(id string) error
    BindKeyToTeam(apiKey, teamID string) error
    GetTeamByKey(apiKey string) (*Team, error)
    AddTeamCost(teamID string, cost float64) error
    CheckTeamQuota(teamID string) (limit, used float64, ok bool, err error)
    AddTeamMember(teamID, userID, role string) error
    RemoveTeamMember(teamID, userID string) error

    // Organizations
    CreateOrg(org *Organization) error
    GetOrg(id string) (*Organization, error)
    ListOrgs() ([]*Organization, error)
    DeleteOrg(id string) error

    // Batch
    CreateBatch(requests []BatchRequest, endpoint, window string) (*Batch, error)
    GetBatch(id string) (*Batch, error)
    ListBatches() ([]*Batch, error)
    CancelBatch(id string) (*Batch, error)
    UpdateBatch(batch *Batch) error

    // Cost
    RecordUsage(keyName, backend, model string, promptTokens, completionTokens int, costUSD float64) error
    GetCostSummary(keyName string) (*CostSummary, error)
    GetCostSummaryAll() (*CostSummary, error)
}
