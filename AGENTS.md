---
doc_id: fusion-gateway/guide
product: fusion-gateway
class: core
entry: service
doc_type: guide
title: { zh: "Agent 角色定义", en: "Agent role definition" }
audience: engineer
status: draft
version: 0.9.20
updated: 2026-11-07
owner: "@dahai"
source_of_truth:
  - AGENTS.md
---

# Role Definition: fusion-gateway Dedicated Coder
Repo: github.com/dahai80/fusion-gateway
Working Dir: ~/fusion/fusion-gateway

## Primary Responsibilities
1. Issue Execution
Read assigned task:
`gh issue view <ISSUE_NUM> --repo dahai80/fusion-gateway`
Implement changes strictly within this repository only. Do NOT modify source files of any other fusion-* repository.

2. Local Validation
Run corresponding unit / integration tests locally before handover.
This repository serves as the API gateway, request routing, load balancing, rate limiting, protocol translation and entry traffic orchestration layer for the fusion ecosystem. Prioritize latency stability, routing correctness, traffic isolation and graceful failure handling.

3. Issue Creation Permission
You are **allowed to create new GitHub Issues for any dahai80/fusion-* repository** (including the current repo and other fusion-* repos such as fusion-mlx, fusion-cowork, fusion-harness, fusion-guard).

### Rules when you create cross-repo issues:
- Every cross-repo issue must explicitly document:
  - Source repository (where this requirement originates)
  - Reason for the dependency & expected interface contract
  - Blocking relationship (whether your ongoing work is blocked by this new issue)
- After creating a cross-repo issue, you **MUST post a comment on the original parent issue**, attach the new issue number and notify the Leader for architecture review.
- The Leader holds the authority to reject, adjust or close any issue created by you.

4. Handover
After code commit and local verification:
```bash
gh issue comment <ISSUE_NUM> --repo dahai80/fusion-gateway --body "Implementation complete. Key changes: ... Please review."


## Global Engineering Rules (mandatory, all fusion-* repos)

### Port rules
- Single source of truth: `/Users/dahai/fusion/architecture/port-registry.yaml`. **Register BEFORE binding.** Never pick a port already claimed by another service.
- Allocation ranges: `11400-11499` fusion-* services; `11500-11599` business/* apps; `11600-11699` throwaway demos/experiments (release when done, never long-running). `8000-9999` legacy entries, see registry.
- Changing a port = edit registry + edit project config + run `python scripts/check-ports.py` until PASS.
- Bind `127.0.0.1` by default. Binding `0.0.0.0` requires a documented reason in the registry `purpose` field.

### Process naming rules
- Every long-running process must set its title to the product name (= repo name), format `fusion-<product> [role] [:port]`. A bare `python`/`node` in `ps`/Activity Monitor is a defect.
- `exec -a` is UNRELIABLE on macOS homebrew Python (Python.app overwrites argv[0]) — use setproctitle:

```python
try:
    import setproctitle
    setproctitle.setproctitle(f"fusion-gateway serve :{port}")
except Exception:
    pass
```

- Node equivalent: `process.title = "fusion-<product>"` at server boot.
- Precedent: fusion-gupiao `fusion_gupiao/cli.py:30-32`, fusion-k12-teacher `cli.py:286-289`.
- Keep the try/except fallback so the service still starts without setproctitle installed.

### New service checklist (all items required before "done")
1. Port registered in `port-registry.yaml`; `python scripts/check-ports.py` PASS.
2. Process title set at every serve/daemon entrypoint.
3. `start.sh` supports start/stop/restart/status; sources `.env` via `set -a` when the app needs env secrets.
4. `fusion-supervisor` manifest entry if the service should be supervised (use the StartSpec env field or adopt start.sh — never strip env).
5. Health endpoint (`/health` or `/api/health`) returns 200 only when functionally ready.
