package connector

import (
    "bytes"
    "context"
    "encoding/base64"
    "encoding/json"
    "fmt"
    "io"
    "log/slog"
    "net/http"
    "net/url"
    "strings"
    "time"
)

type QuickBooksConnector struct {
    baseURL  string
    client   *http.Client
    registry *Registry
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

    realmID := conn.AuthConfig["realmId"]
    if realmID == "" {
        return ErrorWithCode(ErrValidation, "realmId is required in connection authConfig"), nil
    }

    token, err := getDecryptedToken(q.registry, conn)
    if err != nil {
        return ErrorWithCode(ErrAuth, fmt.Sprintf("decrypt token failed: %v", err)), nil
    }

    baseURL := fmt.Sprintf("%s/v3/company/%s", q.baseURL, realmID)

    switch actionKey {
    case "query_overdue_invoice":
        query := url.QueryEscape("SELECT * FROM Invoice WHERE Balance > '0'")
        return q.doGet(ctx, token, fmt.Sprintf("%s/query?query=%s", baseURL, query))
    case "list_customers":
        query := url.QueryEscape("SELECT * FROM Customer")
        return q.doGet(ctx, token, fmt.Sprintf("%s/query?query=%s", baseURL, query))
    case "create_invoice":
        customerName, _ := params["customerName"].(string)
        if customerName == "" {
            return ErrorWithCode(ErrValidation, "customerName is required"), nil
        }
        body, _ := json.Marshal(map[string]interface{}{"CustomerRef": map[string]string{"name": customerName}})
        return q.doPost(ctx, token, fmt.Sprintf("%s/invoice", baseURL), body)
    case "get_company_info":
        return q.doGet(ctx, token, fmt.Sprintf("%s/companyinfo/%s", baseURL, realmID))
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

func (q *QuickBooksConnector) doGet(ctx context.Context, token, apiURL string) (*ActionResponse, error) {
    req, _ := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
    req.Header.Set("Authorization", "Bearer "+token)
    resp, err := q.client.Do(req)
    if err != nil {
        return ErrorWithCode(ErrExternal, err.Error()), nil
    }
    defer resp.Body.Close()
    return parseAPIResponse(resp)
}

func (q *QuickBooksConnector) doPost(ctx context.Context, token, apiURL string, body []byte) (*ActionResponse, error) {
    req, _ := http.NewRequestWithContext(ctx, "POST", apiURL, bytes.NewReader(body))
    req.Header.Set("Authorization", "Bearer "+token)
    req.Header.Set("Content-Type", "application/json")
    resp, err := q.client.Do(req)
    if err != nil {
        return ErrorWithCode(ErrExternal, err.Error()), nil
    }
    defer resp.Body.Close()
    return parseAPIResponse(resp)
}

type GoogleWorkspaceConnector struct {
    client   *http.Client
    oauth2   *OAuth2Provider
    registry *Registry
}

func NewGoogleWorkspaceConnector() *GoogleWorkspaceConnector {
    return &GoogleWorkspaceConnector{
        client: &http.Client{Timeout: 15 * time.Second},
    }
}

func (g *GoogleWorkspaceConnector) SetOAuth2Provider(o *OAuth2Provider) {
    g.oauth2 = o
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

    token, err := getDecryptedToken(g.registry, conn)
    if err != nil {
        return ErrorWithCode(ErrAuth, fmt.Sprintf("decrypt token failed: %v", err)), nil
    }

    switch actionKey {
    case "list_users":
        domain := url.QueryEscape(conn.AuthConfig["domain"])
        return g.doRequest(ctx, token, conn, "GET", "https://admin.googleapis.com/admin/directory/v1/users?domain="+domain, nil)
    case "get_user":
        email, _ := params["email"].(string)
        if email == "" {
            return ErrorWithCode(ErrValidation, "email is required"), nil
        }
        return g.doRequest(ctx, token, conn, "GET", "https://admin.googleapis.com/admin/directory/v1/users/"+url.QueryEscape(email), nil)
    case "list_calendar_events":
        calendarID := "primary"
        if cid, ok := params["calendarId"].(string); ok && cid != "" {
            calendarID = cid
        }
        return g.doRequest(ctx, token, conn, "GET", "https://www.googleapis.com/calendar/v3/calendars/"+url.QueryEscape(calendarID)+"/events", nil)
    case "send_email":
        to, _ := params["to"].(string)
        if to == "" {
            return ErrorWithCode(ErrValidation, "to is required"), nil
        }
        subject, _ := params["subject"].(string)
        bodyText, _ := params["body"].(string)
        emailPayload := buildGmailRaw(to, subject, bodyText)
        payload, _ := json.Marshal(map[string]interface{}{"raw": emailPayload})
        return g.doRequest(ctx, token, conn, "POST", "https://gmail.googleapis.com/gmail/v1/users/me/messages/send", payload)
    case "read_drive_file":
        fileID, _ := params["fileId"].(string)
        if fileID == "" {
            return ErrorWithCode(ErrValidation, "fileId is required"), nil
        }
        return g.doRequest(ctx, token, conn, "GET", "https://www.googleapis.com/drive/v3/files/"+url.QueryEscape(fileID)+"?fields=id,name,mimeType,size", nil)
    default:
        return ErrorWithCode(ErrNotFound, fmt.Sprintf("action %q not found", actionKey)), nil
    }
}

func (g *GoogleWorkspaceConnector) doRequest(ctx context.Context, token string, conn *Connection, method, apiURL string, body []byte) (*ActionResponse, error) {
    resp, err := g.doHTTP(ctx, token, method, apiURL, body)
    if err != nil {
        return ErrorWithCode(ErrExternal, err.Error()), nil
    }
    defer resp.Body.Close()

    if resp.StatusCode == http.StatusUnauthorized && g.oauth2 != nil {
        slog.Info("google_workspace got 401, attempting token refresh", "connection", conn.ID)
        if refreshErr := g.oauth2.RefreshIfNeeded(ctx, conn); refreshErr != nil {
            slog.Error("google_workspace token refresh failed", "error", refreshErr)
            return ErrorWithCode(ErrAuth, "token expired and refresh failed"), nil
        }
        newToken, decErr := getDecryptedToken(g.registry, conn)
        if decErr != nil {
            return ErrorWithCode(ErrAuth, "decrypt refreshed token failed"), nil
        }
        retryResp, retryErr := g.doHTTP(ctx, newToken, method, apiURL, body)
        if retryErr != nil {
            return ErrorWithCode(ErrExternal, retryErr.Error()), nil
        }
        defer retryResp.Body.Close()
        return parseAPIResponse(retryResp)
    }

    return parseAPIResponse(resp)
}

func (g *GoogleWorkspaceConnector) doHTTP(ctx context.Context, token, method, apiURL string, body []byte) (*http.Response, error) {
    var req *http.Request
    if body != nil {
        req, _ = http.NewRequestWithContext(ctx, method, apiURL, bytes.NewReader(body))
    } else {
        req, _ = http.NewRequestWithContext(ctx, method, apiURL, nil)
    }
    req.Header.Set("Authorization", "Bearer "+token)
    req.Header.Set("Content-Type", "application/json")
    return g.client.Do(req)
}

func (g *GoogleWorkspaceConnector) RefreshAuth(ctx context.Context, conn *Connection) error {
    if g.oauth2 != nil {
        return g.oauth2.RefreshIfNeeded(ctx, conn)
    }
    return nil
}

func (g *GoogleWorkspaceConnector) ValidateAuth(ctx context.Context, conn *Connection) error {
    slog.Info("google_workspace auth validate", "connection", conn.ID)
    return nil
}

type HubSpotConnector struct {
    client   *http.Client
    registry *Registry
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

    token, err := getDecryptedToken(h.registry, conn)
    if err != nil {
        return ErrorWithCode(ErrAuth, fmt.Sprintf("decrypt token failed: %v", err)), nil
    }

    switch actionKey {
    case "list_contacts":
        return h.doGet(ctx, token, "https://api.hubapi.com/crm/v3/objects/contacts")
    case "get_contact":
        contactID, _ := params["contactId"].(string)
        if contactID == "" {
            return ErrorWithCode(ErrValidation, "contactId is required"), nil
        }
        return h.doGet(ctx, token, fmt.Sprintf("https://api.hubapi.com/crm/v3/objects/contacts/%s", url.PathEscape(contactID)))
    case "create_contact":
        email, _ := params["email"].(string)
        if email == "" {
            return ErrorWithCode(ErrValidation, "email is required"), nil
        }
        payload, _ := json.Marshal(map[string]interface{}{
            "properties": map[string]string{"email": email},
        })
        return h.doPost(ctx, token, "https://api.hubapi.com/crm/v3/objects/contacts", payload)
    case "list_deals":
        return h.doGet(ctx, token, "https://api.hubapi.com/crm/v3/objects/deals")
    case "update_deal":
        dealID, _ := params["dealId"].(string)
        if dealID == "" {
            return ErrorWithCode(ErrValidation, "dealId is required"), nil
        }
        props, _ := params["properties"].(map[string]interface{})
        if props == nil {
            props = map[string]interface{}{}
        }
        payload, _ := json.Marshal(map[string]interface{}{"properties": props})
        return h.doPatch(ctx, token, fmt.Sprintf("https://api.hubapi.com/crm/v3/objects/deals/%s", url.PathEscape(dealID)), payload)
    default:
        return ErrorWithCode(ErrNotFound, fmt.Sprintf("action %q not found", actionKey)), nil
    }
}

func (h *HubSpotConnector) doGet(ctx context.Context, token, apiURL string) (*ActionResponse, error) {
    req, _ := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
    req.Header.Set("Authorization", "Bearer "+token)
    resp, err := h.client.Do(req)
    if err != nil {
        return ErrorWithCode(ErrExternal, err.Error()), nil
    }
    defer resp.Body.Close()
    return parseAPIResponse(resp)
}

func (h *HubSpotConnector) doPost(ctx context.Context, token, apiURL string, body []byte) (*ActionResponse, error) {
    req, _ := http.NewRequestWithContext(ctx, "POST", apiURL, bytes.NewReader(body))
    req.Header.Set("Authorization", "Bearer "+token)
    req.Header.Set("Content-Type", "application/json")
    resp, err := h.client.Do(req)
    if err != nil {
        return ErrorWithCode(ErrExternal, err.Error()), nil
    }
    defer resp.Body.Close()
    return parseAPIResponse(resp)
}

func (h *HubSpotConnector) doPatch(ctx context.Context, token, apiURL string, body []byte) (*ActionResponse, error) {
    req, _ := http.NewRequestWithContext(ctx, "PATCH", apiURL, bytes.NewReader(body))
    req.Header.Set("Authorization", "Bearer "+token)
    req.Header.Set("Content-Type", "application/json")
    resp, err := h.client.Do(req)
    if err != nil {
        return ErrorWithCode(ErrExternal, err.Error()), nil
    }
    defer resp.Body.Close()
    return parseAPIResponse(resp)
}

func (h *HubSpotConnector) RefreshAuth(ctx context.Context, conn *Connection) error {
    slog.Info("hubspot auth refresh", "connection", conn.ID)
    return nil
}

func (h *HubSpotConnector) ValidateAuth(ctx context.Context, conn *Connection) error {
    slog.Info("hubspot auth validate", "connection", conn.ID)
    return nil
}

func getDecryptedToken(registry *Registry, conn *Connection) (string, error) {
    if conn.EncryptedAccessToken == "" {
        return "", fmt.Errorf("no access token")
    }
    if registry == nil {
        return conn.EncryptedAccessToken, nil
    }
    return registry.DecryptToken(conn.EncryptedAccessToken)
}

func parseAPIResponse(resp *http.Response) (*ActionResponse, error) {
    const maxResponseSize = 10 << 20 // 10 MiB cap on external API response
    body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
    if err != nil {
        return ErrorWithCode(ErrExternal, fmt.Sprintf("read response body: %v", err)), nil
    }
    var data map[string]interface{}
    if err := json.Unmarshal(body, &data); err != nil {
        data = map[string]interface{}{"raw": string(body)}
    }
    success := resp.StatusCode >= 200 && resp.StatusCode < 300
    code := resp.StatusCode
    msg := ""
    if !success {
        msg = fmt.Sprintf("API returned %d", resp.StatusCode)
    }
    return &ActionResponse{Success: success, Code: code, Data: data, Message: msg}, nil
}

func buildGmailRaw(to, subject, body string) string {
    // Sanitize CRLF to prevent email header injection
    safeTo := strings.NewReplacer("\r", "", "\n", "").Replace(to)
    safeSubject := strings.NewReplacer("\r", "", "\n", "").Replace(subject)
    msg := fmt.Sprintf("To: %s\r\nSubject: %s\r\n\r\n%s", safeTo, safeSubject, body)
    return base64.URLEncoding.EncodeToString([]byte(msg))
}

func RegisterBuiltins(r *Registry) {
    qb := NewQuickBooksConnector()
    qb.registry = r
    r.Register(qb)

    g := NewGoogleWorkspaceConnector()
    g.SetOAuth2Provider(r.OAuth2())
    g.registry = r
    r.Register(g)

    hs := NewHubSpotConnector()
    hs.registry = r
    r.Register(hs)

    slog.Info("builtin connectors registered", "count", 3)
}
