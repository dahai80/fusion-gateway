package store

import (
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
    TotalRequests     int64              `json:"total_requests"`
    TotalTokens       int64              `json:"total_tokens"`
    TotalCost         float64            `json:"total_cost"`
    LocalHitRate      float64            `json:"local_hit_rate"`
    RequestsTrend     []TokenStat        `json:"requests_trend"`
    TokensTrend       []TokenStat        `json:"tokens_trend"`
    RouteDistribution map[string]float64 `json:"route_distribution"`
}

type Store interface {
    AppendLog(log *RequestLog) error
    QueryLogs(filter LogFilter) ([]*RequestLog, int, error)
    GetLog(id string) (*RequestLog, error)
    ExportLogs(filter LogFilter, format string) ([]byte, error)

    ListKeys() ([]*APIKeyEntry, error)
    GetKey(name string) (*APIKeyEntry, error)
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

    CheckQuota(keyName string) (used, limit float64, exceeded bool, err error)
    DeductQuota(keyName string, amount float64) error
}
