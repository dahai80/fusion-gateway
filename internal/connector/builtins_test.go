package connector

import (
    "context"
    "testing"
)

// ============================================================
// QuickBooksConnector
// ============================================================

func TestQuickBooksConnector_Meta(t *testing.T) {
    q := NewQuickBooksConnector()
    meta := q.Meta()
    if meta.ConnectorKey != "quickbooks" {
        t.Errorf("ConnectorKey = %q, want %q", meta.ConnectorKey, "quickbooks")
    }
    if meta.DisplayName != "QuickBooks" {
        t.Errorf("DisplayName = %q, want %q", meta.DisplayName, "QuickBooks")
    }
    if meta.AuthType != AuthTypeOAuth2 {
        t.Errorf("AuthType = %q, want %q", meta.AuthType, AuthTypeOAuth2)
    }
    if len(meta.Actions) != 4 {
        t.Fatalf("Actions count = %d, want 4", len(meta.Actions))
    }
    expectedActions := map[string]ActionPermission{
        "query_overdue_invoice": PermissionRead,
        "list_customers":       PermissionRead,
        "create_invoice":       PermissionWrite,
        "get_company_info":     PermissionRead,
    }
    for _, a := range meta.Actions {
        if perm, ok := expectedActions[a.ActionKey]; !ok {
            t.Errorf("unexpected action %q", a.ActionKey)
        } else if a.Permission != perm {
            t.Errorf("action %q permission = %q, want %q", a.ActionKey, a.Permission, perm)
        }
    }
    t.Logf("QuickBooks meta: key=%s actions=%d icon=%s", meta.ConnectorKey, len(meta.Actions), meta.Icon)
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
            t.Errorf("action %q test mode: success=%v code=%d msg=%q", act, resp.Success, resp.Code, resp.Message)
        }
        data, ok := resp.Data.(map[string]interface{})
        if !ok {
            t.Errorf("action %q test mode data type = %T", act, resp.Data)
            continue
        }
        if data["test"] != true {
            t.Errorf("action %q test mode data[test] = %v, want true", act, data["test"])
        }
        if data["action"] != act {
            t.Errorf("action %q test mode data[action] = %v, want %q", act, data["action"], act)
        }
        t.Logf("QuickBooks testMode %q: success=%v", act, resp.Success)
    }
}

func TestQuickBooksConnector_QueryOverdueInvoice(t *testing.T) {
    q := NewQuickBooksConnector()
    conn := &Connection{ID: "c1", ConnectorKey: "quickbooks"}
    resp, err := q.ExecuteAction(context.Background(), conn, "query_overdue_invoice", nil, false)
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    if !resp.Success {
        t.Errorf("success=%v code=%d", resp.Success, resp.Code)
    }
    data := resp.Data.(map[string]interface{})
    if _, ok := data["invoices"]; !ok {
        t.Error("data should contain invoices")
    }
    t.Logf("query_overdue_invoice: success=%v data=%v", resp.Success, data)
}

func TestQuickBooksConnector_ListCustomers(t *testing.T) {
    q := NewQuickBooksConnector()
    conn := &Connection{ID: "c1", ConnectorKey: "quickbooks"}
    resp, err := q.ExecuteAction(context.Background(), conn, "list_customers", nil, false)
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    if !resp.Success {
        t.Errorf("success=%v code=%d", resp.Success, resp.Code)
    }
    data := resp.Data.(map[string]interface{})
    if _, ok := data["customers"]; !ok {
        t.Error("data should contain customers")
    }
    t.Logf("list_customers: success=%v", resp.Success)
}

func TestQuickBooksConnector_CreateInvoice_Valid(t *testing.T) {
    q := NewQuickBooksConnector()
    conn := &Connection{ID: "c1", ConnectorKey: "quickbooks"}
    params := map[string]interface{}{"customerName": "Acme Corp"}
    resp, err := q.ExecuteAction(context.Background(), conn, "create_invoice", params, false)
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    if !resp.Success {
        t.Errorf("success=%v code=%d msg=%q", resp.Success, resp.Code, resp.Message)
    }
    data := resp.Data.(map[string]interface{})
    if data["customerName"] != "Acme Corp" {
        t.Errorf("customerName = %v, want Acme Corp", data["customerName"])
    }
    t.Logf("create_invoice valid: success=%v data=%v", resp.Success, data)
}

