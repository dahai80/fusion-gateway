package connector

import (
    "context"
    "net/http"
    "net/http/httptest"
    "testing"
)

func TestQuickBooksConnector_Meta(t *testing.T) {
    q := NewQuickBooksConnector()
    meta := q.Meta()
    if meta.ConnectorKey != "quickbooks" {
        t.Errorf("ConnectorKey = %q, want %q", meta.ConnectorKey, "quickbooks")
    }
    if len(meta.Actions) != 4 {
        t.Fatalf("Actions count = %d, want 4", len(meta.Actions))
    }
}

func TestQuickBooksConnector_TestMode(t *testing.T) {
    q := NewQuickBooksConnector()
    conn := &Connection{ID: "c1", ConnectorKey: "quickbooks"}
    actions := []string{"query_overdue_invoice", "list_customers", "create_invoice", "get_company_info"}
    for _, act := range actions {
        resp, err := q.ExecuteAction(context.Background(), conn, act, nil, true)
        if err != nil {
            t.Errorf("action %q test mode error: %v", act, err)
        }
        if !resp.Success {
            t.Errorf("action %q test mode: success=%v code=%d", act, resp.Success, resp.Code)
        }
    }
}

func TestQuickBooksConnector_NoToken(t *testing.T) {
    q := NewQuickBooksConnector()
    // With realmId but no token → ErrAuth
    conn := &Connection{ID: "c1", ConnectorKey: "quickbooks", AuthConfig: map[string]string{"realmId": "123"}}
    resp, err := q.ExecuteAction(context.Background(), conn, "query_overdue_invoice", nil, false)
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    if resp.Success {
        t.Error("should fail without access token")
    }
    if resp.Code != ErrAuth {
        t.Errorf("Code = %d, want %d", resp.Code, ErrAuth)
    }
}

func TestQuickBooksConnector_NoRealmID(t *testing.T) {
    q := NewQuickBooksConnector()
    conn := &Connection{ID: "c1", ConnectorKey: "quickbooks", EncryptedAccessToken: "test-token"}
    resp, err := q.ExecuteAction(context.Background(), conn, "query_overdue_invoice", nil, false)
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    if resp.Code != ErrValidation {
        t.Errorf("Code = %d, want %d", resp.Code, ErrValidation)
    }
}

func TestQuickBooksConnector_CreateInvoice_MissingName(t *testing.T) {
    q := NewQuickBooksConnector()
    conn := &Connection{ID: "c1", ConnectorKey: "quickbooks", EncryptedAccessToken: "token", AuthConfig: map[string]string{"realmId": "123"}}
    resp, err := q.ExecuteAction(context.Background(), conn, "create_invoice", nil, false)
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    if resp.Code != ErrValidation {
        t.Errorf("Code = %d, want %d", resp.Code, ErrValidation)
    }
}

func TestQuickBooksConnector_UnknownAction(t *testing.T) {
    q := NewQuickBooksConnector()
    conn := &Connection{ID: "c1", ConnectorKey: "quickbooks", EncryptedAccessToken: "token", AuthConfig: map[string]string{"realmId": "123"}}
    resp, _ := q.ExecuteAction(context.Background(), conn, "bogus_action", nil, false)
    if resp.Code != ErrNotFound {
        t.Errorf("Code = %d, want %d", resp.Code, ErrNotFound)
    }
}

func TestQuickBooksConnector_RefreshValidateAuth(t *testing.T) {
    q := NewQuickBooksConnector()
    conn := &Connection{ID: "c1", ConnectorKey: "quickbooks"}
    if err := q.RefreshAuth(context.Background(), conn); err != nil {
        t.Errorf("RefreshAuth error: %v", err)
    }
    if err := q.ValidateAuth(context.Background(), conn); err != nil {
        t.Errorf("ValidateAuth error: %v", err)
    }
}

