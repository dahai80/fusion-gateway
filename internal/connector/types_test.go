package connector

import (
    "testing"
)

func TestAuthTypeConstants(t *testing.T) {
    t.Logf("AuthTypeOAuth2=%s AuthTypeStaticKey=%s AuthTypeBasicAuth=%s",
        AuthTypeOAuth2, AuthTypeStaticKey, AuthTypeBasicAuth)
    if AuthTypeOAuth2 != "oauth2" {
        t.Errorf("AuthTypeOAuth2 = %q, want %q", AuthTypeOAuth2, "oauth2")
    }
    if AuthTypeStaticKey != "static_api_key" {
        t.Errorf("AuthTypeStaticKey = %q, want %q", AuthTypeStaticKey, "static_api_key")
    }
    if AuthTypeBasicAuth != "basic_auth" {
        t.Errorf("AuthTypeBasicAuth = %q, want %q", AuthTypeBasicAuth, "basic_auth")
    }
}

func TestActionPermissionConstants(t *testing.T) {
    t.Logf("PermissionRead=%s PermissionWrite=%s", PermissionRead, PermissionWrite)
    if PermissionRead != "read" {
        t.Errorf("PermissionRead = %q, want %q", PermissionRead, "read")
    }
    if PermissionWrite != "write" {
        t.Errorf("PermissionWrite = %q, want %q", PermissionWrite, "write")
    }
}

func TestErrorCodeConstants(t *testing.T) {
    t.Logf("ErrAuthExpired=%d ErrRateLimited=%d ErrPermissionDenied=%d ErrNotFound=%d ErrTimeout=%d ErrValidation=%d",
        ErrAuthExpired, ErrRateLimited, ErrPermissionDenied, ErrNotFound, ErrTimeout, ErrValidation)
    if ErrAuthExpired != 1001 {
        t.Errorf("ErrAuthExpired = %d, want 1001", ErrAuthExpired)
    }
    if ErrRateLimited != 1002 {
        t.Errorf("ErrRateLimited = %d, want 1002", ErrRateLimited)
    }
    if ErrPermissionDenied != 1003 {
        t.Errorf("ErrPermissionDenied = %d, want 1003", ErrPermissionDenied)
    }
    if ErrNotFound != 1004 {
        t.Errorf("ErrNotFound = %d, want 1004", ErrNotFound)
    }
    if ErrTimeout != 1005 {
        t.Errorf("ErrTimeout = %d, want 1005", ErrTimeout)
    }
    if ErrValidation != 2001 {
        t.Errorf("ErrValidation = %d, want 2001", ErrValidation)
    }
}

func TestErrorWithCode(t *testing.T) {
    resp := ErrorWithCode(ErrAuthExpired, "token expired")
    if resp.Success {
        t.Error("ErrorWithCode response should have Success=false")
    }
    if resp.Code != ErrAuthExpired {
        t.Errorf("Code = %d, want %d", resp.Code, ErrAuthExpired)
    }
    if resp.Message != "token expired" {
        t.Errorf("Message = %q, want %q", resp.Message, "token expired")
    }
    if resp.Data != nil {
        t.Errorf("Data = %v, want nil", resp.Data)
    }
    t.Logf("ErrorWithCode result: success=%v code=%d message=%q", resp.Success, resp.Code, resp.Message)
}

func TestErrorWithCode_AllCodes(t *testing.T) {
    cases := []struct {
        code int
        msg  string
    }{
        {ErrAuthExpired, "auth expired"},
        {ErrRateLimited, "rate limited"},
        {ErrPermissionDenied, "permission denied"},
        {ErrNotFound, "not found"},
        {ErrTimeout, "timeout"},
        {ErrValidation, "validation failed"},
    }
    for _, tc := range cases {
        resp := ErrorWithCode(tc.code, tc.msg)
        if resp.Success {
            t.Errorf("code=%d: Success should be false", tc.code)
        }
        if resp.Code != tc.code {
            t.Errorf("code=%d: Code = %d, want %d", tc.code, resp.Code, tc.code)
        }
        if resp.Message != tc.msg {
            t.Errorf("code=%d: Message = %q, want %q", tc.code, resp.Message, tc.msg)
        }
        t.Logf("ErrorWithCode(%d, %q) => success=%v code=%d msg=%q", tc.code, tc.msg, resp.Success, resp.Code, resp.Message)
    }
}