func TestQuickBooksConnector_CreateInvoice_MissingName(t *testing.T) {
    q := NewQuickBooksConnector()
    conn := &Connection{ID: "c1", ConnectorKey: "quickbooks"}
    resp, err := q.ExecuteAction(context.Background(), conn, "create_invoice", nil, false)
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    if resp.Success {
        t.Error("should fail without customerName")
    }
    if resp.Code != ErrValidation {
        t.Errorf("Code = %d, want %d", resp.Code, ErrValidation)
    }
    t.Logf("create_invoice missing name: code=%d msg=%q", resp.Code, resp.Message)
}

func TestQuickBooksConnector_CreateInvoice_EmptyName(t *testing.T) {
    q := NewQuickBooksConnector()
    conn := &Connection{ID: "c1", ConnectorKey: "quickbooks"}
    params := map[string]interface{}{"customerName": ""}
    resp, err := q.ExecuteAction(context.Background(), conn, "create_invoice", params, false)
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    if resp.Success {
        t.Error("should fail with empty customerName")
    }
    if resp.Code != ErrValidation {
        t.Errorf("Code = %d, want %d", resp.Code, ErrValidation)
    }
    t.Logf("create_invoice empty name: code=%d msg=%q", resp.Code, resp.Message)
}

func TestQuickBooksConnector_GetCompanyInfo(t *testing.T) {
    q := NewQuickBooksConnector()
    conn := &Connection{ID: "c1", ConnectorKey: "quickbooks"}
    resp, err := q.ExecuteAction(context.Background(), conn, "get_company_info", nil, false)
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    if !resp.Success {
        t.Errorf("success=%v code=%d", resp.Success, resp.Code)
    }
    data := resp.Data.(map[string]interface{})
    if data["companyName"] != "Test Company" {
        t.Errorf("companyName = %v, want Test Company", data["companyName"])
    }
    t.Logf("get_company_info: success=%v data=%v", resp.Success, data)
}

func TestQuickBooksConnector_UnknownAction(t *testing.T) {
    q := NewQuickBooksConnector()
    conn := &Connection{ID: "c1", ConnectorKey: "quickbooks"}
    resp, err := q.ExecuteAction(context.Background(), conn, "bogus_action", nil, false)
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    if resp.Success {
        t.Error("unknown action should not succeed")
    }
    if resp.Code != ErrNotFound {
        t.Errorf("Code = %d, want %d", resp.Code, ErrNotFound)
    }
    t.Logf("QuickBooks unknown action: code=%d msg=%q", resp.Code, resp.Message)
}

func TestQuickBooksConnector_RefreshAuth(t *testing.T) {
    q := NewQuickBooksConnector()
    conn := &Connection{ID: "c1", ConnectorKey: "quickbooks"}
    if err := q.RefreshAuth(context.Background(), conn); err != nil {
        t.Errorf("RefreshAuth error: %v", err)
    }
    t.Logf("QuickBooks RefreshAuth: ok")
}

func TestQuickBooksConnector_ValidateAuth(t *testing.T) {
    q := NewQuickBooksConnector()
    conn := &Connection{ID: "c1", ConnectorKey: "quickbooks"}
    if err := q.ValidateAuth(context.Background(), conn); err != nil {
        t.Errorf("ValidateAuth error: %v", err)
    }
    t.Logf("QuickBooks ValidateAuth: ok")
}

// ============================================================
// GoogleWorkspaceConnector
// ============================================================

