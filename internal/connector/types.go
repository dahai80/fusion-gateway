package connector

import (
    "context"
    "time"
)

type AuthType string

const (
    AuthTypeOAuth2     AuthType = "oauth2"
    AuthTypeStaticKey  AuthType = "static_api_key"
    AuthTypeBasicAuth  AuthType = "basic_auth"
)

type ActionPermission string

const (
    PermissionRead  ActionPermission = "read"
    PermissionWrite ActionPermission = "write"
)

type ActionDef struct {
    ActionKey    string           `json:"actionKey"`
    DisplayName  string           `json:"displayName"`
    Permission   ActionPermission `json:"permission"`
    InputSchema  interface{}      `json:"inputSchema,omitempty"`
    OutputSchema interface{}      `json:"outputSchema,omitempty"`
}

type ConnectorMeta struct {
    ConnectorKey string      `json:"connectorKey"`
    DisplayName  string      `json:"displayName"`
    Icon         string      `json:"icon,omitempty"`
    AuthType     AuthType    `json:"authType"`
    Description  string      `json:"description,omitempty"`
    Actions      []ActionDef `json:"actions"`
}

type Connection struct {
    ID           string     `json:"id"`
    ConnectorKey string     `json:"connectorKey"`
    AuthType     AuthType   `json:"authType"`
    CreatedAt    time.Time  `json:"createdAt"`
    UpdatedAt    time.Time  `json:"updatedAt"`
    ExpiresAt    *time.Time `json:"expiresAt,omitempty"`
    Status       string     `json:"status"`
}

type ActionRequest struct {
    ConnectorKey string                 `json:"connectorKey"`
    ActionKey    string                 `json:"actionKey"`
    ConnectionID string                 `json:"connectionId"`
    Params       map[string]interface{} `json:"params"`
    TestMode     bool                   `json:"testMode,omitempty"`
}

type ActionResponse struct {
    Success bool        `json:"success"`
    Code    int         `json:"code"`
    Data    interface{} `json:"data,omitempty"`
    Message string      `json:"message,omitempty"`
}

type Connector interface {
    Meta() *ConnectorMeta
    ExecuteAction(ctx context.Context, conn *Connection, actionKey string, params map[string]interface{}, testMode bool) (*ActionResponse, error)
    RefreshAuth(ctx context.Context, conn *Connection) error
    ValidateAuth(ctx context.Context, conn *Connection) error
}

type AuditEntry struct {
    Timestamp    time.Time `json:"timestamp"`
    ConnectionID string    `json:"connectionId"`
    ConnectorKey string    `json:"connectorKey"`
    ActionKey    string    `json:"actionKey"`
    Permission   string    `json:"permission"`
    Success      bool      `json:"success"`
    ErrorCode    int       `json:"errorCode,omitempty"`
    InputSummary string    `json:"inputSummary,omitempty"`
    DurationMS   int64     `json:"durationMs"`
}

const (
    ErrAuthExpired     = 1001
    ErrRateLimited     = 1002
    ErrPermissionDenied = 1003
    ErrNotFound        = 1004
    ErrTimeout         = 1005
    ErrValidation      = 2001
)

func ErrorWithCode(code int, msg string) *ActionResponse {
    return &ActionResponse{
        Success: false,
        Code:    code,
        Message: msg,
    }
}
