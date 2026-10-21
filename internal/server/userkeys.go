package server

import (
    "crypto/rand"
    "crypto/sha256"
    "encoding/hex"
    "encoding/json"
    "log/slog"
    "net/http"
    "strings"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/store"
)

// #262 K2: user API keys — self-service gateway-issued credentials for
// external callers. Surface: /api/v1/keys (master-gated CRUD).
//
// Wire contract:
//   - Keys are fk-<48 hex chars> (24 random bytes); the SHA-256 hash is the
//     only persisted secret (A2 invariant: hashes never leave the store
//     serialization for list/get responses).
//   - Plaintext is returned exactly once, at create/rotate time.
//   - Rotate keeps the pre-rotation hash valid for auth.key_rotation_grace
//     (dual-active, same semantics as the K1 fm- service keys).
//   - Revoke is soft (Status="revoked") — keys are never hard-deleted
//     (preserve-API-keys policy); auth rejects any non-active status.
//   - Create requires a non-empty scope matrix; scopes are enforced on
//     /v1/* by middleware.ScopeEnforce.
//   - Optional tenant binds the key to a team (BindKeyToTeam side table),
//     which drives the X-Fusion-Tenant contract on the request path.

// userKeyResponse is the create/rotate payload (plaintext appears exactly
// once, here, and never again).
type userKeyResponse struct {
    Name      string    `json:"name"`
    KeyPrefix string    `json:"key_prefix"`
    RawKey    string    `json:"raw_key"`
    Scopes    []string  `json:"scopes"`
    Tenant    string    `json:"tenant,omitempty"`
    Status    string    `json:"status"`
    CreatedAt time.Time `json:"created_at"`
}

// userKeyListEntry is the list projection — no hashes, no plaintext.
type userKeyListEntry struct {
    Name        string     `json:"name"`
    KeyPrefix   string     `json:"key_prefix"`
    Status      string     `json:"status"`
    Scopes      []string   `json:"scopes,omitempty"`
    Tenant      string     `json:"tenant,omitempty"`
    QuotaLimit  float64    `json:"quota_limit"`
    QuotaUsed   float64    `json:"quota_used"`
    RPM         int        `json:"rpm"`
    TPM         int        `json:"tpm"`
    RotatedAt   *time.Time `json:"rotated_at,omitempty"`
    CreatedAt   time.Time  `json:"created_at"`
    UpdatedAt   time.Time  `json:"updated_at"`
}

type createUserKeyRequest struct {
    Name          string   `json:"name"`
    Scopes        []string `json:"scopes"`
    Tenant        string   `json:"tenant"`
    AllowedModels []string `json:"allowed_models"`
    QuotaLimit    float64  `json:"quota_limit"`
    RPM           int      `json:"rpm"`
    TPM           int      `json:"tpm"`
}

// generateUserAPIKey returns the fk- plaintext plus its SHA-256 hash.
func generateUserAPIKey() (raw, hash, prefix string, err error) {
    buf := make([]byte, 24)
    if _, err = rand.Read(buf); err != nil {
        return "", "", "", err
    }
    raw = "fk-" + hex.EncodeToString(buf)
    sum := sha256.Sum256([]byte(raw))
    hash = hex.EncodeToString(sum[:])
    // Tail-4 display convention (issue #262): enough for humans to tell keys
    // apart, useless for reconstruction.
    prefix = "fk-…" + raw[len(raw)-4:]
    return raw, hash, prefix, nil
}

// registerUserKeyRoutes wires the /api/v1/keys surface. Master-gated: this is
// the credential-issuance control plane, not a per-key self-service (the
// admin dashboard proxies it later). Go 1.22 exact patterns beat any
// catch-all registered under /api/v1/.
func (s *Server) registerUserKeyRoutes(mux *http.ServeMux) {
    mux.HandleFunc("GET /api/v1/keys", s.withMasterKey(s.handleUserKeysList))
    mux.HandleFunc("POST /api/v1/keys", s.withMasterKey(s.handleUserKeyCreate))
    mux.HandleFunc("POST /api/v1/keys/{name}/rotate", s.withMasterKey(s.handleUserKeyRotate))
    mux.HandleFunc("DELETE /api/v1/keys/{name}", s.withMasterKey(s.handleUserKeyRevoke))
    slog.Info("user api key routes registered", "prefix", "/api/v1/keys")
}