func TestGoogleWorkspaceConnector_Meta(t *testing.T) {
    g := NewGoogleWorkspaceConnector()
    meta := g.Meta()
    if meta.ConnectorKey != "google_workspace" {
        t.Errorf("ConnectorKey = %q, want %q", meta.ConnectorKey, "google_workspace")
    }
    if meta.DisplayName != "Google Workspace" {
        t.Errorf("DisplayName = %q, want %q", meta.DisplayName, "Google Workspace")
    }
    if meta.AuthType != AuthTypeOAuth2 {
        t.Errorf("AuthType = %q, want %q", meta.AuthType, AuthTypeOAuth2)
    }
    if len(meta.Actions) != 5 {
        t.Fatalf("Actions count = %d, want 5", len(meta.Actions))
    }
    expectedActions := map[string]ActionPermission{
        "list_users":          PermissionRead,
        "get_user":            PermissionRead,
        "list_calendar_events": PermissionRead,
        "send_email":          PermissionWrite,
        "read_drive_file":     PermissionRead,
    }
    for _, a := range meta.Actions {
        if perm, ok := expectedActions[a.ActionKey]; !ok {
            t.Errorf("unexpected action %q", a.ActionKey)
        } else if a.Permission != perm {
            t.Errorf("action %q permission = %q, want %q", a.ActionKey, a.Permission, perm)
        }
    }
    t.Logf("GoogleWorkspace meta: key=%s actions=%d", meta.ConnectorKey, len(meta.Actions))
}

func TestGoogleWorkspaceConnector_TestMode(t *testing.T) {
    g := NewGoogleWorkspaceConnector()
    conn := &Connection{ID: "c1", ConnectorKey: "google_workspace"}
    actions := []string{"list_users", "get_user", "list_calendar_events", "send_email", "read_drive_file"}
    for _, act := range actions {
        resp, err := g.ExecuteAction(context.Background(), conn, act, nil, true)
        if err != nil {
            t.Errorf("action %q test mode error: %v", act, err)
        }
        if !resp.Success {
            t.Errorf("action %q test mode: success=%v", act, resp.Success)
        }
        data := resp.Data.(map[string]interface{})
        if data["test"] != true {
            t.Errorf("action %q data[test] = %v, want true", act, data["test"])
        }
        t.Logf("GoogleWorkspace testMode %q: success=%v", act, resp.Success)
    }
}

func TestGoogleWorkspaceConnector_ListUsers(t *testing.T) {
    g := NewGoogleWorkspaceConnector()
    conn := &Connection{ID: "c1", ConnectorKey: "google_workspace"}
    resp, err := g.ExecuteAction(context.Background(), conn, "list_users", nil, false)
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    if !resp.Success {
        t.Errorf("success=%v code=%d", resp.Success, resp.Code)
    }
    data := resp.Data.(map[string]interface{})
    if _, ok := data["users"]; !ok {
        t.Error("data should contain users")
    }
    t.Logf("list_users: success=%v", resp.Success)
}

func TestGoogleWorkspaceConnector_GetUser_Valid(t *testing.T) {
    g := NewGoogleWorkspaceConnector()
    conn := &Connection{ID: "c1", ConnectorKey: "google_workspace"}
    params := map[string]interface{}{"email": "user@example.com"}
    resp, err := g.ExecuteAction(context.Background(), conn, "get_user", params, false)
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    if !resp.Success {
        t.Errorf("success=%v code=%d", resp.Success, resp.Code)
    }
    data := resp.Data.(map[string]interface{})
    if data["email"] != "user@example.com" {
        t.Errorf("email = %v, want user@example.com", data["email"])
    }
    t.Logf("get_user valid: success=%v data=%v", resp.Success, data)
}

func TestGoogleWorkspaceConnector_GetUser_MissingEmail(t *testing.T) {
    g := NewGoogleWorkspaceConnector()
    conn := &Connection{ID: "c1", ConnectorKey: "google_workspace"}
    resp, err := g.ExecuteAction(context.Background(), conn, "get_user", nil, false)
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    if resp.Success {
        t.Error("should fail without email")
    }
    if resp.Code != ErrValidation {
        t.Errorf("Code = %d, want %d", resp.Code, ErrValidation)
    }
    t.Logf("get_user missing email: code=%d msg=%q", resp.Code, resp.Message)
}