func TestGoogleWorkspaceConnector_Meta(t *testing.T) {
    g := NewGoogleWorkspaceConnector()
    meta := g.Meta()
    if meta.ConnectorKey != "google_workspace" {
        t.Errorf("ConnectorKey = %q, want %q", meta.ConnectorKey, "google_workspace")
    }
    if len(meta.Actions) != 5 {
        t.Fatalf("Actions count = %d, want 5", len(meta.Actions))
    }
}

func TestGoogleWorkspaceConnector_TestMode(t *testing.T) {
    g := NewGoogleWorkspaceConnector()
    conn := &Connection{ID: "c1", ConnectorKey: "google_workspace"}
    for _, act := range []string{"list_users", "get_user", "list_calendar_events", "send_email", "read_drive_file"} {
        resp, err := g.ExecuteAction(context.Background(), conn, act, nil, true)
        if err != nil {
            t.Errorf("action %q test mode error: %v", act, err)
        }
        if !resp.Success {
            t.Errorf("action %q test mode: success=%v", act, resp.Success)
        }
    }
}

func TestGoogleWorkspaceConnector_NoToken(t *testing.T) {
    g := NewGoogleWorkspaceConnector()
    conn := &Connection{ID: "c1", ConnectorKey: "google_workspace"}
    resp, _ := g.ExecuteAction(context.Background(), conn, "list_users", nil, false)
    if resp.Code != ErrAuth {
        t.Errorf("Code = %d, want %d", resp.Code, ErrAuth)
    }
}

func TestGoogleWorkspaceConnector_ValidationErrors(t *testing.T) {
    g := NewGoogleWorkspaceConnector()
    conn := &Connection{ID: "c1", ConnectorKey: "google_workspace", EncryptedAccessToken: "token"}
    tests := []struct {
        action string
        params map[string]interface{}
    }{
        {"get_user", nil},
        {"send_email", nil},
        {"read_drive_file", nil},
    }
    for _, tt := range tests {
        resp, _ := g.ExecuteAction(context.Background(), conn, tt.action, tt.params, false)
        if resp.Code != ErrValidation {
            t.Errorf("%s: Code = %d, want %d", tt.action, resp.Code, ErrValidation)
        }
    }
}

func TestGoogleWorkspaceConnector_UnknownAction(t *testing.T) {
    g := NewGoogleWorkspaceConnector()
    conn := &Connection{ID: "c1", ConnectorKey: "google_workspace", EncryptedAccessToken: "token"}
    resp, _ := g.ExecuteAction(context.Background(), conn, "bogus", nil, false)
    if resp.Code != ErrNotFound {
        t.Errorf("Code = %d, want %d", resp.Code, ErrNotFound)
    }
}

func TestGoogleWorkspaceConnector_RefreshValidateAuth(t *testing.T) {
    g := NewGoogleWorkspaceConnector()
    conn := &Connection{ID: "c1", ConnectorKey: "google_workspace"}
    if err := g.RefreshAuth(context.Background(), conn); err != nil {
        t.Errorf("RefreshAuth error: %v", err)
    }
    if err := g.ValidateAuth(context.Background(), conn); err != nil {
        t.Errorf("ValidateAuth error: %v", err)
    }
}

func TestGoogleWorkspaceConnector_401AutoRefresh(t *testing.T) {
    ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusUnauthorized)
        _, _ = w.Write([]byte(`{"error": "unauthorized"}`))
    }))
    defer ts.Close()

    g := NewGoogleWorkspaceConnector()
    conn := &Connection{ID: "c1", ConnectorKey: "google_workspace", EncryptedAccessToken: "token"}
    // Without OAuth2 provider set, 401 is returned as-is from parseAPIResponse
    resp, _ := g.doRequest(context.Background(), "token", conn, "GET", ts.URL, nil)
    if resp.Success {
        t.Error("should not succeed with 401")
    }
    // 401 without oauth2 provider → parsed as API response (code=401)
    if resp.Code != 401 {
        t.Errorf("Code = %d, want 401", resp.Code)
    }
}

