package middleware

import (
    "bytes"
    "context"
    "encoding/json"
    "io"
    "log/slog"
    "net/http"

    "github.com/fusion-gateway/fusion-gateway/internal/adapter"
    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/identity"
    pb "github.com/fusion-gateway/fusion-gateway/internal/identity/pb"
)

// #157: fusion-identity gRPC control-plane middleware. Sits AFTER
// APIKeyAuthWithStore in the chain so the plaintext api key is already
// resolved (p.KeyConfig.Key holds it). When identity.enabled, the inference
// hot path calls AuthorizeAndAcquire before the request reaches fusion-mlx:
//   - allowed → stamp the identity-derived tenant (authoritative over the
//     local key->team binding) + lease_id into ctx, then proceed.
//   - denied → map error_code → HTTP (#157 §4.4) and reject.
//   - transport/breaker failure → fallback_to_local proceeds on the local
//     auth result already produced by APIKeyAuthWithStore; strict mode 503.
//
// Admin-JWT requests (p.AuthMethod=="admin-jwt") skip this: the admin surface
// does not consume the inference control plane. Master key skips too (no
// tenant/lease semantics; #157 is tenant-scoped).
//
// The lease_id + tenant ctx key (identityLeaseKey) is read by the
// stream-end path (server) to call ReleaseLease + ReportUsage.

type identityLeaseKeyType struct{}

var identityLeaseKey identityLeaseKeyType

// IdentityLease holds the per-request identity lease metadata, threaded from
// this middleware to the stream-end path.
type IdentityLease struct {
    LeaseID    string
    TenantID   string
    Priority   pb.PriorityLevel
    MaxAllowedTokens int32
}

// IdentityLeaseFromContext returns the lease stamped by IdentityAuth, or nil.
func IdentityLeaseFromContext(ctx context.Context) *IdentityLease {
    l, _ := ctx.Value(identityLeaseKey).(*IdentityLease)
    return l
}

