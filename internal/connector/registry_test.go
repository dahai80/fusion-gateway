package connector

import (
    "context"
    "errors"
    "fmt"
    "strings"
    "testing"
)

type mockConnector struct {
    meta        *ConnectorMeta
    executeFunc func(ctx context.Context, conn *Connection, actionKey string, params map[string]interface{}, testMode bool) (*ActionResponse, error)
    refreshErr  error
    validateErr error
}

func (m *mockConnector) Meta() *ConnectorMeta {
    return m.meta
}

func (m *mockConnector) ExecuteAction(ctx context.Context, conn *Connection, actionKey string, params map[string]interface{}, testMode bool) (*ActionResponse, error) {
    if m.executeFunc != nil {
        return m.executeFunc(ctx, conn, actionKey, params, testMode)
    }
    return &ActionResponse{Success: true, Code: 0}, nil
}

func (m *mockConnector) RefreshAuth(ctx context.Context, conn *Connection) error {
    return m.refreshErr
}

func (m *mockConnector) ValidateAuth(ctx context.Context, conn *Connection) error {
    return m.validateErr
}

func newMockConnector(key string, actions []ActionDef) *mockConnector {
    return &mockConnector{
        meta: &ConnectorMeta{
            ConnectorKey: key,
            DisplayName:  key + " display",
            AuthType:     AuthTypeOAuth2,
            Actions:      actions,
        },
    }
}

func TestNewRegistry(t *testing.T) {
    r := NewRegistry()
    if r == nil {
        t.Fatal("NewRegistry returned nil")
    }
    if r.connectors == nil {
        t.Error("connectors map should be initialized")
    }
    if r.connections == nil {
        t.Error("connections map should be initialized")
    }
    if r.auditLog == nil {
        t.Error("auditLog should be initialized")
    }
    if r.auditMaxLen != 10000 {
        t.Errorf("auditMaxLen = %d, want 10000", r.auditMaxLen)
    }
    t.Logf("NewRegistry: connectors=%d connections=%d auditMaxLen=%d",
        len(r.connectors), len(r.connections), r.auditMaxLen)
}

func TestRegistry_RegisterAndGet(t *testing.T) {
    r := NewRegistry()
    mc := newMockConnector("test-conn", []ActionDef{
        {ActionKey: "act1", Permission: PermissionRead},
    })
    r.Register(mc)

    got, ok := r.Get("test-conn")
    if !ok {
        t.Fatal("Get should find registered connector")
    }
    if got.Meta().ConnectorKey != "test-conn" {
        t.Errorf("ConnectorKey = %q, want %q", got.Meta().ConnectorKey, "test-conn")
    }

    _, ok = r.Get("nonexistent")
    if ok {
        t.Error("Get should not find unregistered connector")
    }
    t.Logf("Register+Get: key=%q found=%v", "test-conn", ok)
}

func TestRegistry_RegisterOverwrite(t *testing.T) {
    r := NewRegistry()
    mc1 := newMockConnector("dup", []ActionDef{{ActionKey: "a1"}})
    mc2 := newMockConnector("dup", []ActionDef{{ActionKey: "a2"}})
    r.Register(mc1)
    r.Register(mc2)
    got, _ := r.Get("dup")
    if len(got.Meta().Actions) != 1 || got.Meta().Actions[0].ActionKey != "a2" {
        t.Errorf("overwritten connector actions = %v, want [{a2}]", got.Meta().Actions)
    }
    t.Logf("Register overwrite: actions=%v", got.Meta().Actions)
}