func TestHubSpotConnector_Meta(t *testing.T) {
    h := NewHubSpotConnector()
    meta := h.Meta()
    if meta.ConnectorKey != "hubspot" {
        t.Errorf("ConnectorKey = %q, want %q", meta.ConnectorKey, "hubspot")
    }
    if len(meta.Actions) != 5 {
        t.Fatalf("Actions count = %d, want 5", len(meta.Actions))
    }
}

func TestHubSpotConnector_TestMode(t *testing.T) {
    h := NewHubSpotConnector()
    conn := &Connection{ID: "c1", ConnectorKey: "hubspot"}
    for _, act := range []string{"list_contacts", "get_contact", "create_contact", "list_deals", "update_deal"} {
        resp, err := h.ExecuteAction(context.Background(), conn, act, nil, true)
        if err != nil {
            t.Errorf("action %q test mode error: %v", act, err)
        }
        if !resp.Success {
            t.Errorf("action %q test mode: success=%v", act, resp.Success)
        }
    }
}

func TestHubSpotConnector_NoToken(t *testing.T) {
    h := NewHubSpotConnector()
    conn := &Connection{ID: "c1", ConnectorKey: "hubspot"}
    resp, _ := h.ExecuteAction(context.Background(), conn, "list_contacts", nil, false)
    if resp.Code != ErrAuth {
        t.Errorf("Code = %d, want %d", resp.Code, ErrAuth)
    }
}

func TestHubSpotConnector_ValidationErrors(t *testing.T) {
    h := NewHubSpotConnector()
    conn := &Connection{ID: "c1", ConnectorKey: "hubspot", EncryptedAccessToken: "token"}
    tests := []struct {
        action string
        params map[string]interface{}
    }{
        {"get_contact", nil},
        {"create_contact", nil},
        {"update_deal", nil},
    }
    for _, tt := range tests {
        resp, _ := h.ExecuteAction(context.Background(), conn, tt.action, tt.params, false)
        if resp.Code != ErrValidation {
            t.Errorf("%s: Code = %d, want %d", tt.action, resp.Code, ErrValidation)
        }
    }
}

func TestHubSpotConnector_UnknownAction(t *testing.T) {
    h := NewHubSpotConnector()
    conn := &Connection{ID: "c1", ConnectorKey: "hubspot", EncryptedAccessToken: "token"}
    resp, _ := h.ExecuteAction(context.Background(), conn, "bogus", nil, false)
    if resp.Code != ErrNotFound {
        t.Errorf("Code = %d, want %d", resp.Code, ErrNotFound)
    }
}

func TestHubSpotConnector_RefreshValidateAuth(t *testing.T) {
    h := NewHubSpotConnector()
    conn := &Connection{ID: "c1", ConnectorKey: "hubspot"}
    if err := h.RefreshAuth(context.Background(), conn); err != nil {
        t.Errorf("RefreshAuth error: %v", err)
    }
    if err := h.ValidateAuth(context.Background(), conn); err != nil {
        t.Errorf("ValidateAuth error: %v", err)
    }
}

func TestMicrosoftGraphConnector_Meta(t *testing.T) {
    m := NewMicrosoftGraphConnector()
    meta := m.Meta()
    if meta.ConnectorKey != "microsoft_graph" {
        t.Errorf("ConnectorKey = %q, want %q", meta.ConnectorKey, "microsoft_graph")
    }
    if len(meta.Actions) != 5 {
        t.Fatalf("Actions count = %d, want 5", len(meta.Actions))
    }
    for _, a := range meta.Actions {
        if a.Permission != PermissionRead {
            t.Errorf("action %q permission = %q, pilot is read-only", a.ActionKey, a.Permission)
        }
    }
}

func TestMicrosoftGraphConnector_TestMode(t *testing.T) {
    m := NewMicrosoftGraphConnector()
    conn := &Connection{ID: "c1", ConnectorKey: "microsoft_graph"}
    for _, act := range []string{"get_me", "list_messages", "get_message", "list_calendar_events", "list_calendar_instances"} {
        resp, err := m.ExecuteAction(context.Background(), conn, act, nil, true)
        if err != nil {
            t.Errorf("action %q test mode error: %v", act, err)
        }
        if !resp.Success {
            t.Errorf("action %q test mode: success=%v", act, resp.Success)
        }
    }
}