// IdentityAuth builds the gRPC control-plane middleware. A nil client means
// identity is disabled — the middleware is a no-op pass-through (the local
// APIKeyAuthWithStore path already authenticated the request).
func IdentityAuth(client *identity.Client, cfg *config.IdentityConfig) func(http.Handler) http.Handler {
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            if client == nil || cfg == nil || !cfg.Enabled {
                next.ServeHTTP(w, r)
                return
            }
            ctx, p := EnsurePrincipal(r.Context())

            // admin surface + master key are not inference-tenant-scoped.
            if p.AuthMethod == "admin-jwt" || p.IsMaster {
                next.ServeHTTP(w, r.WithContext(ctx))
                return
            }

            apiKey := ""
            if p.KeyConfig != nil {
                apiKey = p.KeyConfig.Key
            }
            if apiKey == "" {
                // No resolved key (e.g. auth passthrough). Cannot call
                // AuthorizeAndAcquire without a credential — fall through to
                // the local path rather than inventing a denial.
                slog.Debug("identity auth skipped: no api key resolved", "path", r.URL.Path)
                next.ServeHTTP(w, r.WithContext(ctx))
                return
            }

            module := inferenceModule(r)
            model := requestModel(r)
            requestID := RequestIDFromContext(ctx)
            // #160: send the tenant resolved from the credential (NOT a
            // client-supplied assertion). APIKeyAuthWithStore upstream already
            // bound the key to its team via GetTeamByKey, so p.Team.ID is the
            // gateway-authoritative tenant. The identity servicer compares it
            // against the api-key's real tenant — a mismatch is refused
            // (P2-3 cross-tenant guard). Empty = no local binding (identity
            // treats asserted=="" as no assertion).
            resolvedTenantID := ""
            if p.Team != nil {
                resolvedTenantID = p.Team.ID
            }
            ar, err := client.AuthorizeAndAcquire(ctx, apiKey, module, model, requestID, r.RemoteAddr, resolvedTenantID)
            if err != nil {
                if client.FallbackToLocal() {
                    // identity unavailable + configured to fall back: proceed
                    // on the local auth result. The local key-store path is
                    // fail-closed on its own (invalid key → 401 upstream).
                    slog.Warn("identity unavailable, falling back to local auth",
                        "path", r.URL.Path, "error", err)
                    next.ServeHTTP(w, r.WithContext(ctx))
                    return
                }
                // strict mode: identity is a hard dependency. New request 503.
                slog.Warn("identity unavailable (strict mode), rejecting new request",
                    "path", r.URL.Path, "error", err)
                writeIdentityError(w, http.StatusServiceUnavailable, "identity service unavailable", "")
                return
            }

            if !ar.Allowed {
                status, msg := mapAuthErrorCode(ar.ErrorCode, ar.ErrorMessage)
                slog.Info("identity denied request",
                    "path", r.URL.Path, "error_code", ar.ErrorCode, "tenant", ar.TenantID)
                writeIdentityError(w, status, msg, ar.ErrorCode.String())
                return
            }

            // allowed: stamp the identity-derived tenant as authoritative
            // (overriding any local key->team binding — identity is the SSOT
            // for tenant context per #157). Re-inject into adapter tenant ctx
            // so outbound X-Fusion-Tenant reflects identity's derivation.
            if ar.TenantID != "" {
                if p.Team == nil {
                    p.Team = &TeamInfo{ID: ar.TenantID, Name: ar.TenantName, Role: RoleInference}
                } else {
                    p.Team.ID = ar.TenantID
                    if ar.TenantName != "" {
                        p.Team.Name = ar.TenantName
                    }
                }
                ctx = adapter.WithTenant(ctx, ar.TenantID)
            }
            if ar.LeaseID != "" {
                ctx = context.WithValue(ctx, identityLeaseKey, &IdentityLease{
                    LeaseID:          ar.LeaseID,
                    TenantID:         ar.TenantID,
                    Priority:         ar.Priority,
                    MaxAllowedTokens: ar.MaxAllowedTokens,
                })
                // #157 items 2+3: thread the scheduling signals onto ctx so
                // InjectFusionHeaders stamps X-Fusion-Priority +
                // X-Fusion-Max-Tokens on the outbound upstream request. The
                // gateway forwards the signals; fusion-mlx applies VRAM
                // priority + KV cache-tag isolation on its side.
                ctx = adapter.WithLeaseSignals(ctx, adapter.LeaseSignals{
                    Priority:         int32(ar.Priority),
                    MaxAllowedTokens: ar.MaxAllowedTokens,
                })
            }
            slog.Debug("identity authorized",
                "path", r.URL.Path, "tenant", ar.TenantID, "lease", ar.LeaseID, "priority", ar.Priority)
            next.ServeHTTP(w, r.WithContext(ctx))
        })
    }
}

// mapAuthErrorCode maps identity AuthErrorCode → HTTP status (#157 §4.4).
func mapAuthErrorCode(code pb.AuthErrorCode, msg string) (int, string) {
    switch code {
    case pb.AuthErrorCode_INVALID_API_KEY:
        return http.StatusUnauthorized, pickMsg(msg, "invalid api key")
    case pb.AuthErrorCode_TENANT_DISABLED:
        return http.StatusForbidden, pickMsg(msg, "tenant disabled")
    case pb.AuthErrorCode_MODULE_UNAUTHORIZED:
        return http.StatusForbidden, pickMsg(msg, "module unauthorized")
    case pb.AuthErrorCode_MODEL_UNAUTHORIZED:
        return http.StatusForbidden, pickMsg(msg, "model unauthorized")
    case pb.AuthErrorCode_CONCURRENCY_LIMIT_EXCEEDED:
        return http.StatusTooManyRequests, pickMsg(msg, "concurrency limit exceeded")
    case pb.AuthErrorCode_DAILY_QUOTA_EXCEEDED:
        return http.StatusPaymentRequired, pickMsg(msg, "daily quota exceeded")
    case pb.AuthErrorCode_RATE_LIMIT_EXCEEDED:
        return http.StatusTooManyRequests, pickMsg(msg, "rate limit exceeded")
    default:
        return http.StatusForbidden, pickMsg(msg, "authorization denied")
    }
}