func TestGoogleWorkspaceConnector_ListCalendarEvents(t *testing.T) {
    g := NewGoogleWorkspaceConnector()
    conn := &Connection{ID: "c1", ConnectorKey: "google_workspace"}
    resp, err := g.ExecuteAction(context.Background(), conn, "list_calendar_events", nil, false)
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    if !resp.Success {
        t.Errorf("success=%v code=%d", resp.Success, resp.Code)
    }
    data := resp.Data.(map[string]interface{})
    if _, ok := data["events"]; !ok {
        t.Error("data should contain events")
    }
    t.Logf("list_calendar_events: success=%v", resp.Success)
}

func TestGoogleWorkspaceConnector_SendEmail_Valid(t *testing.T) {
    g := NewGoogleWorkspaceConnector()
    conn := &Connection{ID: "c1", ConnectorKey: "google_workspace"}
    params := map[string]interface{}{"to": "dest@example.com"}
    resp, err := g.ExecuteAction(context.Background(), conn, "send_email", params, false)
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    if !resp.Success {
        t.Errorf("success=%v code=%d", resp.Success, resp.Code)
    }
    data := resp.Data.(map[string]interface{})
    if data["to"] != "dest@example.com" {
        t.Errorf("to = %v, want dest@example.com", data["to"])
    }
    t.Logf("send_email valid: success=%v data=%v", resp.Success, data)
}

func TestGoogleWorkspaceConnector_SendEmail_MissingTo(t *testing.T) {
    g := NewGoogleWorkspaceConnector()
    conn := &Connection{ID: "c1", ConnectorKey: "google_workspace"}
    resp, err := g.ExecuteAction(context.Background(), conn, "send_email", nil, false)
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    if resp.Success {
        t.Error("should fail without to")
    }
    if resp.Code != ErrValidation {
        t.Errorf("Code = %d, want %d", resp.Code, ErrValidation)
    }
    t.Logf("send_email missing to: code=%d msg=%q", resp.Code, resp.Message)
}

func TestGoogleWorkspaceConnector_ReadDriveFile_Valid(t *testing.T) {
    g := NewGoogleWorkspaceConnector()
    conn := &Connection{ID: "c1", ConnectorKey: "google_workspace"}
    params := map[string]interface{}{"fileId": "file123"}
    resp, err := g.ExecuteAction(context.Background(), conn, "read_drive_file", params, false)
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    if !resp.Success {
        t.Errorf("success=%v code=%d", resp.Success, resp.Code)
    }
    data := resp.Data.(map[string]interface{})
    if data["fileId"] != "file123" {
        t.Errorf("fileId = %v, want file123", data["fileId"])
    }
    t.Logf("read_drive_file valid: success=%v data=%v", resp.Success, data)
}

func TestGoogleWorkspaceConnector_ReadDriveFile_MissingFileId(t *testing.T) {
    g := NewGoogleWorkspaceConnector()
    conn := &Connection{ID: "c1", ConnectorKey: "google_workspace"}
    resp, err := g.ExecuteAction(context.Background(), conn, "read_drive_file", nil, false)
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    if resp.Success {
        t.Error("should fail without fileId")
    }
    if resp.Code != ErrValidation {
        t.Errorf("Code = %d, want %d", resp.Code, ErrValidation)
    }
    t.Logf("read_drive_file missing fileId: code=%d msg=%q", resp.Code, resp.Message)
}

func TestGoogleWorkspaceConnector_UnknownAction(t *testing.T) {
    g := NewGoogleWorkspaceConnector()
    conn := &Connection{ID: "c1", ConnectorKey: "google_workspace"}
    resp, err := g.ExecuteAction(context.Background(), conn, "bogus", nil, false)
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    if resp.Success {
        t.Error("unknown action should not succeed")
    }
    if resp.Code != ErrNotFound {
        t.Errorf("Code = %d, want %d", resp.Code, ErrNotFound)
    }
    t.Logf("GoogleWorkspace unknown action: code=%d msg=%q", resp.Code, resp.Message)
}