func TestActionResponseFields(t *testing.T) {
    r := &ActionResponse{
        Success: true,
        Code:    0,
        Data:    map[string]interface{}{"key": "value"},
        Message: "ok",
    }
    if !r.Success {
        t.Error("Success should be true")
    }
    if r.Code != 0 {
        t.Errorf("Code = %d, want 0", r.Code)
    }
    if r.Message != "ok" {
        t.Errorf("Message = %q, want %q", r.Message, "ok")
    }
    t.Logf("ActionResponse: success=%v code=%d data=%v message=%q", r.Success, r.Code, r.Data, r.Message)
}

func TestConnectionStruct(t *testing.T) {
    conn := &Connection{
        ID:           "conn-1",
        ConnectorKey: "quickbooks",
        AuthType:     AuthTypeOAuth2,
        Status:       "active",
    }
    if conn.ID != "conn-1" {
        t.Errorf("ID = %q, want %q", conn.ID, "conn-1")
    }
    if conn.ConnectorKey != "quickbooks" {
        t.Errorf("ConnectorKey = %q, want %q", conn.ConnectorKey, "quickbooks")
    }
    if conn.ExpiresAt != nil {
        t.Error("ExpiresAt should be nil")
    }
    t.Logf("Connection: id=%s connector=%s auth=%s status=%s", conn.ID, conn.ConnectorKey, conn.AuthType, conn.Status)
}

func TestActionRequestStruct(t *testing.T) {
    req := &ActionRequest{
        ConnectorKey: "quickbooks",
        ActionKey:    "list_customers",
        ConnectionID: "conn-1",
        Params:       map[string]interface{}{"page": 1},
        TestMode:     true,
    }
    if req.ConnectorKey != "quickbooks" {
        t.Errorf("ConnectorKey = %q, want %q", req.ConnectorKey, "quickbooks")
    }
    if req.ActionKey != "list_customers" {
        t.Errorf("ActionKey = %q, want %q", req.ActionKey, "list_customers")
    }
    if !req.TestMode {
        t.Error("TestMode should be true")
    }
    t.Logf("ActionRequest: connector=%s action=%s connID=%s params=%v test=%v",
        req.ConnectorKey, req.ActionKey, req.ConnectionID, req.Params, req.TestMode)
}

func TestAuditEntryStruct(t *testing.T) {
    entry := AuditEntry{
        ConnectionID: "conn-1",
        ConnectorKey: "quickbooks",
        ActionKey:    "list_customers",
        Permission:   "read",
        Success:      true,
        ErrorCode:    0,
        InputSummary: "page=1",
        DurationMS:   42,
    }
    if entry.ConnectionID != "conn-1" {
        t.Errorf("ConnectionID = %q, want %q", entry.ConnectionID, "conn-1")
    }
    if entry.DurationMS != 42 {
        t.Errorf("DurationMS = %d, want 42", entry.DurationMS)
    }
    t.Logf("AuditEntry: connID=%s connector=%s action=%s perm=%s success=%v dur=%d",
        entry.ConnectionID, entry.ConnectorKey, entry.ActionKey, entry.Permission, entry.Success, entry.DurationMS)
}

func TestActionDefStruct(t *testing.T) {
    def := ActionDef{
        ActionKey:   "create_invoice",
        DisplayName: "Create Invoice",
        Permission:  PermissionWrite,
    }
    if def.Permission != PermissionWrite {
        t.Errorf("Permission = %q, want %q", def.Permission, PermissionWrite)
    }
    if def.InputSchema != nil {
        t.Error("InputSchema should be nil when not set")
    }
    t.Logf("ActionDef: key=%s display=%s perm=%s", def.ActionKey, def.DisplayName, def.Permission)
}

func TestConnectorMetaStruct(t *testing.T) {
    meta := ConnectorMeta{
        ConnectorKey: "test",
        DisplayName:  "Test Connector",
        Icon:         "icon-test",
        AuthType:     AuthTypeStaticKey,
        Description:  "A test connector",
        Actions:      []ActionDef{{ActionKey: "act1", Permission: PermissionRead}},
    }
    if meta.ConnectorKey != "test" {
        t.Errorf("ConnectorKey = %q, want %q", meta.ConnectorKey, "test")
    }
    if len(meta.Actions) != 1 {
        t.Errorf("len(Actions) = %d, want 1", len(meta.Actions))
    }
    t.Logf("ConnectorMeta: key=%s display=%s auth=%s actions=%d", meta.ConnectorKey, meta.DisplayName, meta.AuthType, len(meta.Actions))
}
