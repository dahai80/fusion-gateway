package server

import (
    "crypto/rand"
    "encoding/hex"
    "encoding/json"
    "fmt"
    "log/slog"
    "net/http"
    "strings"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/connector"
)

func (s *Server) handleConnectorList(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodGet {
        http.Error(w, `{"error":{"message":"Method not allowed"}}`, http.StatusMethodNotAllowed)
        return
    }
    connectors := s.connectorRegistry.ListConnectors()
    writeJSON(w, http.StatusOK, map[string]interface{}{
        "connectors": connectors,
    })
}

func (s *Server) handleConnectorTest(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        http.Error(w, `{"error":{"message":"Method not allowed"}}`, http.StatusMethodNotAllowed)
        return
    }
    var req connector.ActionRequest
    if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAdminBodySize)).Decode(&req); err != nil {
        http.Error(w, `{"error":{"message":"Invalid JSON"}}`, http.StatusBadRequest)
        return
    }
    req.TestMode = true
    resp, _ := s.connectorRegistry.ExecuteAction(r.Context(), &req)
    writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleConnectorAction(w http.ResponseWriter, r *http.Request) {
    // Path: /gateway/v1/connector/{connectorKey}/action/{actionKey}
    if r.Method != http.MethodPost {
        http.Error(w, `{"error":{"message":"Method not allowed"}}`, http.StatusMethodNotAllowed)
        return
    }
    path := strings.TrimPrefix(r.URL.Path, "/gateway/v1/connector/")
    parts := strings.SplitN(path, "/", 3)
    if len(parts) < 3 || parts[1] != "action" {
        http.Error(w, `{"error":{"message":"Invalid path, expected /gateway/v1/connector/{key}/action/{action}"}}`, http.StatusBadRequest)
        return
    }
    connectorKey := parts[0]
    actionKey := parts[2]

    var body struct {
        Params       map[string]interface{} `json:"params"`
        ConnectionID string                 `json:"connectionId"`
    }
    if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAdminBodySize)).Decode(&body); err != nil {
        http.Error(w, `{"error":{"message":"Invalid JSON"}}`, http.StatusBadRequest)
        return
    }

    // Also check header for connection ID
    connectionID := body.ConnectionID
    if v := r.Header.Get("X-Fusion-Connection-Id"); v != "" {
        connectionID = v
    }

    req := &connector.ActionRequest{
        ConnectorKey: connectorKey,
        ActionKey:    actionKey,
        ConnectionID: connectionID,
        Params:       body.Params,
    }
    resp, _ := s.connectorRegistry.ExecuteAction(r.Context(), req)
    writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleConnectionList(w http.ResponseWriter, r *http.Request) {
    switch r.Method {
    case http.MethodGet:
        connections := s.connectorRegistry.ListConnections()
        writeJSON(w, http.StatusOK, map[string]interface{}{
            "connections": connections,
        })
    case http.MethodPost:
        var conn connector.Connection
        if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAdminBodySize)).Decode(&conn); err != nil {
            http.Error(w, `{"error":{"message":"Invalid JSON"}}`, http.StatusBadRequest)
            return
        }
        if err := s.connectorRegistry.CreateConnection(&conn); err != nil {
            writeJSON(w, http.StatusConflict, map[string]interface{}{
                "success": false,
                "code":    connector.ErrValidation,
                "message": err.Error(),
            })
            return
        }
        writeJSON(w, http.StatusCreated, conn)
    default:
        http.Error(w, `{"error":{"message":"Method not allowed"}}`, http.StatusMethodNotAllowed)
    }
}

func (s *Server) handleConnectionCRUD(w http.ResponseWriter, r *http.Request) {
    id := strings.TrimPrefix(r.URL.Path, "/gateway/v1/connection/")
    if id == "" {
        http.Error(w, `{"error":{"message":"Connection ID required"}}`, http.StatusBadRequest)
        return
    }
    switch r.Method {
    case http.MethodGet:
        conn, err := s.connectorRegistry.GetConnection(id)
        if err != nil {
            writeJSON(w, http.StatusNotFound, map[string]interface{}{
                "success": false,
                "code":    connector.ErrNotFound,
                "message": err.Error(),
            })
            return
        }
        writeJSON(w, http.StatusOK, conn)
    case http.MethodDelete:
        if err := s.connectorRegistry.DeleteConnection(id); err != nil {
            writeJSON(w, http.StatusNotFound, map[string]interface{}{
                "success": false,
                "code":    connector.ErrNotFound,
                "message": err.Error(),
            })
            return
        }
        w.WriteHeader(http.StatusNoContent)
    case http.MethodPost:
        if strings.HasSuffix(r.URL.Path, "/refresh") {
            if err := s.connectorRegistry.RefreshConnection(r.Context(), id); err != nil {
                writeJSON(w, http.StatusBadRequest, map[string]interface{}{
                    "success": false,
                    "code":    connector.ErrAuthExpired,
                    "message": err.Error(),
                })
                return
            }
            writeJSON(w, http.StatusOK, map[string]interface{}{
                "success": true,
                "code":    0,
                "message": "refreshed",
            })
            return
        }
        http.Error(w, `{"error":{"message":"Unknown action"}}`, http.StatusBadRequest)
    default:
        http.Error(w, `{"error":{"message":"Method not allowed"}}`, http.StatusMethodNotAllowed)
    }
}

func (s *Server) evictOAuth2States() {
    ticker := time.NewTicker(10 * time.Minute)
    defer ticker.Stop()
    for range ticker.C {
        s.oauth2StatesMu.Lock()
        now := time.Now()
        for state, entry := range s.oauth2States {
            if now.Sub(entry.issuedAt) > 10*time.Minute {
                delete(s.oauth2States, state)
            }
        }
        s.oauth2StatesMu.Unlock()
    }
}

