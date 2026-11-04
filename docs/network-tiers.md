# Network Tiers Contract (#493, phase 1)

Type: contract · Status: stable · Introduced: v0.9.119

The gateway binds and authenticates according to a network tier. Phase 1
ships `local` and `lan`; `public` is deployment guidance only (reverse-proxy
TLS termination — no built-in TLS).

## Tiers

| Tier | Bind | Auth posture |
|------|------|--------------|
| `local` (default) | `server.host` (default `127.0.0.1`) | Identical to today — regression-tested |
| `lan` | `0.0.0.0` via `server.host` | Hard auth gate: every route requires a credential except the anonymous allowlist below; startup fails when the auth plane is off |
| `public` | n/a (phase 1) | See `docs/deployment-public-tier.md` |

## Configuration

| Key | Default | Meaning |
|-----|---------|---------|
| `network.tier` | `local` | `local` or `lan` (validated enum) |
| env `FUSION_NETWORK_TIER` | — | Overrides `network.tier` (env-first, same as other secrets) |
| `server.host` | `127.0.0.1` | Bind address; set `0.0.0.0` for lan |

### lan startup gate

`network.tier: lan` fails startup when the auth plane cannot reject an
anonymous request:

- `auth.enabled: false` or `auth.passthrough: true` → startup error listing
  the anonymous surface (`"*": auth disabled — every route is anonymous`).
- No credentials at all (empty `auth.master_key`, zero `auth.api_keys`, and
  no store-issued keys) → startup error with the same listing.

The anonymous allowlist (surfaces anonymous **by design**, enforced at
request time by the lan guard wrapper around the mux):

- `/health`, `/healthz`, `/readyz`, `/livez` — probes
- `/admin/` — SPA static + admin login; admin API endpoints carry their own
  cookie/JWT gates
- `/api/v1/shell/session` — shell login
- `/gateway/v1/managed-settings` + `/pubkey` — Ed25519-signature trust surface
- `/` SPA static (only when `web_shell.spa_dir` is configured)

Everything else: a request carrying no API key (Bearer / X-API-Key), admin
session cookie, or shell session cookie is rejected `401` before any handler
runs — including routes whose own gate is weaker or config-disabled.

## Key-plane naming unification

Canonical names (ecosystem-wide: `architecture/config-conventions.md` §1)
and their gateway-side bindings:

| Canonical | Plane | Gateway binding |
|-----------|-------|-----------------|
| `FUSION_CLIENT_KEY` | user/client | Client-side ambient credential — **never enters the gateway process** (#412). `FUSION_GATEWAY_KEY` is accepted as a documentation alias of the same plane; setting it logs one WARN pointing at the canonical name |
| `FUSION_ADMIN_KEY` | management | Binds `auth.master_key` (env-first). `FG_MASTER_KEY` stays as the Kubernetes secretKeyRef injection name |
| `FUSION_SERVICE_KEY_<MODULE>` | service-to-gateway | Overlays `web_shell.service_keys.<module>` at load (uppercase module id, e.g. `FUSION_SERVICE_KEY_FUSION_RAG` → `fusion-rag`); a registered service key authenticates that module's own manifest registration/self-heal |
| `FUSION_MLX_API_KEY` | backend injection | Unchanged (#412) |
| `FG_SHELL_ADMIN_KEY` | module registration | Unchanged canonical (#409); legacy `FUSION_GATEWAY_ADMIN_KEY` alias stays |

Legacy names keep working with a deprecation WARN logged once at load; the
mapping table above is the migration reference.

## lan hardening (groundwork shipped here)

- Per-request audit with key identity: existing request-log plane already
  records the resolved principal (`X-Fusion-Principal`, #262).
- CORS: already config-gated (`cors.*`); lan deployments must set
  `cors.allowed_origins` explicitly — documented in the deployment guide.
- Per-key rate limiting: `routing.rate_limit` is global today; per-key
  granularity is the follow-up issue tracked on #493.

## Port registry

The 11432 entry in `architecture/port-registry.yaml` gains tier semantics
via the workspace-side sync (dependency noted on #493) — this repo does not
own that file.