func TestRegistry_ListConnectors(t *testing.T) {
    r := NewRegistry()
    list := r.ListConnectors()
    if len(list) != 0 {
        t.Errorf("empty registry should return 0 connectors, got %d", len(list))
    }

    r.Register(newMockConnector("c1", nil))
    r.Register(newMockConnector("c2", nil))
    list = r.ListConnectors()
    if len(list) != 2 {
        t.Errorf("ListConnectors = %d items, want 2", len(list))
    }
    keys := map[string]bool{}
    for _, m := range list {
        keys[m.ConnectorKey] = true
    }
    if !keys["c1"] || !keys["c2"] {
        t.Errorf("ListConnectors keys = %v, want c1 and c2", keys)
    }
    t.Logf("ListConnectors: count=%d keys=%v", len(list), keys)
}

func TestRegistry_CreateConnection_Valid(t *testing.T) {
    r := NewRegistry()
    r.Register(newMockConnector("quickbooks", []ActionDef{{ActionKey: "a1"}}))

    conn := &Connection{
        ID:           "conn-1",
        ConnectorKey: "quickbooks",
        AuthType:     AuthTypeOAuth2,
    }
    if err := r.CreateConnection(conn); err != nil {
        t.Fatalf("CreateConnection failed: %v", err)
    }
    if conn.CreatedAt.IsZero() {
        t.Error("CreatedAt should be set")
    }
    if conn.UpdatedAt.IsZero() {
        t.Error("UpdatedAt should be set")
    }
    if conn.Status != "active" {
        t.Errorf("Status = %q, want %q", conn.Status, "active")
    }
    t.Logf("CreateConnection: id=%s status=%s created=%v", conn.ID, conn.Status, conn.CreatedAt)
}

func TestRegistry_CreateConnection_DefaultStatus(t *testing.T) {
    r := NewRegistry()
    r.Register(newMockConnector("qb", nil))
    conn := &Connection{ID: "c1", ConnectorKey: "qb", Status: "pending"}
    if err := r.CreateConnection(conn); err != nil {
        t.Fatalf("CreateConnection: %v", err)
    }
    if conn.Status != "pending" {
        t.Errorf("Status = %q, want %q (should not overwrite)", conn.Status, "pending")
    }
    t.Logf("CreateConnection with status: status=%s", conn.Status)
}

func TestRegistry_CreateConnection_NoID(t *testing.T) {
    r := NewRegistry()
    r.Register(newMockConnector("qb", nil))
    conn := &Connection{ConnectorKey: "qb"}
    err := r.CreateConnection(conn)
    if err == nil {
        t.Fatal("should fail with empty ID")
    }
    if !strings.Contains(err.Error(), "connection id is required") {
        t.Errorf("error = %q, want 'connection id is required'", err.Error())
    }
    t.Logf("CreateConnection no ID: error=%v", err)
}

func TestRegistry_CreateConnection_ConnectorNotFound(t *testing.T) {
    r := NewRegistry()
    conn := &Connection{ID: "c1", ConnectorKey: "nonexistent"}
    err := r.CreateConnection(conn)
    if err == nil {
        t.Fatal("should fail when connector not found")
    }
    if !strings.Contains(err.Error(), "connector") {
        t.Errorf("error = %q, should mention connector", err.Error())
    }
    t.Logf("CreateConnection bad connector: error=%v", err)
}

func TestRegistry_CreateConnection_Duplicate(t *testing.T) {
    r := NewRegistry()
    r.Register(newMockConnector("qb", nil))
    conn1 := &Connection{ID: "c1", ConnectorKey: "qb"}
    if err := r.CreateConnection(conn1); err != nil {
        t.Fatalf("first CreateConnection: %v", err)
    }
    conn2 := &Connection{ID: "c1", ConnectorKey: "qb"}
    err := r.CreateConnection(conn2)
    if err == nil {
        t.Fatal("duplicate connection should fail")
    }
    if !strings.Contains(err.Error(), "already exists") {
        t.Errorf("error = %q, want 'already exists'", err.Error())
    }
    t.Logf("CreateConnection duplicate: error=%v", err)
}