func (s *Server) handleOAuth2Authorize(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        http.Error(w, `{"error":{"message":"Method not allowed"}}`, http.StatusMethodNotAllowed)
        return
    }
    var req struct {
        ConnectorKey string `json:"connectorKey"`
        State        string `json:"state"`
    }
    if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAdminBodySize)).Decode(&req); err != nil {
        http.Error(w, `{"error":{"message":"Invalid JSON"}}`, http.StatusBadRequest)
        return
    }
    if req.ConnectorKey == "" {
        http.Error(w, `{"error":{"message":"connectorKey is required"}}`, http.StatusBadRequest)
        return
    }
    if req.State == "" {
        b := make([]byte, 16)
        if _, err := rand.Read(b); err != nil {
            slog.Error("generate state failed", "error", err)
            http.Error(w, `{"error":{"message":"internal error"}}`, http.StatusInternalServerError)
            return
        }
        req.State = hex.EncodeToString(b)
    }
    // Enforce length limit on client-supplied state to prevent unbounded map growth
    if len(req.State) > 64 {
        http.Error(w, `{"error":{"message":"state parameter too long"}}`, http.StatusBadRequest)
        return
    }
    s.oauth2StatesMu.Lock()
    s.oauth2States[req.State] = oauth2StateEntry{connectorKey: req.ConnectorKey, issuedAt: time.Now().UTC()}
    s.oauth2StatesMu.Unlock()
    authURL, err := s.connectorRegistry.OAuth2().AuthorizationURL(req.ConnectorKey, req.State)
    if err != nil {
        writeJSON(w, http.StatusBadRequest, map[string]interface{}{
            "success": false,
            "message": err.Error(),
        })
        return
    }
    writeJSON(w, http.StatusOK, map[string]interface{}{
        "authorization_url": authURL,
        "state":             req.State,
    })
}

func (s *Server) handleOAuth2Callback(w http.ResponseWriter, r *http.Request) {
    code := r.URL.Query().Get("code")
    state := r.URL.Query().Get("state")
    connectorKey := r.URL.Query().Get("connector_key")
    if code == "" || connectorKey == "" {
        http.Error(w, `{"error":{"message":"missing code or connector_key"}}`, http.StatusBadRequest)
        return
    }
    // CSRF: validate state matches a previously-issued one
    if state == "" {
        http.Error(w, `{"error":{"message":"missing state parameter"}}`, http.StatusBadRequest)
        return
    }
    s.oauth2StatesMu.Lock()
    entry, ok := s.oauth2States[state]
    if ok {
        delete(s.oauth2States, state)
    }
    s.oauth2StatesMu.Unlock()
    if !ok {
        http.Error(w, `{"error":{"message":"invalid or expired state parameter"}}`, http.StatusBadRequest)
        return
    }
    if time.Since(entry.issuedAt) > 10*time.Minute {
        http.Error(w, `{"error":{"message":"state parameter expired"}}`, http.StatusBadRequest)
        return
    }
    // B13: the state was issued for a specific connector_key during authorize.
    // Reject a callback whose connector_key does not match — this stops
    // cross-connector state replay (a state minted for connector A replayed
    // against connector B's callback to exfiltrate a token into the wrong one).
    if entry.connectorKey != connectorKey {
        slog.Warn("oauth2 callback: state/connector_key mismatch (cross-connector replay)",
            "expected_connector", entry.connectorKey, "callback_connector", connectorKey, "state", state)
        http.Error(w, `{"error":{"message":"state does not match connector_key"}}`, http.StatusBadRequest)
        return
    }

    slog.Info("oauth2 callback received", "connector", connectorKey, "state", state)
    tokenResp, err := s.connectorRegistry.OAuth2().ExchangeCode(r.Context(), connectorKey, code)
    if err != nil {
        writeJSON(w, http.StatusBadRequest, map[string]interface{}{
            "success": false,
            "message": err.Error(),
        })
        return
    }
    encAccess, err := s.connectorRegistry.OAuth2().EncryptToken(tokenResp.AccessToken)
    if err != nil {
        slog.Error("encrypt access token failed, aborting callback", "error", err)
        writeJSON(w, http.StatusInternalServerError, map[string]interface{}{
            "success": false,
            "message": "token encryption failed",
        })
        return
    }
    encRefresh, err := s.connectorRegistry.OAuth2().EncryptToken(tokenResp.RefreshToken)
    if err != nil {
        slog.Error("encrypt refresh token failed, aborting callback", "error", err)
        writeJSON(w, http.StatusInternalServerError, map[string]interface{}{
            "success": false,
            "message": "token encryption failed",
        })
        return
    }
    conn := &connector.Connection{
        ID:                     fmt.Sprintf("conn-%s-%d", connectorKey, time.Now().UnixNano()),
        ConnectorKey:           connectorKey,
        AuthType:               connector.AuthTypeOAuth2,
        Status:                 "active",
        EncryptedAccessToken:   encAccess,
        EncryptedRefreshToken:  encRefresh,
    }
    if tokenResp.ExpiresIn > 0 {
        expiry := time.Now().UTC().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)
        conn.TokenExpiry = &expiry
    }
    if err := s.connectorRegistry.CreateConnection(conn); err != nil {
        writeJSON(w, http.StatusInternalServerError, map[string]interface{}{
            "success": false,
            "message": err.Error(),
        })
        return
    }
    writeJSON(w, http.StatusOK, map[string]interface{}{
        "success":       true,
        "connection_id": conn.ID,
        "expires_in":    tokenResp.ExpiresIn,
    })
}
