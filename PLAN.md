# Plan: Fix Issues #6, #7, #8

## Issue Summary

3 open issues with significant overlap:
- **#6**: Connector OAuth2 flow + credential persistence + real SaaS API calls
- **#7**: OAuth2 provider for connector authentication (subset of #6)
- **#8**: HTTPS termination + AES-256 at-rest encryption

## Approach

### Phase 1: HTTPS Termination (#8 partial)
- Add `TLS` section to `ServerConfig`: `cert_file`, `key_file`
- Modify `Server.Start()` to call `ListenAndServeTLS()` when TLS configured
- Update `config.example.yaml`
- **Note**: In production, HTTPS is typically handled by reverse proxy (nginx/ingress). This adds native support for single-binary deployment.

### Phase 2: AES-256 At-Rest Encryption (#8 + #7 partial)
- Create `internal/crypto/aes.go`: AES-256-GCM encrypt/decrypt with per-entry nonce
- Encryption key derived from `encryption.master_key` config via HKDF
- Apply to: connector tokens, API key storage
- Update `Connection` struct to add encrypted `OAuthToken` field
- Config: add `encryption` section with `master_key`

### Phase 3: OAuth2 Authorization Flow (#7 + #6 core)
- Create `internal/connector/oauth2.go`:
  - `OAuth2Config` struct (client_id, client_secret, auth_url, token_url, scopes, redirect_url)
  - `AuthorizationURL(state string)` → redirect URL
  - `ExchangeCode(code string)` → token response
  - `RefreshToken(refreshToken string)` → new token
  - Auto-refresh on `ErrAuthExpired` in `ExecuteAction`
- Add `GET /gateway/v1/oauth2/callback` endpoint
- Add `POST /gateway/v1/oauth2/authorize` to initiate flow
- Update `Connection` struct: add `AccessToken`, `RefreshToken`, `TokenExpiry` fields (encrypted at rest)

### Phase 4: Credential Persistence (#6)
- Create `internal/connector/persistence.go`:
  - JSON file-based storage (`data/connections.json`)
  - Load on startup, save on mutation
  - Tokens encrypted via AES before writing to disk
- Update `Registry` to auto-persist on CreateConnection/DeleteConnection/RefreshConnection

### Phase 5: Real SaaS API - GoogleWorkspace (#6)
- Replace mock `GoogleWorkspaceConnector.ExecuteAction` with real HTTP calls
- Use `google.admin.directory.v1` API for list_users, get_user
- Use `calendar.v3` for list_calendar_events
- Use `gmail.v1` for send_email
- Use `drive.v3` for read_drive_file
- Auto-refresh token on 401 responses
- Keep `testMode` as mock fallback

## Files to Create/Modify

### New Files
1. `internal/crypto/aes.go` — AES-256-GCM encrypt/decrypt
2. `internal/crypto/aes_test.go` — tests
3. `internal/connector/oauth2.go` — OAuth2 flow implementation
4. `internal/connector/oauth2_test.go` — tests
5. `internal/connector/persistence.go` — JSON file persistence with encryption
6. `internal/connector/persistence_test.go` — tests

### Modified Files
7. `internal/config/config.go` — add TLSConfig, EncryptionConfig, OAuth2Config
8. `internal/server/server.go` — TLS listener, OAuth2 callback route
9. `internal/connector/types.go` — add token fields to Connection
10. `internal/connector/registry.go` — persistence hooks
11. `internal/connector/builtins.go` — real GoogleWorkspace API calls
12. `config.example.yaml` — new config sections
13. `README.md` — documentation update