func pickMsg(identityMsg, fallback string) string {
    if identityMsg != "" {
        return identityMsg
    }
    return fallback
}

func writeIdentityError(w http.ResponseWriter, status int, msg, code string) {
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(status)
    body := map[string]any{
        "error": map[string]any{
            "message":  msg,
            "type":     "identity_error",
        },
    }
    if code != "" {
        body["error"].(map[string]any)["code"] = code
    }
    _ = json.NewEncoder(w).Encode(body)
}

// inferenceModule infers the target module from the request path for the
// AuthorizeAndAcquire module check. /v1/chat/* → chat, /v1/messages → chat
// (anthropic), /v1/embeddings → rag, /v1/images → design. Unknown → "" (let
// identity decide; an empty module is not a denial by itself).
func inferenceModule(r *http.Request) string {
    switch {
    case hasPathPrefix(r, "/v1/chat/"), hasPathPrefix(r, "/v1/messages"), hasPathPrefix(r, "/v1/completions"):
        return "chat"
    case hasPathPrefix(r, "/v1/embeddings"), hasPathPrefix(r, "/v1/rerank"):
        return "rag"
    case hasPathPrefix(r, "/v1/images"):
        return "design"
    case hasPathPrefix(r, "/gateway/v1/connector"):
        return "agent"
    }
    return ""
}

func hasPathPrefix(r *http.Request, p string) bool {
    return len(r.URL.Path) >= len(p) && r.URL.Path[:len(p)] == p
}

// requestModel extracts the requested model from the JSON body without
// consuming it. Returns "" if absent or unparseable — identity treats an
// empty model as "any model" (the model_unauthorized check is skipped
// server-side when target_model is empty).
func requestModel(r *http.Request) string {
    if r.Body == nil {
        return ""
    }
    peek, err := peekModel(r, 1<<20)
    if err != nil {
        return ""
    }
    return peek
}

// peekModel extracts the "model" field from the request body without
// consuming it. It buffers the body into a bytes.Buffer (so it can be
// restored for downstream handlers), then json-decodes just the top-level
// fields — Decoder.Token streaming stops as soon as "model" is found, so a
// large prompt body is not fully parsed. The buffer itself holds the whole
// body in memory; that is unavoidable for a single-pass peek + restore, and
// the request body is already bounded by Server.MaxRequestBodySize. Returns
// "" if the field is absent or the body is not JSON.
func peekModel(r *http.Request, maxBytes int64) (string, error) {
    buf := &bytes.Buffer{}
    n, err := io.Copy(buf, io.LimitReader(r.Body, maxBytes))
    _ = n
    r.Body.Close()
    r.Body = io.NopCloser(buf)
    if err != nil || buf.Len() == 0 {
        return "", err
    }
    dec := json.NewDecoder(bytes.NewReader(buf.Bytes()))
    tok, err := dec.Token()
    if err != nil || tok != json.Delim('{') {
        return "", nil
    }
    for dec.More() {
        key, err := dec.Token()
        if err != nil {
            return "", nil
        }
        k, _ := key.(string)
        if k == "model" {
            val, err := dec.Token()
            if err != nil {
                return "", nil
            }
            if s, ok := val.(string); ok {
                return s, nil
            }
            return "", nil
        }
        // skip the value without parsing into a struct.
        if err := skipJSONValue(dec); err != nil {
            return "", nil
        }
    }
    return "", nil
}

// skipJSONValue advances the decoder past one JSON value (any type).
func skipJSONValue(dec *json.Decoder) error {
    tok, err := dec.Token()
    if err != nil {
        return err
    }
    if delim, ok := tok.(json.Delim); ok {
        depth := 1
        for depth > 0 {
            t, err := dec.Token()
            if err != nil {
                return err
            }
            if d, ok := t.(json.Delim); ok {
                if d == json.Delim('{') || d == json.Delim('[') {
                    depth++
                } else {
                    depth--
                }
            }
        }
        _ = delim
    }
    return nil
}