func TestMicrosoftGraphConnector_NoToken(t *testing.T) {
    m := NewMicrosoftGraphConnector()
    conn := &Connection{ID: "c1", ConnectorKey: "microsoft_graph"}
    resp, _ := m.ExecuteAction(context.Background(), conn, "list_messages", nil, false)
    if resp.Code != ErrAuth {
        t.Errorf("Code = %d, want %d", resp.Code, ErrAuth)
    }
}

func TestMicrosoftGraphConnector_ValidationErrors(t *testing.T) {
    m := NewMicrosoftGraphConnector()
    conn := &Connection{ID: "c1", ConnectorKey: "microsoft_graph", EncryptedAccessToken: "token"}
    tests := []struct {
        action string
        params map[string]interface{}
    }{
        {"get_message", nil},
        {"list_calendar_instances", nil},
        {"list_calendar_instances", map[string]interface{}{"startDateTime": "2026-10-31T00:00:00Z"}},
    }
    for _, tt := range tests {
        resp, _ := m.ExecuteAction(context.Background(), conn, tt.action, tt.params, false)
        if resp.Code != ErrValidation {
            t.Errorf("%s: Code = %d, want %d", tt.action, resp.Code, ErrValidation)
        }
    }
}

func TestMicrosoftGraphConnector_UnknownAction(t *testing.T) {
    m := NewMicrosoftGraphConnector()
    conn := &Connection{ID: "c1", ConnectorKey: "microsoft_graph", EncryptedAccessToken: "token"}
    resp, _ := m.ExecuteAction(context.Background(), conn, "send_mail", nil, false)
    // send_mail intentionally not offered (pilot is read-only) → ErrNotFound
    if resp.Code != ErrNotFound {
        t.Errorf("Code = %d, want %d", resp.Code, ErrNotFound)
    }
}

func TestMicrosoftGraphConnector_ListMessages(t *testing.T) {
    var gotPath string
    var gotAuth string
    ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        gotPath = r.URL.Path + "?" + r.URL.RawQuery
        gotAuth = r.Header.Get("Authorization")
        w.WriteHeader(200)
        _, _ = w.Write([]byte(`{"value": [{"id": "msg1", "subject": "hi"}]}`))
    }))
    defer ts.Close()

    m := NewMicrosoftGraphConnector()
    m.baseURLForTest = ts.URL
    conn := &Connection{ID: "c1", ConnectorKey: "microsoft_graph", EncryptedAccessToken: "token"}
    resp, err := m.ExecuteAction(context.Background(), conn, "list_messages", map[string]interface{}{"top": float64(5), "userId": "u1"}, false)
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    if !resp.Success {
        t.Fatalf("success=%v code=%d", resp.Success, resp.Code)
    }
    if gotAuth != "Bearer token" {
        t.Errorf("Authorization = %q", gotAuth)
    }
    if gotPath != "/users/u1/messages?%24top=5" {
        t.Errorf("path = %q, want /users/u1/messages?$top=5", gotPath)
    }
}

func TestMicrosoftGraphConnector_ListCalendarInstances(t *testing.T) {
    var gotQuery string
    ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        gotQuery = r.URL.RawQuery
        w.WriteHeader(200)
        _, _ = w.Write([]byte(`{"value": [{"id": "ev1"}]}`))
    }))
    defer ts.Close()

    m := NewMicrosoftGraphConnector()
    m.baseURLForTest = ts.URL
    conn := &Connection{ID: "c1", ConnectorKey: "microsoft_graph", EncryptedAccessToken: "token"}
    resp, _ := m.ExecuteAction(context.Background(), conn, "list_calendar_instances", map[string]interface{}{
        "startDateTime": "2026-10-01T00:00:00Z",
        "endDateTime":   "2026-11-01T00:00:00Z",
    }, false)
    if !resp.Success {
        t.Fatalf("success=%v code=%d", resp.Success, resp.Code)
    }
    want := "endDateTime=2026-11-01T00%3A00%3A00Z&startDateTime=2026-10-01T00%3A00%3A00Z"
    if gotQuery != want {
        t.Errorf("query = %q, want %q", gotQuery, want)
    }
}