func TestRegistry_GetConnection_Found(t *testing.T) {
    r := NewRegistry()
    r.Register(newMockConnector("qb", nil))
    conn := &Connection{ID: "c1", ConnectorKey: "qb"}
    _ = r.CreateConnection(conn)

    got, err := r.GetConnection("c1")
    if err != nil {
        t.Fatalf("GetConnection: %v", err)
    }
    if got.ID != "c1" {
        t.Errorf("ID = %q, want %q", got.ID, "c1")
    }
    t.Logf("GetConnection: id=%s connector=%s", got.ID, got.ConnectorKey)
}

func TestRegistry_GetConnection_NotFound(t *testing.T) {
    r := NewRegistry()
    _, err := r.GetConnection("nope")
    if err == nil {
        t.Fatal("should fail for nonexistent connection")
    }
    if !strings.Contains(err.Error(), "not found") {
        t.Errorf("error = %q, want 'not found'", err.Error())
    }
    t.Logf("GetConnection not found: error=%v", err)
}

func TestRegistry_ListConnections(t *testing.T) {
    r := NewRegistry()
    r.Register(newMockConnector("qb", nil))
    list := r.ListConnections()
    if len(list) != 0 {
        t.Errorf("empty connections = %d, want 0", len(list))
    }

    _ = r.CreateConnection(&Connection{ID: "c1", ConnectorKey: "qb"})
    _ = r.CreateConnection(&Connection{ID: "c2", ConnectorKey: "qb"})
    list = r.ListConnections()
    if len(list) != 2 {
        t.Errorf("ListConnections = %d, want 2", len(list))
    }
    t.Logf("ListConnections: count=%d", len(list))
}

func TestRegistry_DeleteConnection_Found(t *testing.T) {
    r := NewRegistry()
    r.Register(newMockConnector("qb", nil))
    _ = r.CreateConnection(&Connection{ID: "c1", ConnectorKey: "qb"})

    if err := r.DeleteConnection("c1"); err != nil {
        t.Fatalf("DeleteConnection: %v", err)
    }
    _, err := r.GetConnection("c1")
    if err == nil {
        t.Error("connection should be deleted")
    }
    t.Logf("DeleteConnection: deleted c1, subsequent GetConnection error=%v", err)
}

func TestRegistry_DeleteConnection_NotFound(t *testing.T) {
    r := NewRegistry()
    err := r.DeleteConnection("nope")
    if err == nil {
        t.Fatal("should fail for nonexistent connection")
    }
    if !strings.Contains(err.Error(), "not found") {
        t.Errorf("error = %q, want 'not found'", err.Error())
    }
    t.Logf("DeleteConnection not found: error=%v", err)
}

func TestRegistry_RefreshConnection_Success(t *testing.T) {
    r := NewRegistry()
    mc := newMockConnector("qb", nil)
    r.Register(mc)
    _ = r.CreateConnection(&Connection{ID: "c1", ConnectorKey: "qb"})

    if err := r.RefreshConnection(context.Background(), "c1"); err != nil {
        t.Fatalf("RefreshConnection: %v", err)
    }
    got, _ := r.GetConnection("c1")
    if got.UpdatedAt.IsZero() {
        t.Error("UpdatedAt should be refreshed")
    }
    t.Logf("RefreshConnection success: updatedAt=%v", got.UpdatedAt)
}

func TestRegistry_RefreshConnection_NotFound(t *testing.T) {
    r := NewRegistry()
    err := r.RefreshConnection(context.Background(), "nope")
    if err == nil {
        t.Fatal("should fail for nonexistent connection")
    }
    t.Logf("RefreshConnection not found: error=%v", err)
}

func TestRegistry_RefreshConnection_RefreshError(t *testing.T) {
    r := NewRegistry()
    mc := newMockConnector("qb", nil)
    mc.refreshErr = errors.New("refresh failed")
    r.Register(mc)
    _ = r.CreateConnection(&Connection{ID: "c1", ConnectorKey: "qb"})

    err := r.RefreshConnection(context.Background(), "c1")
    if err == nil {
        t.Fatal("should fail when RefreshAuth returns error")
    }
    if !strings.Contains(err.Error(), "refresh failed") {
        t.Errorf("error = %q, want 'refresh failed'", err.Error())
    }
    t.Logf("RefreshConnection error: %v", err)
}

