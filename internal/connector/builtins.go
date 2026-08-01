package connector

import (
    "context"
    "fmt"
    "log/slog"
    "net/http"
    "time"
)

type QuickBooksConnector struct {
    baseURL string
    client  *http.Client
}

func NewQuickBooksConnector() *QuickBooksConnector {
    return &QuickBooksConnector{
        baseURL: "https://quickbooks.api.intuit.com",
        client:  &http.Client{Timeout: 15 * time.Second},
    }
}

func (q *QuickBooksConnector) Meta() *ConnectorMeta {
    return &ConnectorMeta{
        ConnectorKey: "quickbooks",
        DisplayName:  "QuickBooks",
        Icon:         "icon-quickbooks",
        AuthType:     AuthTypeOAuth2,
        Description:  "QuickBooks Online accounting API",
        Actions: []ActionDef{
            {ActionKey: "query_overdue_invoice", DisplayName: "查询逾期发票", Permission: PermissionRead},
            {ActionKey: "list_customers", DisplayName: "列出客户", Permission: PermissionRead},
            {ActionKey: "create_invoice", DisplayName: "创建发票", Permission: PermissionWrite},
            {ActionKey: "get_company_info", DisplayName: "获取公司信息", Permission: PermissionRead},
        },
    }
}

func (q *QuickBooksConnector) ExecuteAction(ctx context.Context, conn *Connection, actionKey string, params map[string]interface{}, testMode bool) (*ActionResponse, error) {
    slog.Info("quickbooks action", "action", actionKey, "connection", conn.ID, "test", testMode)
    if testMode {
        return &ActionResponse{Success: true, Code: 0, Data: map[string]interface{}{"test": true, "action": actionKey}, Message: "test mode"}, nil
    }
    switch actionKey {
    case "query_overdue_invoice":
        return &ActionResponse{Success: true, Code: 0, Data: map[string]interface{}{"invoices": []interface{}{}}}, nil
    case "list_customers":
        return &ActionResponse{Success: true, Code: 0, Data: map[string]interface{}{"customers": []interface{}{}}}, nil
    case "create_invoice":
        customerName, _ := params["customerName"].(string)
        if customerName == "" {
            return ErrorWithCode(ErrValidation, "customerName is required"), nil
        }
        return &ActionResponse{Success: true, Code: 0, Data: map[string]interface{}{"invoiceId": "test-inv-001", "customerName": customerName}}, nil
    case "get_company_info":
        return &ActionResponse{Success: true, Code: 0, Data: map[string]interface{}{"companyName": "Test Company"}}, nil
    default:
        return ErrorWithCode(ErrNotFound, fmt.Sprintf("action %q not found", actionKey)), nil
    }
}

func (q *QuickBooksConnector) RefreshAuth(ctx context.Context, conn *Connection) error {
    slog.Info("quickbooks auth refresh", "connection", conn.ID)
    return nil
}

func (q *QuickBooksConnector) ValidateAuth(ctx context.Context, conn *Connection) error {
    slog.Info("quickbooks auth validate", "connection", conn.ID)
    return nil
}

type GoogleWorkspaceConnector struct {
    client *http.Client
}

func NewGoogleWorkspaceConnector() *GoogleWorkspaceConnector {
    return &GoogleWorkspaceConnector{
        client: &http.Client{Timeout: 15 * time.Second},
    }
}

func (g *GoogleWorkspaceConnector) Meta() *ConnectorMeta {
    return &ConnectorMeta{
        ConnectorKey: "google_workspace",
        DisplayName:  "Google Workspace",
        Icon:         "icon-google",
        AuthType:     AuthTypeOAuth2,
        Description:  "Google Workspace Admin & APIs",
        Actions: []ActionDef{
            {ActionKey: "list_users", DisplayName: "列出用户", Permission: PermissionRead},
            {ActionKey: "get_user", DisplayName: "获取用户详情", Permission: PermissionRead},
            {ActionKey: "list_calendar_events", DisplayName: "列出日历事件", Permission: PermissionRead},
            {ActionKey: "send_email", DisplayName: "发送邮件", Permission: PermissionWrite},
            {ActionKey: "read_drive_file", DisplayName: "读取Drive文件", Permission: PermissionRead},
        },
    }
}