func TestMicrosoftGraphConnector_RefreshValidateAuth(t *testing.T) {
    m := NewMicrosoftGraphConnector()
    conn := &Connection{ID: "c1", ConnectorKey: "microsoft_graph"}
    if err := m.RefreshAuth(context.Background(), conn); err != nil {
        t.Errorf("RefreshAuth error: %v", err)
    }
    if err := m.ValidateAuth(context.Background(), conn); err != nil {
        t.Errorf("ValidateAuth error: %v", err)
    }
}

func TestDefaultOAuth2Endpoints(t *testing.T) {
    authURL, tokenURL := DefaultOAuth2Endpoints("microsoft_graph")
    if authURL != "https://login.microsoftonline.com/common/oauth2/v2.0/authorize" {
        t.Errorf("microsoft_graph authURL = %q", authURL)
    }
    if tokenURL != "https://login.microsoftonline.com/common/oauth2/v2.0/token" {
        t.Errorf("microsoft_graph tokenURL = %q", tokenURL)
    }
    authURL, tokenURL = DefaultOAuth2Endpoints("google_workspace")
    if authURL == "" || tokenURL == "" {
        t.Error("google_workspace should have default endpoints")
    }
    authURL, tokenURL = DefaultOAuth2Endpoints("hubspot")
    if authURL != "" || tokenURL != "" {
        t.Error("hubspot has no well-known endpoints")
    }
}

func TestDefaultOAuth2Scopes(t *testing.T) {
    scopes := DefaultOAuth2Scopes("microsoft_graph")
    if len(scopes) != 3 {
        t.Fatalf("scopes = %v, want [Mail.Read Calendars.Read offline_access]", scopes)
    }
    found := map[string]bool{}
    for _, s := range scopes {
        found[s] = true
    }
    if !found["offline_access"] {
        t.Error("offline_access required for Entra ID refresh tokens")
    }
}

func TestRegisterBuiltins(t *testing.T) {
    r := NewRegistry()
    RegisterBuiltins(r)
    list := r.ListConnectors()
    if len(list) != 4 {
        t.Fatalf("ListConnectors = %d, want 4", len(list))
    }
}

func TestRegisterBuiltins_Integration(t *testing.T) {
    r := NewRegistry()
    RegisterBuiltins(r)
    if err := r.CreateConnection(&Connection{ID: "c1", ConnectorKey: "quickbooks"}); err != nil {
        t.Fatalf("CreateConnection: %v", err)
    }
    req := &ActionRequest{
        ConnectorKey: "quickbooks",
        ActionKey:    "get_company_info",
        ConnectionID: "c1",
    }
    resp, entry := r.ExecuteAction(context.Background(), req)
    if resp.Success {
        t.Error("expected auth error without token")
    }
    if entry == nil {
        t.Error("entry should not be nil")
    }
}

func TestParseAPIResponse(t *testing.T) {
    ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(200)
        _, _ = w.Write([]byte(`{"key": "value"}`))
    }))
    defer ts.Close()
    resp, _ := ts.Client().Get(ts.URL)
    result, _ := parseAPIResponse(resp)
    if !result.Success {
        t.Error("200 should be success")
    }
    data := result.Data.(map[string]interface{})
    if data["key"] != "value" {
        t.Errorf("key = %v, want value", data["key"])
    }
}

func TestParseAPIResponse_Error(t *testing.T) {
    ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(400)
        _, _ = w.Write([]byte(`{"error": "bad request"}`))
    }))
    defer ts.Close()
    resp, _ := ts.Client().Get(ts.URL)
    result, _ := parseAPIResponse(resp)
    if result.Success {
        t.Error("400 should not be success")
    }
    if result.Code != 400 {
        t.Errorf("Code = %d, want 400", result.Code)
    }
}
