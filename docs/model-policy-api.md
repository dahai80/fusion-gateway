---
doc_id: fusion-gateway/guide
product: fusion-gateway
class: core
entry: service
doc_type: guide
title: { zh: "模型策略 API 契约", en: "Model-policy API contract" }
audience: engineer
status: draft
version: 0.9.20
updated: 2026-11-07
owner: "@dahai"
source_of_truth:
  - docs/model-policy-api.md
---

# Model-Policy API Contract (#486)

Type: contract · Status: stable · Introduced: v0.9.115

The central model-policy plane: a tenant-scoped four-layer preference chain
that every model-consuming module resolves through, replacing per-module
build-time env defaults (legacy, deprecated progressively).

## Resolution chain

```
module override > user preference (allowlist-constrained)
    > tenant default > build-time fallback (routing.default_model, legacy)
```

Tenant scope is authoritative and gateway-derived (api_key → team binding,
identity lease) — client-supplied tenant headers are never trusted. A
principal with no tenant binding resolves in the unnamed (`""`) tenant
namespace, fully isolated from named tenants.

`privacy_mode: local_only` filters the candidate set to local-backend
models (mlx / model-hub residency per the router ModelSet wiring). The
whole chain re-filters — a non-local module override is skipped, not
fatal. If nothing local remains, resolution returns an empty model with
`privacy_filtered: true`; callers must fall back to their own safe
default rather than assuming a cloud model.

## Endpoints

### GET/PUT /v1/model-policy — admin role only

Management-plane surface: the inference master key maps to the inference
role and is rejected (RR3 plane separation). Requires an admin-role
principal: OIDC RBAC / identity role, an admin-login JWT, or an admin
shell-session token (the same bridges the `/admin` plane runs — without
them a deployment with no OIDC would have no way to administer policy).

Request/response body (`PUT`, and the `policy` member of `GET`):

```json
{
    "default_model": "mlx/qwen3.5-32b",
    "module_overrides": { "fusion-code": "mlx/coder" },
    "user_allowlist": ["mlx/coder", "mlx/chat"],
    "privacy_mode": "all"
}
```

- `privacy_mode`: `"all"` (default, empty allowed) | `"local_only"`.
- `user_allowlist`: models users may select as their preference. Empty =
  unrestricted. Constrains both the write (403 outside) and resolution
  (stale preferences recorded before a narrowing are ignored).
- Validation: privacy_mode enum; module/model ids non-empty. Invalid
  bodies are 400.
- Hot-apply: PUT takes effect on the next resolution without a gateway
  restart; state persists to `data_dir/model_policy.json`.
- Every PUT lands in the hash-chained audit log
  (`data_dir/model_policy_audit.json`).
- `GET`/`PUT` responses carry `"tenant"` — the tenant the change applies
  to, so cross-tenant mistakes are visible at the wire.
- The admin-JWT / shell-session bridges run BEFORE the auth middleware
  chain (#499): a Bearer admin JWT reaches APIKeyAuth as an already-set
  `Principal{AuthMethod:"admin-jwt"}` and hits the auth short-circuit,
  not a 401. The unnamed (`""`) tenant namespace is a valid PUT target —
  an admin JWT has no team binding, so its policy lands there.

### GET /v1/model-preference — any authenticated principal

Query: `module` (optional; empty resolves the tenant default / fallback
chain only).

```json
{
    "tenant": "tenant-a",
    "user": "user-1",
    "module": "fusion-code",
    "model": "mlx/coder",
    "source": "module_override",
    "privacy_filtered": false
}
```

`source` ∈ `module_override` | `user_preference` | `tenant_default` |
`fallback` | `""` (nothing resolved).

### GET /api/v1/shell/model-policy — shell-session modules (#487, #491)

Alias: `GET /api/v1/model-policy` (same handler). Behind the standard
shell-session gate. Read-only view for module/iframes that authenticate
with a shell session token instead of an API key; resolution follows the
identical chain (session tenant, session user):

```json
{
    "resolved_model": "mlx/coder",
    "source": "module_override",
    "allowlist": ["mlx/coder", "mlx/chat"],
    "privacy_mode": "all",
    "privacy_filtered": false,
    "tenant": "local",
    "user": "alice",
    "module": "fusion-code"
}
```

Under `privacy_mode: local_only` the surfaced `allowlist` is filtered to
local models as well. Consumers treat this endpoint best-effort: 404 /
unreachable / shape drift = fall through to the manifest
`suggested_model` (#491 consumer contract), so the payload may grow
without breaking modules.

### PUT /v1/model-preference — self preference

Body: `{"module": "fusion-code", "model": "mlx/chat"}`. Writes the
caller's own preference; 403 when the model is outside the tenant
allowlist. `DELETE /v1/model-preference?module=...` clears it.

## Manifest schema v3 fields (#486 scope 2)

`config.ShellManifest` extends with two pass-through fields (the
#250/#297 ext-field precedent — the gateway validates JSON safety only;
the admin governs them in Settings):

| Field | JSON wire key | Meaning |
|-------|---------------|---------|
| `ModelCapable` | `model_capable` | Module can consume a resolved model at all |
| `SuggestedModel` | `suggested_model` | Module's own domain suggestion (advisory; resolution follows the chain, not this hint) |

Older manifests without the fields keep working: both are `omitempty` on
the wire and in the persistence round-trip.

## Consumers

- fusion-web-shell Settings UI (out of scope here) administers the policy.
- Modules/iframes call `GET /v1/model-preference` via the shell SDK.
- Agent dual-SKU binding consumes the resolved model (as#570); a
  `privacy_mode: local_only` tenant pins the agent to local models.

## Out of scope

Settings UI, per-module adoption, agent binding consumption.