func TestRegistry_RefreshConnection_ConnectorGone(t *testing.T) {
    r := NewRegistry()
    r.Register(newMockConnector("qb", nil))
    _ = r.CreateConnection(&Connection{ID: "c1", ConnectorKey: "qb"})

    // Simulate connector being removed after connection creation
    r.mu.Lock()
    delete(r.connectors, "qb")
    r.mu.Unlock()

    err := r.RefreshConnection(context.Background(), "c1")
    if err == nil {
        t.Fatal("should fail when connector not found")
    }
    if !strings.Contains(err.Error(), "connector") {
        t.Errorf("error = %q, should mention connector", err.Error())
    }
    t.Logf("RefreshConnection connector gone: error=%v", err)
}

func TestRegistry_ExecuteAction_ConnectorNotFound(t *testing.T) {
    r := NewRegistry()
    req := &ActionRequest{ConnectorKey: "nonexistent", ActionKey: "act1", ConnectionID: "c1"}
    resp, entry := r.ExecuteAction(context.Background(), req)
    if resp.Success {
        t.Error("should not succeed when connector not found")
    }
    if resp.Code != ErrNotFound {
        t.Errorf("Code = %d, want %d", resp.Code, ErrNotFound)
    }
    if entry != nil {
        t.Error("AuditEntry should be nil when connector not found")
    }
    t.Logf("ExecuteAction connector not found: success=%v code=%d entry=%v", resp.Success, resp.Code, entry)
}

func TestRegistry_ExecuteAction_ConnectionNotFound(t *testing.T) {
    r := NewRegistry()
    r.Register(newMockConnector("qb", []ActionDef{{ActionKey: "act1", Permission: PermissionRead}}))
    req := &ActionRequest{ConnectorKey: "qb", ActionKey: "act1", ConnectionID: "nope"}
    resp, entry := r.ExecuteAction(context.Background(), req)
    if resp.Success {
        t.Error("should not succeed when connection not found")
    }
    if resp.Code != ErrNotFound {
        t.Errorf("Code = %d, want %d", resp.Code, ErrNotFound)
    }
    if entry != nil {
        t.Error("AuditEntry should be nil when connection not found")
    }
    t.Logf("ExecuteAction connection not found: success=%v code=%d", resp.Success, resp.Code)
}

func TestRegistry_ExecuteAction_ConnectionMismatch(t *testing.T) {
    r := NewRegistry()
    r.Register(newMockConnector("qb", []ActionDef{{ActionKey: "act1", Permission: PermissionRead}}))
    r.Register(newMockConnector("other", []ActionDef{{ActionKey: "act1", Permission: PermissionRead}}))
    _ = r.CreateConnection(&Connection{ID: "c1", ConnectorKey: "other"})

    req := &ActionRequest{ConnectorKey: "qb", ActionKey: "act1", ConnectionID: "c1"}
    resp, entry := r.ExecuteAction(context.Background(), req)
    if resp.Success {
        t.Error("should not succeed when connection mismatch")
    }
    if resp.Code != ErrValidation {
        t.Errorf("Code = %d, want %d", resp.Code, ErrValidation)
    }
    if !strings.Contains(resp.Message, "does not match") {
        t.Errorf("Message = %q, should mention mismatch", resp.Message)
    }
    if entry != nil {
        t.Error("AuditEntry should be nil on mismatch")
    }
    t.Logf("ExecuteAction mismatch: code=%d msg=%q", resp.Code, resp.Message)
}

