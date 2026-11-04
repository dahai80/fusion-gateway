# Product Catalog API Contract (#488)

Type: contract · Status: stable · Introduced: v0.9.117

`GET /v1/catalog` is the merged three-source product view the union web
shell renders: what exists (static catalog), what is registered and
reachable (shell manifest registry), and what is installed/running
(fusion-supervisor health). It is the machine-readable backbone of the
shell three-state product list and the #490 core-profile boot contract.

## Sources

| Source | Provider | Absence behavior |
|--------|----------|------------------|
| `catalog` | `internal/catalog` (`go:embed catalog.json`) | Parse failure = WARN log + flag false, endpoint still 200 |
| `manifests` | shell manifest registry (dynamic registrations + static fallback) | Registry nil = flag false |
| `supervisor` | `catalog.supervisor_status_url` (default `http://127.0.0.1:9100/api/v1/status`) | Unreachable/undecodable = flag false + `supervisor_reason`, cached per TTL |

Degrade-by-design: **no source failure ever 5xx's or error-spams.** Every
absence is a visible boolean/flag in `sources`.

## Three-state derivation

Per product, resolved in order:

1. Supervisor knows the service: `Healthy` → `running`, `Stopped` →
   `installed_stopped`, any other state → `installed_unhealthy`.
2. Not in supervisor, but manifest-registered and reachable → `running`;
   registered but unreachable → `installed_stopped`.
3. Supervisor live and does not know the product → `not_installed`.
4. Supervisor degraded and no registry signal → `state` omitted
   (unknown, never fabricated).

## Response shape

```json
{
  "sources": {
    "catalog": true,
    "manifests": true,
    "supervisor": false,
    "supervisor_reason": "supervisor unreachable: connection refused"
  },
  "products": [
    {
      "id": "fusion-mlx",
      "title_en": "Fusion MLX",
      "title_cn": "本地推理引擎",
      "category": "core",
      "repo": "fusion-mlx",
      "description_en": "...",
      "description_cn": "...",
      "footprint": "...",
      "model_class": "...",
      "state": "running",
      "registered": true,
      "reachable": true,
      "in_catalog": true
    }
  ],
  "generated_at": "2026-11-04T03:00:00Z"
}
```

- `state` is omitted when unknown (source-degraded case above).
- `in_catalog: false` marks a **dynamic row**: a module registered in the
  manifest registry but absent from the static catalog. Dynamic rows use
  `category: "dynamic"` and the manifest title for both languages. The
  view never hides a live module.
- Products sort by `category`, then `id`.

## Configuration (`catalog.*`)

| Key | Default | Meaning |
|-----|---------|---------|
| `catalog.enabled` | `true` | Registers `/v1/catalog` at boot (restart required) |
| `catalog.supervisor_status_url` | `http://127.0.0.1:9100/api/v1/status` | Supervisor read-only status face |
| `catalog.status_ttl` | `30s` | Supervisor fetch cache TTL (fetch timeout 2s) |

## Static catalog schema

`internal/catalog/catalog.json` — one JSON object per product:

| Field | Required | Meaning |
|-------|----------|---------|
| `id` | yes | Join key (`fusion-<name>`, matches supervisor name + manifest id) |
| `title_en` / `title_cn` | yes | Display names |
| `category` | yes | `core` / `platform` / `governance` / `application` / `domain` / `business` |
| `repo` | yes | fusion-* repository name |
| `description_en` / `description_cn` | yes | One-line descriptions |
| `footprint` | yes | Resource footprint hint (memory/ports) |
| `model_class` | yes | Model dependency class (none / local / local+cloud) |
| `suggested_model` | no | Model-policy hint passed to the shell (#486 chain) |
| `dependencies` | no | ids of products this one requires |

Adding a product = one-line addition to `catalog.json` in a PR; ids must
be unique (they are the join keys). The embedded file is validated by
`TestCatalog_ValidAndUnique` (schema completeness + core-profile ids).

## Consumers

- **Union web shell** (`fusion-web-shell`): renders the three-state
  product list from `products[].state` + `sources` flags.
- **#490 core-profile boot verification**: `sources` flags are the
  degradation contract — a core-profile boot with supervisor absent must
  still answer 200 with `supervisor: false` + reason, at most WARN-level
  logging.