func TestGoogleWorkspaceConnector_RefreshAuth(t *testing.T) {
    g := NewGoogleWorkspaceConnector()
    conn := &Connection{ID: "c1", ConnectorKey: "google_workspace"}
    if err := g.RefreshAuth(context.Background(), conn); err != nil {
        t.Errorf("RefreshAuth error: %v", err)
    }
    t.Logf("GoogleWorkspace RefreshAuth: ok")
}

func TestGoogleWorkspaceConnector_ValidateAuth(t *testing.T) {
    g := NewGoogleWorkspaceConnector()
    conn := &Connection{ID: "c1", ConnectorKey: "google_workspace"}
    if err := g.ValidateAuth(context.Background(), conn); err != nil {
        t.Errorf("ValidateAuth error: %v", err)
    }
    t.Logf("GoogleWorkspace ValidateAuth: ok")
}

// ============================================================
// HubSpotConnector
// ============================================================

func TestHubSpotConnector_Meta(t *testing.T) {
    h := NewHubSpotConnector()
    meta := h.Meta()
    if meta.ConnectorKey != "hubspot" {
        t.Errorf("ConnectorKey = %q, want %q", meta.ConnectorKey, "hubspot")
    }
    if meta.DisplayName != "HubSpot" {
        t.Errorf("DisplayName = %q, want %q", meta.DisplayName, "HubSpot")
    }
    if meta.AuthType != AuthTypeOAuth2 {
        t.Errorf("AuthType = %q, want %q", meta.AuthType, AuthTypeOAuth2)
    }
    if len(meta.Actions) != 5 {
        t.Fatalf("Actions count = %d, want 5", len(meta.Actions))
    }
    expectedActions := map[string]ActionPermission{
        "list_contacts":  PermissionRead,
        "get_contact":    PermissionRead,
        "create_contact": PermissionWrite,
        "list_deals":     PermissionRead,
        "update_deal":    PermissionWrite,
    }
    for _, a := range meta.Actions {
        if perm, ok := expectedActions[a.ActionKey]; !ok {
            t.Errorf("unexpected action %q", a.ActionKey)
        } else if a.Permission != perm {
            t.Errorf("action %q permission = %q, want %q", a.ActionKey, a.Permission, perm)
        }
    }
    t.Logf("HubSpot meta: key=%s actions=%d", meta.ConnectorKey, len(meta.Actions))
}

func TestHubSpotConnector_TestMode(t *testing.T) {
    h := NewHubSpotConnector()
    conn := &Connection{ID: "c1", ConnectorKey: "hubspot"}
    actions := []string{"list_contacts", "get_contact", "create_contact", "list_deals", "update_deal"}
    for _, act := range actions {
        resp, err := h.ExecuteAction(context.Background(), conn, act, nil, true)
        if err != nil {
            t.Errorf("action %q test mode error: %v", act, err)
        }
        if !resp.Success {
            t.Errorf("action %q test mode: success=%v", act, resp.Success)
        }
        data := resp.Data.(map[string]interface{})
        if data["test"] != true {
            t.Errorf("action %q data[test] = %v, want true", act, data["test"])
        }
        t.Logf("HubSpot testMode %q: success=%v", act, resp.Success)
    }
}

func TestHubSpotConnector_ListContacts(t *testing.T) {
    h := NewHubSpotConnector()
    conn := &Connection{ID: "c1", ConnectorKey: "hubspot"}
    resp, err := h.ExecuteAction(context.Background(), conn, "list_contacts", nil, false)
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    if !resp.Success {
        t.Errorf("success=%v code=%d", resp.Success, resp.Code)
    }
    data := resp.Data.(map[string]interface{})
    if _, ok := data["contacts"]; !ok {
        t.Error("data should contain contacts")
    }
    t.Logf("list_contacts: success=%v", resp.Success)
}