func (s *Server) handleUserKeysList(w http.ResponseWriter, r *http.Request) {
    keys, err := s.store.ListKeys()
    if err != nil {
        slog.Error("user keys list failed", "error", err)
        writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to list keys"})
        return
    }
    result := make([]userKeyListEntry, 0, len(keys))
    for _, k := range keys {
        entry := userKeyListEntry{
            Name:       k.Name,
            KeyPrefix:  k.KeyPrefix,
            Status:     k.Status,
            Scopes:     k.Scopes,
            QuotaLimit: k.QuotaLimit,
            QuotaUsed:  k.QuotaUsed,
            RPM:        k.RPM,
            TPM:        k.TPM,
            RotatedAt:  k.KeyRotatedAt,
            CreatedAt:  k.CreatedAt,
            UpdatedAt:  k.UpdatedAt,
        }
        // Tenant column: resolve from the key's own metadata binding
        // (BindKeyToTeam records it in Metadata at create time).
        if k.Metadata != nil {
            entry.Tenant = k.Metadata["tenant"]
        }
        result = append(result, entry)
    }
    writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleUserKeyCreate(w http.ResponseWriter, r *http.Request) {
    r.Body = http.MaxBytesReader(w, r.Body, maxAdminBodySize)
    var req createUserKeyRequest
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        http.Error(w, `{"error":{"message":"invalid JSON","type":"invalid_request"}}`, http.StatusBadRequest)
        return
    }
    if strings.TrimSpace(req.Name) == "" {
        http.Error(w, `{"error":{"message":"name is required","type":"invalid_request"}}`, http.StatusBadRequest)
        return
    }
    if len(req.Scopes) == 0 {
        http.Error(w, `{"error":{"message":"scopes is required (module or module:action grants)","type":"invalid_request"}}`, http.StatusBadRequest)
        return
    }
    if req.QuotaLimit < 0 || req.RPM < 0 || req.TPM < 0 {
        http.Error(w, `{"error":{"message":"quota/rpm/tpm must be non-negative","type":"invalid_request"}}`, http.StatusBadRequest)
        return
    }
    if req.Tenant != "" {
        if _, err := s.store.GetTeam(req.Tenant); err != nil {
            http.Error(w, `{"error":{"message":"unknown tenant","type":"invalid_request"}}`, http.StatusBadRequest)
            return
        }
    }
    raw, hash, prefix, err := generateUserAPIKey()
    if err != nil {
        slog.Error("user key generation failed", "name", req.Name, "error", err)
        http.Error(w, `{"error":{"message":"key generation failed","type":"api_error"}}`, http.StatusInternalServerError)
        return
    }
    entry := &store.APIKeyEntry{
        Name:          req.Name,
        KeyPrefix:     prefix,
        KeyHash:       hash,
        Status:        "active",
        Scopes:        req.Scopes,
        AllowedModels: req.AllowedModels,
        QuotaLimit:    req.QuotaLimit,
        RPM:           req.RPM,
        TPM:           req.TPM,
    }
    if req.Tenant != "" {
        entry.Metadata = map[string]string{"tenant": req.Tenant}
    }
    if err := s.store.CreateKey(entry); err != nil {
        slog.Warn("user key create rejected", "name", req.Name, "error", err)
        http.Error(w, `{"error":{"message":"key creation failed: "+err.Error(),"type":"invalid_request"}}`, http.StatusConflict)
        return
    }
    if req.Tenant != "" {
        if err := s.store.BindKeyToTeam(raw, req.Tenant); err != nil {
            // Key exists but is unbound — not fatal, but loudly visible: an
            // unbound key still authenticates, it just asserts no tenant.
            slog.Error("user key tenant binding failed (key stays tenant-less)",
                "name", req.Name, "tenant", req.Tenant, "error", err)
        }
    }
    slog.Info("user api key created", "name", req.Name, "scopes", strings.Join(req.Scopes, ","), "tenant", req.Tenant)
    writeJSON(w, http.StatusCreated, userKeyResponse{
        Name:      entry.Name,
        KeyPrefix: entry.KeyPrefix,
        RawKey:    raw,
        Scopes:    entry.Scopes,
        Tenant:    req.Tenant,
        Status:    entry.Status,
        CreatedAt: entry.CreatedAt,
    })
}