func (g *GoogleWorkspaceConnector) ExecuteAction(ctx context.Context, conn *Connection, actionKey string, params map[string]interface{}, testMode bool) (*ActionResponse, error) {
    slog.Info("google_workspace action", "action", actionKey, "connection", conn.ID, "test", testMode)
    if testMode {
        return &ActionResponse{Success: true, Code: 0, Data: map[string]interface{}{"test": true, "action": actionKey}, Message: "test mode"}, nil
    }
    switch actionKey {
    case "list_users":
        return &ActionResponse{Success: true, Code: 0, Data: map[string]interface{}{"users": []interface{}{}}}, nil
    case "get_user":
        email, _ := params["email"].(string)
        if email == "" {
            return ErrorWithCode(ErrValidation, "email is required"), nil
        }
        return &ActionResponse{Success: true, Code: 0, Data: map[string]interface{}{"email": email, "status": "active"}}, nil
    case "list_calendar_events":
        return &ActionResponse{Success: true, Code: 0, Data: map[string]interface{}{"events": []interface{}{}}}, nil
    case "send_email":
        to, _ := params["to"].(string)
        if to == "" {
            return ErrorWithCode(ErrValidation, "to is required"), nil
        }
        return &ActionResponse{Success: true, Code: 0, Data: map[string]interface{}{"messageId": "msg-001", "to": to}}, nil
    case "read_drive_file":
        fileID, _ := params["fileId"].(string)
        if fileID == "" {
            return ErrorWithCode(ErrValidation, "fileId is required"), nil
        }
        return &ActionResponse{Success: true, Code: 0, Data: map[string]interface{}{"fileId": fileID, "name": "document.pdf"}}, nil
    default:
        return ErrorWithCode(ErrNotFound, fmt.Sprintf("action %q not found", actionKey)), nil
    }
}

func (g *GoogleWorkspaceConnector) RefreshAuth(ctx context.Context, conn *Connection) error {
    slog.Info("google_workspace auth refresh", "connection", conn.ID)
    return nil
}

func (g *GoogleWorkspaceConnector) ValidateAuth(ctx context.Context, conn *Connection) error {
    slog.Info("google_workspace auth validate", "connection", conn.ID)
    return nil
}

type HubSpotConnector struct {
    client *http.Client
}

func NewHubSpotConnector() *HubSpotConnector {
    return &HubSpotConnector{
        client: &http.Client{Timeout: 15 * time.Second},
    }
}

func (h *HubSpotConnector) Meta() *ConnectorMeta {
    return &ConnectorMeta{
        ConnectorKey: "hubspot",
        DisplayName:  "HubSpot",
        Icon:         "icon-hubspot",
        AuthType:     AuthTypeOAuth2,
        Description:  "HubSpot CRM API",
        Actions: []ActionDef{
            {ActionKey: "list_contacts", DisplayName: "列出联系人", Permission: PermissionRead},
            {ActionKey: "get_contact", DisplayName: "获取联系人详情", Permission: PermissionRead},
            {ActionKey: "create_contact", DisplayName: "创建联系人", Permission: PermissionWrite},
            {ActionKey: "list_deals", DisplayName: "列出交易", Permission: PermissionRead},
            {ActionKey: "update_deal", DisplayName: "更新交易", Permission: PermissionWrite},
        },
    }
}

func (h *HubSpotConnector) ExecuteAction(ctx context.Context, conn *Connection, actionKey string, params map[string]interface{}, testMode bool) (*ActionResponse, error) {
    slog.Info("hubspot action", "action", actionKey, "connection", conn.ID, "test", testMode)
    if testMode {
        return &ActionResponse{Success: true, Code: 0, Data: map[string]interface{}{"test": true, "action": actionKey}, Message: "test mode"}, nil
    }
    switch actionKey {
    case "list_contacts":
        return &ActionResponse{Success: true, Code: 0, Data: map[string]interface{}{"contacts": []interface{}{}}}, nil
    case "get_contact":
        contactID, _ := params["contactId"].(string)
        if contactID == "" {
            return ErrorWithCode(ErrValidation, "contactId is required"), nil
        }
        return &ActionResponse{Success: true, Code: 0, Data: map[string]interface{}{"contactId": contactID, "email": "test@example.com"}}, nil
    case "create_contact":
        email, _ := params["email"].(string)
        if email == "" {
            return ErrorWithCode(ErrValidation, "email is required"), nil
        }
        return &ActionResponse{Success: true, Code: 0, Data: map[string]interface{}{"contactId": "new-001", "email": email}}, nil
    case "list_deals":
        return &ActionResponse{Success: true, Code: 0, Data: map[string]interface{}{"deals": []interface{}{}}}, nil
    case "update_deal":
        dealID, _ := params["dealId"].(string)
        if dealID == "" {
            return ErrorWithCode(ErrValidation, "dealId is required"), nil
        }
        return &ActionResponse{Success: true, Code: 0, Data: map[string]interface{}{"dealId": dealID, "updated": true}}, nil
    default:
        return ErrorWithCode(ErrNotFound, fmt.Sprintf("action %q not found", actionKey)), nil
    }
}

func (h *HubSpotConnector) RefreshAuth(ctx context.Context, conn *Connection) error {
    slog.Info("hubspot auth refresh", "connection", conn.ID)
    return nil
}

func (h *HubSpotConnector) ValidateAuth(ctx context.Context, conn *Connection) error {
    slog.Info("hubspot auth validate", "connection", conn.ID)
    return nil
}

func RegisterBuiltins(r *Registry) {
    r.Register(NewQuickBooksConnector())
    r.Register(NewGoogleWorkspaceConnector())
    r.Register(NewHubSpotConnector())
    slog.Info("builtin connectors registered", "count", 3)
}