func TestHubSpotConnector_GetContact_Valid(t *testing.T) {
    h := NewHubSpotConnector()
    conn := &Connection{ID: "c1", ConnectorKey: "hubspot"}
    params := map[string]interface{}{"contactId": "ct-001"}
    resp, err := h.ExecuteAction(context.Background(), conn, "get_contact", params, false)
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    if !resp.Success {
        t.Errorf("success=%v code=%d", resp.Success, resp.Code)
    }
    data := resp.Data.(map[string]interface{})
    if data["contactId"] != "ct-001" {
        t.Errorf("contactId = %v, want ct-001", data["contactId"])
    }
    t.Logf("get_contact valid: success=%v data=%v", resp.Success, data)
}

func TestHubSpotConnector_GetContact_MissingId(t *testing.T) {
    h := NewHubSpotConnector()
    conn := &Connection{ID: "c1", ConnectorKey: "hubspot"}
    resp, err := h.ExecuteAction(context.Background(), conn, "get_contact", nil, false)
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    if resp.Success {
        t.Error("should fail without contactId")
    }
    if resp.Code != ErrValidation {
        t.Errorf("Code = %d, want %d", resp.Code, ErrValidation)
    }
    t.Logf("get_contact missing id: code=%d msg=%q", resp.Code, resp.Message)
}

func TestHubSpotConnector_CreateContact_Valid(t *testing.T) {
    h := NewHubSpotConnector()
    conn := &Connection{ID: "c1", ConnectorKey: "hubspot"}
    params := map[string]interface{}{"email": "new@example.com"}
    resp, err := h.ExecuteAction(context.Background(), conn, "create_contact", params, false)
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    if !resp.Success {
        t.Errorf("success=%v code=%d", resp.Success, resp.Code)
    }
    data := resp.Data.(map[string]interface{})
    if data["email"] != "new@example.com" {
        t.Errorf("email = %v, want new@example.com", data["email"])
    }
    t.Logf("create_contact valid: success=%v data=%v", resp.Success, data)
}

func TestHubSpotConnector_CreateContact_MissingEmail(t *testing.T) {
    h := NewHubSpotConnector()
    conn := &Connection{ID: "c1", ConnectorKey: "hubspot"}
    resp, err := h.ExecuteAction(context.Background(), conn, "create_contact", nil, false)
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    if resp.Success {
        t.Error("should fail without email")
    }
    if resp.Code != ErrValidation {
        t.Errorf("Code = %d, want %d", resp.Code, ErrValidation)
    }
    t.Logf("create_contact missing email: code=%d msg=%q", resp.Code, resp.Message)
}

func TestHubSpotConnector_ListDeals(t *testing.T) {
    h := NewHubSpotConnector()
    conn := &Connection{ID: "c1", ConnectorKey: "hubspot"}
    resp, err := h.ExecuteAction(context.Background(), conn, "list_deals", nil, false)
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    if !resp.Success {
        t.Errorf("success=%v code=%d", resp.Success, resp.Code)
    }
    data := resp.Data.(map[string]interface{})
    if _, ok := data["deals"]; !ok {
        t.Error("data should contain deals")
    }
    t.Logf("list_deals: success=%v", resp.Success)
}

func TestHubSpotConnector_UpdateDeal_Valid(t *testing.T) {
    h := NewHubSpotConnector()
    conn := &Connection{ID: "c1", ConnectorKey: "hubspot"}
    params := map[string]interface{}{"dealId": "deal-001"}
    resp, err := h.ExecuteAction(context.Background(), conn, "update_deal", params, false)
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    if !resp.Success {
        t.Errorf("success=%v code=%d", resp.Success, resp.Code)
    }
    data := resp.Data.(map[string]interface{})
    if data["dealId"] != "deal-001" {
        t.Errorf("dealId = %v, want deal-001", data["dealId"])
    }
    t.Logf("update_deal valid: success=%v data=%v", resp.Success, data)
}

func TestHubSpotConnector_UpdateDeal_MissingDealId(t *testing.T) {
    h := NewHubSpotConnector()
    conn := &Connection{ID: "c1", ConnectorKey: "hubspot"}
    resp, err := h.ExecuteAction(context.Background(), conn, "update_deal", nil, false)
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    if resp.Success {
        t.Error("should fail without dealId")
    }
    if resp.Code != ErrValidation {
        t.Errorf("Code = %d, want %d", resp.Code, ErrValidation)
    }
    t.Logf("update_deal missing dealId: code=%d msg=%q", resp.Code, resp.Message)
}