func TestRegistry_ExecuteAction_ActionNotFound(t *testing.T) {
    r := NewRegistry()
    r.Register(newMockConnector("qb", []ActionDef{{ActionKey: "act1", Permission: PermissionRead}}))
    _ = r.CreateConnection(&Connection{ID: "c1", ConnectorKey: "qb"})

    req := &ActionRequest{ConnectorKey: "qb", ActionKey: "nonexistent", ConnectionID: "c1"}
    resp, entry := r.ExecuteAction(context.Background(), req)
    if resp.Success {
        t.Error("should not succeed when action not found")
    }
    if resp.Code != ErrNotFound {
        t.Errorf("Code = %d, want %d", resp.Code, ErrNotFound)
    }
    if entry != nil {
        t.Error("AuditEntry should be nil when action not found")
    }
    t.Logf("ExecuteAction action not found: code=%d msg=%q", resp.Code, resp.Message)
}

func TestRegistry_ExecuteAction_Success(t *testing.T) {
    r := NewRegistry()
    mc := newMockConnector("qb", []ActionDef{
        {ActionKey: "list_items", Permission: PermissionRead},
    })
    r.Register(mc)
    _ = r.CreateConnection(&Connection{ID: "c1", ConnectorKey: "qb"})

    req := &ActionRequest{
        ConnectorKey: "qb",
        ActionKey:    "list_items",
        ConnectionID: "c1",
        Params:       map[string]interface{}{"page": 1},
    }
    resp, entry := r.ExecuteAction(context.Background(), req)
    if !resp.Success {
        t.Errorf("should succeed, got code=%d msg=%q", resp.Code, resp.Message)
    }
    if entry == nil {
        t.Fatal("AuditEntry should not be nil on success")
    }
    if entry.ConnectorKey != "qb" {
        t.Errorf("entry.ConnectorKey = %q, want %q", entry.ConnectorKey, "qb")
    }
    if entry.ActionKey != "list_items" {
        t.Errorf("entry.ActionKey = %q, want %q", entry.ActionKey, "list_items")
    }
    if !entry.Success {
        t.Error("entry.Success should be true")
    }
    if entry.DurationMS < 0 {
        t.Errorf("DurationMS = %d, want >= 0", entry.DurationMS)
    }
    // Read action should not have InputSummary
    if entry.InputSummary != "" {
        t.Errorf("read action InputSummary = %q, want empty", entry.InputSummary)
    }
    t.Logf("ExecuteAction success: code=%d entry=%+v", resp.Code, entry)
}

func TestRegistry_ExecuteAction_WriteActionInputSummary(t *testing.T) {
    r := NewRegistry()
    mc := newMockConnector("qb", []ActionDef{
        {ActionKey: "create_item", Permission: PermissionWrite},
    })
    r.Register(mc)
    _ = r.CreateConnection(&Connection{ID: "c1", ConnectorKey: "qb"})

    req := &ActionRequest{
        ConnectorKey: "qb",
        ActionKey:    "create_item",
        ConnectionID: "c1",
        Params:       map[string]interface{}{"name": "test"},
    }
    resp, entry := r.ExecuteAction(context.Background(), req)
    if !resp.Success {
        t.Errorf("should succeed, got code=%d", resp.Code)
    }
    if entry.InputSummary == "" {
        t.Error("write action should have InputSummary")
    }
    if !strings.Contains(entry.InputSummary, "name=test") {
        t.Errorf("InputSummary = %q, should contain name=test", entry.InputSummary)
    }
    t.Logf("Write action InputSummary: %q", entry.InputSummary)
}

func TestRegistry_ExecuteAction_WriteActionNilParams(t *testing.T) {
    r := NewRegistry()
    mc := newMockConnector("qb", []ActionDef{
        {ActionKey: "create_item", Permission: PermissionWrite},
    })
    r.Register(mc)
    _ = r.CreateConnection(&Connection{ID: "c1", ConnectorKey: "qb"})

    req := &ActionRequest{
        ConnectorKey: "qb",
        ActionKey:    "create_item",
        ConnectionID: "c1",
        Params:       nil,
    }
    resp, entry := r.ExecuteAction(context.Background(), req)
    if !resp.Success {
        t.Errorf("should succeed, got code=%d", resp.Code)
    }
    if entry.InputSummary != "" {
        t.Errorf("nil params write action InputSummary = %q, want empty", entry.InputSummary)
    }
    t.Logf("Write action nil params InputSummary: %q", entry.InputSummary)
}

