package connector

import (
    "context"
    "fmt"
    "log/slog"
    "strings"
    "sync"
    "time"
)

type Registry struct {
    mu          sync.RWMutex
    connectors  map[string]Connector
    connections map[string]*Connection
    auditLog    []AuditEntry
    auditMaxLen int
}

func NewRegistry() *Registry {
    return &Registry{
        connectors:  make(map[string]Connector),
        connections: make(map[string]*Connection),
        auditLog:    make([]AuditEntry, 0),
        auditMaxLen: 10000,
    }
}

func (r *Registry) Register(c Connector) {
    r.mu.Lock()
    defer r.mu.Unlock()
    meta := c.Meta()
    r.connectors[meta.ConnectorKey] = c
    slog.Info("connector registered", "key", meta.ConnectorKey, "display", meta.DisplayName, "actions", len(meta.Actions))
}

func (r *Registry) Get(key string) (Connector, bool) {
    r.mu.RLock()
    defer r.mu.RUnlock()
    c, ok := r.connectors[key]
    return c, ok
}

func (r *Registry) ListConnectors() []ConnectorMeta {
    r.mu.RLock()
    defer r.mu.RUnlock()
    result := make([]ConnectorMeta, 0, len(r.connectors))
    for _, c := range r.connectors {
        result = append(result, *c.Meta())
    }
    return result
}

func (r *Registry) CreateConnection(conn *Connection) error {
    if conn.ID == "" {
        return fmt.Errorf("connection id is required")
    }
    if _, ok := r.Get(conn.ConnectorKey); !ok {
        return fmt.Errorf("connector %q not found", conn.ConnectorKey)
    }
    r.mu.Lock()
    defer r.mu.Unlock()
    if _, exists := r.connections[conn.ID]; exists {
        return fmt.Errorf("connection %q already exists", conn.ID)
    }
    conn.CreatedAt = time.Now().UTC()
    conn.UpdatedAt = time.Now().UTC()
    if conn.Status == "" {
        conn.Status = "active"
    }
    r.connections[conn.ID] = conn
    slog.Info("connection created", "id", conn.ID, "connector", conn.ConnectorKey)
    return nil
}

func (r *Registry) GetConnection(id string) (*Connection, error) {
    r.mu.RLock()
    defer r.mu.RUnlock()
    conn, ok := r.connections[id]
    if !ok {
        return nil, fmt.Errorf("connection %q not found", id)
    }
    return conn, nil
}

func (r *Registry) ListConnections() []*Connection {
    r.mu.RLock()
    defer r.mu.RUnlock()
    result := make([]*Connection, 0, len(r.connections))
    for _, c := range r.connections {
        result = append(result, c)
    }
    return result
}

func (r *Registry) DeleteConnection(id string) error {
    r.mu.Lock()
    defer r.mu.Unlock()
    if _, ok := r.connections[id]; !ok {
        return fmt.Errorf("connection %q not found", id)
    }
    delete(r.connections, id)
    slog.Info("connection deleted", "id", id)
    return nil
}

func (r *Registry) RefreshConnection(ctx context.Context, id string) error {
    conn, err := r.GetConnection(id)
    if err != nil {
        return err
    }
    c, ok := r.Get(conn.ConnectorKey)
    if !ok {
        return fmt.Errorf("connector %q not found", conn.ConnectorKey)
    }
    if err := c.RefreshAuth(ctx, conn); err != nil {
        slog.Error("connection refresh failed", "id", id, "error", err)
        return err
    }
    r.mu.Lock()
    conn.UpdatedAt = time.Now().UTC()
    r.mu.Unlock()
    slog.Info("connection refreshed", "id", id)
    return nil
}

func (r *Registry) ExecuteAction(ctx context.Context, req *ActionRequest) (*ActionResponse, *AuditEntry) {
    start := time.Now()
    c, ok := r.Get(req.ConnectorKey)
    if !ok {
        return ErrorWithCode(ErrNotFound, fmt.Sprintf("connector %q not found", req.ConnectorKey)), nil
    }
    conn, err := r.GetConnection(req.ConnectionID)
    if err != nil {
        return ErrorWithCode(ErrNotFound, err.Error()), nil
    }
    if conn.ConnectorKey != req.ConnectorKey {
        return ErrorWithCode(ErrValidation, "connection does not match connector"), nil
    }
    meta := c.Meta()
    var perm ActionPermission
    found := false
    for _, a := range meta.Actions {
        if a.ActionKey == req.ActionKey {
            perm = a.Permission
            found = true
            break
        }
    }
    if !found {
        return ErrorWithCode(ErrNotFound, fmt.Sprintf("action %q not found", req.ActionKey)), nil
    }
    resp, err := c.ExecuteAction(ctx, conn, req.ActionKey, req.Params, req.TestMode)
    duration := time.Since(start)
    entry := AuditEntry{
        Timestamp:    time.Now().UTC(),
        ConnectionID: req.ConnectionID,
        ConnectorKey: req.ConnectorKey,
        ActionKey:    req.ActionKey,
        Permission:   string(perm),
        DurationMS:   duration.Milliseconds(),
    }
    if err != nil {
        resp = ErrorWithCode(ErrTimeout, err.Error())
        entry.Success = false
        entry.ErrorCode = resp.Code
    } else {
        entry.Success = resp.Success
        entry.ErrorCode = resp.Code
    }
    if perm == PermissionWrite && req.Params != nil {
        entry.InputSummary = summarizeParams(req.Params)
    }
    r.appendAudit(entry)
    return resp, &entry
}

func (r *Registry) appendAudit(entry AuditEntry) {
    r.mu.Lock()
    defer r.mu.Unlock()
    r.auditLog = append(r.auditLog, entry)
    if len(r.auditLog) > r.auditMaxLen {
        r.auditLog = r.auditLog[len(r.auditLog)-r.auditMaxLen:]
    }
}

func (r *Registry) AuditLog(limit int) []AuditEntry {
    r.mu.RLock()
    defer r.mu.RUnlock()
    if limit <= 0 || limit > len(r.auditLog) {
        limit = len(r.auditLog)
    }
    start := len(r.auditLog) - limit
    result := make([]AuditEntry, limit)
    copy(result, r.auditLog[start:])
    return result
}

func summarizeParams(params map[string]interface{}) string {
    parts := make([]string, 0, len(params))
    for k, v := range params {
        s := fmt.Sprintf("%s=%v", k, v)
        if len(s) > 100 {
            s = s[:100] + "..."
        }
        parts = append(parts, s)
    }
    result := strings.Join(parts, ", ")
    if len(result) > 500 {
        result = result[:500] + "..."
    }
    return result
}
