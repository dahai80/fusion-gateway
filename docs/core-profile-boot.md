# Core-Profile Boot Contract (#490)

Type: contract · Status: stable · Introduced: v0.9.119

The core profile is the minimal download: **fusion-mlx + fusion-gateway +
fusion-supervisor + fusion-web-shell + fusion-cli**. A developer with only
these five repositories must get a zero-error boot. Everything else may
degrade visibly (warn-level, with a remediation hint) but must never spam
errors or fail startup. The `/v1/catalog` source flags (#488) are the
machine-readable degradation contract this boot contract leans on.

## Verification protocol

1. **Scratch state**: clean environment (no `~/.fusion-gateway/` data, no
   model cache, no other fusion-* repos present — in particular no root
   repo, so `architecture/port-registry.yaml` is absent).
2. **Boot order**: fusion-mlx (`start.sh start`) → fusion-gateway
   (`start.sh start`) → fusion-supervisor (daemon) → fusion-web-shell →
   fusion-cli (`fusion doctor`).
3. **Capture**: full startup logs of all five components (per-component
   files, kept as evidence).
4. **Classify** every warning/error line into:
   - `E` — error-level line: contract violation, blocks sign-off.
   - `W-remediable` — warn-level WITH a remediation hint: acceptable.
   - `W-bare` — warn-level without a remediation hint: contract violation
     of the hint requirement, fix or downgrade to info.
   - `N` — noise (repeated identical lines): flag for throttling.
5. **Route findings**: one issue per affected repository, cross-referenced
   from the #490 table.
6. **Re-verify** after fixes: clean boot with zero `E` lines and no
   `W-bare` lines; logs archived as evidence in #490.

## Contract

- **Zero error-level lines** across all five components on a core-profile
  boot.
- **Missing optional components** (rag, model-hub, plugins-ecosystem, …)
  produce at most warn-level with a remediation hint (how to install/silence).
- **Absent root repo**: port-registry self-checks in every start.sh must
  skip with one info line, never error or warn-spam (bypass precedent:
  fusion-office start.sh).
- **Degradation visibility**: `GET /v1/catalog` answers 200 with
  `sources.supervisor: false` + reason when the supervisor is absent, and
  per-source booleans otherwise — absence is a flag, never a 5xx, never
  error-spam (#488).
- **Gateway specifics** (this repo):
  - `server.auto_start` probe-first: an already-healthy fusion-mlx is
    adopted, not re-launched (#212).
  - Unknown config keys WARN once (simple-conf validator), never error.
  - `catalog.enabled` default true; an unreachable supervisor degrades the
    catalog source, not the boot.

## Evidence archive

Verification runs post their classification table to #490; the final
zero-error run's logs are archived there. The catalog's `sources` block
serves as the runtime probe for the degradation half of the contract.