func TestRegistry_ExecuteAction_ErrorFromConnector(t *testing.T) {
    r := NewRegistry()
    mc := newMockConnector("qb", []ActionDef{
        {ActionKey: "act1", Permission: PermissionRead},
    })
    mc.executeFunc = func(ctx context.Context, conn *Connection, actionKey string, params map[string]interface{}, testMode bool) (*ActionResponse, error) {
        return nil, errors.New("connector exploded")
    }
    r.Register(mc)
    _ = r.CreateConnection(&Connection{ID: "c1", ConnectorKey: "qb"})

    req := &ActionRequest{ConnectorKey: "qb", ActionKey: "act1", ConnectionID: "c1"}
    resp, entry := r.ExecuteAction(context.Background(), req)
    if resp.Success {
        t.Error("should not succeed when connector returns error")
    }
    if resp.Code != ErrTimeout {
        t.Errorf("Code = %d, want %d (ErrTimeout)", resp.Code, ErrTimeout)
    }
    if !strings.Contains(resp.Message, "connector exploded") {
        t.Errorf("Message = %q, should contain error", resp.Message)
    }
    if entry == nil {
        t.Fatal("AuditEntry should not be nil even on error")
    }
    if entry.Success {
        t.Error("entry.Success should be false")
    }
    if entry.ErrorCode != ErrTimeout {
        t.Errorf("entry.ErrorCode = %d, want %d", entry.ErrorCode, ErrTimeout)
    }
    t.Logf("ExecuteAction connector error: code=%d msg=%q entry=%+v", resp.Code, resp.Message, entry)
}

func TestRegistry_ExecuteAction_ResponseError(t *testing.T) {
    r := NewRegistry()
    mc := newMockConnector("qb", []ActionDef{
        {ActionKey: "act1", Permission: PermissionRead},
    })
    mc.executeFunc = func(ctx context.Context, conn *Connection, actionKey string, params map[string]interface{}, testMode bool) (*ActionResponse, error) {
        return ErrorWithCode(ErrValidation, "bad input"), nil
    }
    r.Register(mc)
    _ = r.CreateConnection(&Connection{ID: "c1", ConnectorKey: "qb"})

    req := &ActionRequest{ConnectorKey: "qb", ActionKey: "act1", ConnectionID: "c1"}
    resp, entry := r.ExecuteAction(context.Background(), req)
    if resp.Success {
        t.Error("should not succeed")
    }
    if resp.Code != ErrValidation {
        t.Errorf("Code = %d, want %d", resp.Code, ErrValidation)
    }
    if entry.ErrorCode != ErrValidation {
        t.Errorf("entry.ErrorCode = %d, want %d", entry.ErrorCode, ErrValidation)
    }
    t.Logf("ExecuteAction response error: code=%d entry.ErrorCode=%d", resp.Code, entry.ErrorCode)
}

func TestRegistry_AuditLogging(t *testing.T) {
    r := NewRegistry()
    mc := newMockConnector("qb", []ActionDef{
        {ActionKey: "act1", Permission: PermissionRead},
    })
    r.Register(mc)
    _ = r.CreateConnection(&Connection{ID: "c1", ConnectorKey: "qb"})

    for i := 0; i < 3; i++ {
        req := &ActionRequest{ConnectorKey: "qb", ActionKey: "act1", ConnectionID: "c1"}
        r.ExecuteAction(context.Background(), req)
    }
    log := r.AuditLog(0)
    if len(log) != 3 {
        t.Errorf("AuditLog = %d entries, want 3", len(log))
    }
    t.Logf("AuditLog entries: %d", len(log))
}