func (s *Server) handleUserKeyRotate(w http.ResponseWriter, r *http.Request) {
    name := r.PathValue("name")
    entry, err := s.store.GetKey(name)
    if err != nil {
        http.Error(w, `{"error":{"message":"key not found","type":"invalid_request"}}`, http.StatusNotFound)
        return
    }
    if entry.Status != "active" {
        http.Error(w, `{"error":{"message":"key is not active","type":"invalid_request"}}`, http.StatusConflict)
        return
    }
    raw, hash, prefix, err := generateUserAPIKey()
    if err != nil {
        slog.Error("user key generation failed on rotate", "name", name, "error", err)
        http.Error(w, `{"error":{"message":"key generation failed","type":"api_error"}}`, http.StatusInternalServerError)
        return
    }
    now := time.Now()
    entry.KeyPrevHash = entry.KeyHash
    entry.KeyRotatedAt = &now
    entry.KeyHash = hash
    entry.KeyPrefix = prefix
    // BindKeyToTeam is keyed by plaintext and the old plaintext is gone from
    // our hands — the auth path resolves the tenant from the side table on
    // every request, so the rotated key keeps the binding only if the caller
    // re-presents it. Bind the new plaintext now when we know the tenant.
    if entry.Metadata != nil && entry.Metadata["tenant"] != "" {
        if err := s.store.BindKeyToTeam(raw, entry.Metadata["tenant"]); err != nil {
            slog.Error("user key tenant re-bind failed on rotate", "name", name, "error", err)
        }
    }
    if err := s.store.UpdateKey(entry); err != nil {
        slog.Error("user key rotate persist failed", "name", name, "error", err)
        http.Error(w, `{"error":{"message":"rotation failed","type":"api_error"}}`, http.StatusInternalServerError)
        return
    }
    slog.Info("user api key rotated", "name", name)
    writeJSON(w, http.StatusOK, userKeyResponse{
        Name:      entry.Name,
        KeyPrefix: entry.KeyPrefix,
        RawKey:    raw,
        Scopes:    entry.Scopes,
        Tenant:    entry.Metadata["tenant"],
        Status:    entry.Status,
        CreatedAt: entry.CreatedAt,
    })
}

func (s *Server) handleUserKeyRevoke(w http.ResponseWriter, r *http.Request) {
    name := r.PathValue("name")
    entry, err := s.store.GetKey(name)
    if err != nil {
        http.Error(w, `{"error":{"message":"key not found","type":"invalid_request"}}`, http.StatusNotFound)
        return
    }
    if entry.Status == "revoked" {
        http.Error(w, `{"error":{"message":"key already revoked","type":"invalid_request"}}`, http.StatusConflict)
        return
    }
    // Soft-delete only (preserve-API-keys policy): auth rejects non-active
    // statuses, so the credential is dead immediately, but the row + usage
    // history stay for audit and the admin surface.
    entry.Status = "revoked"
    if err := s.store.UpdateKey(entry); err != nil {
        slog.Error("user key revoke persist failed", "name", name, "error", err)
        http.Error(w, `{"error":{"message":"revoke failed","type":"api_error"}}`, http.StatusInternalServerError)
        return
    }
    slog.Info("user api key revoked (soft)", "name", name)
    writeJSON(w, http.StatusOK, map[string]string{"name": name, "status": "revoked"})
}
