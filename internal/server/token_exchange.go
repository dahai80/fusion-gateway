package server

// Issue #595: token exchange plane. A bootstrap credential (API key, or a
// shell session token) is swapped for a short-lived RS256 JWT scoped to the
// bootstrap credential's own limits. The gateway also serves its JWKS so
// downstream verifiers validate minted tokens without a shared secret.

import (
    "encoding/json"
    "log/slog"
    "net/http"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/middleware"
)

const exchangeDefaultAudience = "fusion-api"

// exchangeVerifier adapts the signer to the middleware TokenVerifier
// interface. nil (disabled) when the signer is absent — the middleware then
// accepts no minted tokens.
func (s *Server) exchangeVerifier() middleware.TokenVerifier {
    if s.tokenSigner == nil {
        return nil
    }
    return s.tokenSigner
}

type tokenExchangeRequest struct {
    TTLSeconds int    `json:"ttl_seconds"`
    Audience   string `json:"audience"`
}

// handleTokenExchange (POST /auth/api/token/exchange) mints a scoped token
// from an already-authenticated credential. Auth happens in the middleware
// chain (withMiddleware → APIKeyAuthWithStore); this handler only sees
// resolved Principals. Re-exchange is rejected: a minted JWT carries no
// plaintext key (KeyConfig.Key == ""), so it cannot bootstrap again.
func (s *Server) handleTokenExchange(w http.ResponseWriter, r *http.Request) {
    if s.tokenSigner == nil {
        writeJSONError(w, http.StatusNotFound, "token exchange is disabled (auth.token_exchange.enabled=false)")
        return
    }
    p := middleware.PrincipalFromContext(r.Context())
    if p == nil {
        writeJSONError(w, http.StatusUnauthorized, "missing authenticated principal")
        return
    }

    var sub string
    var tenant string
    var models []string
    var rpm, tpm int
    var credExpires time.Time
    var credHasExpiry bool

    switch p.AuthMethod {
    case "shell-session":
        // Shell session tokens are resolved in the middleware by the shell
        // session plane; re-derive the claims here from the raw token so the
        // minted JWT inherits the session's subject + tenant.
        token := bearerToken(r)
        claims, err := s.shellSessions.Validate(token)
        if err != nil {
            slog.Warn("token exchange rejected: shell session no longer valid", "error", err)
            writeJSONError(w, http.StatusUnauthorized, "shell session token is no longer valid")
            return
        }
        sub = "shell:" + claims.Sub
        tenant = claims.TenantID
    case "apikey":
        if p.KeyConfig == nil {
            writeJSONError(w, http.StatusUnauthorized, "credential has no key context")
            return
        }
        if p.KeyConfig.Key == "" {
            // Minted JWTs carry no plaintext key; blocking re-exchange keeps
            // a leaked short-lived token from extending its own life.
            slog.Warn("token exchange rejected: minted exchange tokens cannot be re-exchanged", "sub", p.KeyConfig.Name)
            writeJSONError(w, http.StatusForbidden, "exchange tokens cannot be used to mint further tokens")
            return
        }
        sub = p.KeyConfig.Name
        tenant = p.KeyConfig.TeamID
        models = p.KeyConfig.AllowedModels
        rpm = p.KeyConfig.RPM
        tpm = p.KeyConfig.TPM
        if p.KeyConfig.ExpiresAt != "" {
            if exp, err := time.Parse(time.RFC3339, p.KeyConfig.ExpiresAt); err == nil {
                credExpires = exp
                credHasExpiry = true
            }
        }
    default:
        slog.Warn("token exchange rejected: unsupported auth method", "auth_method", p.AuthMethod)
        writeJSONError(w, http.StatusUnauthorized, "credential type does not support token exchange")
        return
    }

    var req tokenExchangeRequest
    if r.Body != nil {
        if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil && err.Error() != "EOF" {
            slog.Warn("token exchange: invalid request body", "error", err)
            writeJSONError(w, http.StatusBadRequest, "invalid request body")
            return
        }
    }

    maxTTL := s.cfg.Config.Auth.TokenExchange.MaxTTL
    if maxTTL <= 0 {
        maxTTL = time.Hour
    }
    ttl := maxTTL
    if req.TTLSeconds > 0 && time.Duration(req.TTLSeconds)*time.Second < ttl {
        ttl = time.Duration(req.TTLSeconds) * time.Second
    }
    if credHasExpiry {
        if remaining := time.Until(credExpires); remaining < ttl {
            if remaining <= 0 {
                writeJSONError(w, http.StatusUnauthorized, "bootstrap credential is expired")
                return
            }
            ttl = remaining
        }
    }
    audience := req.Audience
    if audience == "" {
        audience = exchangeDefaultAudience
    }

    token, exp, err := s.tokenSigner.Sign(sub, audience, ttl, tenant, models, rpm, tpm)
    if err != nil {
        slog.Error("token exchange: mint failed", "error", err)
        writeJSONError(w, http.StatusInternalServerError, "failed to mint token")
        return
    }
    slog.Info("token exchange minted", "sub", sub, "audience", audience, "ttl", ttl.Round(time.Second).String(), "tenant", tenant)
    w.Header().Set("Content-Type", "application/json")
    w.Header().Set("Cache-Control", "no-store")
    _ = json.NewEncoder(w).Encode(map[string]any{
        "access_token": token,
        "token_type":   "Bearer",
        "expires_in":   int(time.Until(exp).Seconds()),
        "issued_at":    time.Now().UTC().Format(time.RFC3339),
    })
}

// handleJWKS serves the gateway's own signing keys (RFC 7517) so downstream
// verifiers validate minted tokens offline. 404 when the exchange plane is
// disabled — the route is a no-op surface then.
func (s *Server) handleJWKS(w http.ResponseWriter, r *http.Request) {
    if s.tokenSigner == nil {
        http.NotFound(w, r)
        return
    }
    w.Header().Set("Content-Type", "application/json")
    w.Header().Set("Cache-Control", "no-store")
    _ = json.NewEncoder(w).Encode(s.tokenSigner.JWKS())
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(status)
    _ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": msg, "type": "auth_error"}})
}