func TestRegistry_AuditLogTrimming(t *testing.T) {
    r := NewRegistry()
    r.auditMaxLen = 5
    mc := newMockConnector("qb", []ActionDef{
        {ActionKey: "act1", Permission: PermissionRead},
    })
    r.Register(mc)
    _ = r.CreateConnection(&Connection{ID: "c1", ConnectorKey: "qb"})

    for i := 0; i < 10; i++ {
        req := &ActionRequest{ConnectorKey: "qb", ActionKey: "act1", ConnectionID: "c1"}
        r.ExecuteAction(context.Background(), req)
    }
    log := r.AuditLog(0)
    if len(log) != 5 {
        t.Errorf("AuditLog = %d entries, want 5 (trimmed to auditMaxLen)", len(log))
    }
    // The most recent entries should be kept
    t.Logf("AuditLog trimmed: count=%d", len(log))
}

func TestRegistry_AuditLog_WithLimit(t *testing.T) {
    r := NewRegistry()
    mc := newMockConnector("qb", []ActionDef{
        {ActionKey: "act1", Permission: PermissionRead},
    })
    r.Register(mc)
    _ = r.CreateConnection(&Connection{ID: "c1", ConnectorKey: "qb"})

    for i := 0; i < 10; i++ {
        req := &ActionRequest{ConnectorKey: "qb", ActionKey: "act1", ConnectionID: "c1"}
        r.ExecuteAction(context.Background(), req)
    }
    log := r.AuditLog(3)
    if len(log) != 3 {
        t.Errorf("AuditLog(3) = %d entries, want 3", len(log))
    }
    // Should return the last 3 entries
    t.Logf("AuditLog(3): count=%d", len(log))
}

func TestRegistry_AuditLog_LimitExceedsSize(t *testing.T) {
    r := NewRegistry()
    mc := newMockConnector("qb", []ActionDef{
        {ActionKey: "act1", Permission: PermissionRead},
    })
    r.Register(mc)
    _ = r.CreateConnection(&Connection{ID: "c1", ConnectorKey: "qb"})

    for i := 0; i < 3; i++ {
        req := &ActionRequest{ConnectorKey: "qb", ActionKey: "act1", ConnectionID: "c1"}
        r.ExecuteAction(context.Background(), req)
    }
    log := r.AuditLog(100)
    if len(log) != 3 {
        t.Errorf("AuditLog(100) = %d entries, want 3 (all entries)", len(log))
    }
    t.Logf("AuditLog(100): count=%d", len(log))
}

func TestRegistry_AuditLog_NegativeLimit(t *testing.T) {
    r := NewRegistry()
    mc := newMockConnector("qb", []ActionDef{
        {ActionKey: "act1", Permission: PermissionRead},
    })
    r.Register(mc)
    _ = r.CreateConnection(&Connection{ID: "c1", ConnectorKey: "qb"})

    r.ExecuteAction(context.Background(), &ActionRequest{ConnectorKey: "qb", ActionKey: "act1", ConnectionID: "c1"})
    log := r.AuditLog(-1)
    if len(log) != 1 {
        t.Errorf("AuditLog(-1) = %d entries, want 1 (treat as default)", len(log))
    }
    t.Logf("AuditLog(-1): count=%d", len(log))
}

func TestRegistry_AuditLog_Empty(t *testing.T) {
    r := NewRegistry()
    log := r.AuditLog(0)
    if len(log) != 0 {
        t.Errorf("empty AuditLog = %d entries, want 0", len(log))
    }
    t.Logf("AuditLog empty: count=%d", len(log))
}