func TestHubSpotConnector_UnknownAction(t *testing.T) {
    h := NewHubSpotConnector()
    conn := &Connection{ID: "c1", ConnectorKey: "hubspot"}
    resp, err := h.ExecuteAction(context.Background(), conn, "bogus", nil, false)
    if err != nil {
        t.Fatalf("error: %v", err)
    }
    if resp.Success {
        t.Error("unknown action should not succeed")
    }
    if resp.Code != ErrNotFound {
        t.Errorf("Code = %d, want %d", resp.Code, ErrNotFound)
    }
    t.Logf("HubSpot unknown action: code=%d msg=%q", resp.Code, resp.Message)
}

func TestHubSpotConnector_RefreshAuth(t *testing.T) {
    h := NewHubSpotConnector()
    conn := &Connection{ID: "c1", ConnectorKey: "hubspot"}
    if err := h.RefreshAuth(context.Background(), conn); err != nil {
        t.Errorf("RefreshAuth error: %v", err)
    }
    t.Logf("HubSpot RefreshAuth: ok")
}

func TestHubSpotConnector_ValidateAuth(t *testing.T) {
    h := NewHubSpotConnector()
    conn := &Connection{ID: "c1", ConnectorKey: "hubspot"}
    if err := h.ValidateAuth(context.Background(), conn); err != nil {
        t.Errorf("ValidateAuth error: %v", err)
    }
    t.Logf("HubSpot ValidateAuth: ok")
}

// ============================================================
// RegisterBuiltins
// ============================================================

func TestRegisterBuiltins(t *testing.T) {
    r := NewRegistry()
    RegisterBuiltins(r)

    list := r.ListConnectors()
    if len(list) != 3 {
        t.Fatalf("ListConnectors = %d, want 3", len(list))
    }
    keys := map[string]bool{}
    for _, m := range list {
        keys[m.ConnectorKey] = true
    }
    for _, k := range []string{"quickbooks", "google_workspace", "hubspot"} {
        if !keys[k] {
            t.Errorf("missing connector %q", k)
        }
    }

    qb, ok := r.Get("quickbooks")
    if !ok {
        t.Fatal("quickbooks not found")
    }
    if len(qb.Meta().Actions) != 4 {
        t.Errorf("quickbooks actions = %d, want 4", len(qb.Meta().Actions))
    }

    gw, ok := r.Get("google_workspace")
    if !ok {
        t.Fatal("google_workspace not found")
    }
    if len(gw.Meta().Actions) != 5 {
        t.Errorf("google_workspace actions = %d, want 5", len(gw.Meta().Actions))
    }

    hs, ok := r.Get("hubspot")
    if !ok {
        t.Fatal("hubspot not found")
    }
    if len(hs.Meta().Actions) != 5 {
        t.Errorf("hubspot actions = %d, want 5", len(hs.Meta().Actions))
    }

    t.Logf("RegisterBuiltins: connectors=%d keys=%v", len(list), keys)
}

func TestRegisterBuiltins_Integration(t *testing.T) {
    r := NewRegistry()
    RegisterBuiltins(r)

    // Create connection and execute action through registry
    if err := r.CreateConnection(&Connection{ID: "c1", ConnectorKey: "quickbooks"}); err != nil {
        t.Fatalf("CreateConnection: %v", err)
    }
    req := &ActionRequest{
        ConnectorKey: "quickbooks",
        ActionKey:    "get_company_info",
        ConnectionID: "c1",
    }
    resp, entry := r.ExecuteAction(context.Background(), req)
    if !resp.Success {
        t.Errorf("ExecuteAction failed: code=%d msg=%q", resp.Code, resp.Message)
    }
    if entry == nil {
        t.Error("entry should not be nil")
    }
    t.Logf("RegisterBuiltins integration: success=%v entry=%+v", resp.Success, entry)
}