func TestSummarizeParams(t *testing.T) {
    params := map[string]interface{}{
        "name":   "test",
        "count":  42,
        "active": true,
    }
    s := summarizeParams(params)
    if s == "" {
        t.Error("summarizeParams returned empty string")
    }
    if !strings.Contains(s, "name=test") {
        t.Errorf("summary = %q, should contain name=test", s)
    }
    if !strings.Contains(s, "count=42") {
        t.Errorf("summary = %q, should contain count=42", s)
    }
    t.Logf("summarizeParams: %q", s)
}

func TestSummarizeParams_LongValue(t *testing.T) {
    longVal := strings.Repeat("x", 200)
    params := map[string]interface{}{
        "data": longVal,
    }
    s := summarizeParams(params)
    if !strings.Contains(s, "...") {
        t.Errorf("long value should be truncated, got: %q", s)
    }
    t.Logf("summarizeParams long value: len=%d", len(s))
}

func TestSummarizeParams_ManyKeys(t *testing.T) {
    params := map[string]interface{}{}
    for i := 0; i < 30; i++ {
        params[fmt.Sprintf("key_%02d", i)] = strings.Repeat("v", 30)
    }
    s := summarizeParams(params)
    if len(s) > 503 {
        t.Errorf("summary too long: %d chars", len(s))
    }
    t.Logf("summarizeParams many keys: len=%d", len(s))
}

func TestRegistry_ExecuteAction_TestMode(t *testing.T) {
    r := NewRegistry()
    mc := newMockConnector("qb", []ActionDef{
        {ActionKey: "act1", Permission: PermissionRead},
    })
    var receivedTestMode bool
    mc.executeFunc = func(ctx context.Context, conn *Connection, actionKey string, params map[string]interface{}, testMode bool) (*ActionResponse, error) {
        receivedTestMode = testMode
        return &ActionResponse{Success: true, Code: 0, Data: map[string]interface{}{"test": testMode}}, nil
    }
    r.Register(mc)
    _ = r.CreateConnection(&Connection{ID: "c1", ConnectorKey: "qb"})

    req := &ActionRequest{ConnectorKey: "qb", ActionKey: "act1", ConnectionID: "c1", TestMode: true}
    resp, _ := r.ExecuteAction(context.Background(), req)
    if !receivedTestMode {
        t.Error("testMode should be true")
    }
    if !resp.Success {
        t.Error("should succeed")
    }
    t.Logf("ExecuteAction testMode: received=%v", receivedTestMode)
}

func TestRegistry_ExecuteAction_AuditEntryFields(t *testing.T) {
    r := NewRegistry()
    mc := newMockConnector("qb", []ActionDef{
        {ActionKey: "create_thing", Permission: PermissionWrite},
    })
    r.Register(mc)
    _ = r.CreateConnection(&Connection{ID: "c1", ConnectorKey: "qb"})

    req := &ActionRequest{
        ConnectorKey: "qb",
        ActionKey:    "create_thing",
        ConnectionID: "c1",
        Params:       map[string]interface{}{"foo": "bar"},
    }
    _, entry := r.ExecuteAction(context.Background(), req)
    if entry.ConnectionID != "c1" {
        t.Errorf("ConnectionID = %q, want %q", entry.ConnectionID, "c1")
    }
    if entry.ConnectorKey != "qb" {
        t.Errorf("ConnectorKey = %q, want %q", entry.ConnectorKey, "qb")
    }
    if entry.ActionKey != "create_thing" {
        t.Errorf("ActionKey = %q, want %q", entry.ActionKey, "create_thing")
    }
    if entry.Permission != "write" {
        t.Errorf("Permission = %q, want %q", entry.Permission, "write")
    }
    if entry.Timestamp.IsZero() {
        t.Error("Timestamp should be set")
    }
    t.Logf("AuditEntry fields: connID=%s connector=%s action=%s perm=%s ts=%v dur=%d",
        entry.ConnectionID, entry.ConnectorKey, entry.ActionKey, entry.Permission, entry.Timestamp, entry.DurationMS)
}
