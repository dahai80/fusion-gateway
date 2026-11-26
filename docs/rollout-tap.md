---
doc_id: fusion-gateway/guide
product: fusion-gateway
class: core
entry: service
doc_type: guide
title: { zh: "Rollout tap 采集契约", en: "Rollout tap capture contract" }
audience: engineer
status: draft
version: 0.9.195
updated: 2026-11-25
owner: "@dahai"
source_of_truth:
  - docs/rollout-tap.md
---

# Rollout tap (#608 / #720)

The rollout tap mirrors production chat traffic from the gateway into an
append-only local JSONL sink that fusion-trainer consumes as RL data
(RFT/GRPO). It is the proxy-side collection pattern: one choke point
(`:11432`), no second proxy, 100% offline — the sink is a local file, there
is no egress.

## Gateway config

```yaml
rollout_tap:
    enabled: true
    sink_path: "~/.fusion-gateway/rollout-tap.jsonl"  # required when enabled
    content_mode: raw          # redact (default) | raw
    tenants: []                # tenant allowlist; empty = all tenants
    max_queue: 1024            # async write queue; overflow drops loudly
    capture_max_bytes: 262144  # response capture buffer per request
```

- Opt-in matrix: `tenants` allowlist + per-key `rollout_tap: true/false`
  override on auth keys (deny beats allow).
- `content_mode: redact` (default) drops message content but keeps the
  shape/length (`[redacted:<n>]`); tool-call arguments get the same
  treatment (#720). `raw` stores verbatim content — explicit opt-in.
- Isolation contract: the tap never blocks, fails, or slows the proxied
  request; a sink failure degrades to a log line.

## Record schema (v1)

One JSON object per line. Required: `schema_version`, `rollout_id`,
`messages`. Optional: everything else.

```json
{
  "schema_version": 1,
  "rollout_id": "ro-65a820a0d931a25a",
  "tenant_id": "master",
  "source": "chat_completions",
  "model": "mlx-community/Qwen3.5-4B-MLX-4bit",
  "created_at": "2026-11-25T10:31:16Z",
  "tools": [{"type": "function", "function": {"name": "make_doc"}}],
  "messages": [
    {"role": "user", "content": "..."},
    {"role": "assistant", "content": "", "tool_calls": [{"id": "call_1", "type": "function", "function": {"name": "make_doc", "arguments": "{}"}}]},
    {"role": "tool", "content": "ok", "tool_call_id": "call_1"},
    {"role": "assistant", "content": "done"}
  ],
  "finish_reason": "stop",
  "latency_ms": 113
}
```

Tool trajectories (#720): streamed `tool_calls` fragments are merged by
index (id/name from the first fragment, `function.arguments` concatenated),
role=tool results keep their `tool_call_id`, and the request's `tools`
definitions are mirrored so the trajectory shows what the agent could call.

## Outcome join (office flywheel)

The tap never writes outcomes — app-observable producers backfill them:

1. Office-agent request goes through the gateway; the tap appends a record
   with a fresh `rollout_id` to `rollout_tap.sink_path`.
2. The app reports the outcome to office-api `POST /v1/outcomes`
   (fusion-office #33) with the same `rollout_id`; the service appends an
   outcome record (same schema + an `outcome: {signal, score}` object) to
   its own sink file (`~/.fusion-office/rollout-outcomes.jsonl`).
3. fusion-trainer consumes BOTH files and joins on `rollout_id`.

Signals: `task_success`, `user_accepted`, `user_edited`, `user_rejected`,
`error` — mapped to default rewards by the trainer when no explicit score
is present.

## Trainer wiring

Point fusion-trainer at the sinks (os.pathsep-separated files or
directories of `*.jsonl`):

```bash
FUSION_TRAINER_ROLLOUT_SINKS="$HOME/.fusion-gateway/rollout-tap.jsonl:$HOME/.fusion-office/rollout-outcomes.jsonl"
```

The trainer dashboard (`GET /api/dashboard` → trajectory panel) validates
each line (`schema_version == 1` + `rollout_id` + `messages`), counts
totals + a 7-day window, and aggregates the outcome-signal mix / positive
share. Missing data renders an honest empty state — never fabricated
numbers.

## Operating notes

- Sink file grows unbounded by design (append-only training data); rotate
  by moving the file — the sink re-creates it on the next record.
- Hot-reload (#720): a reload updates enabled/tenants/content_mode live and
  re-opens the sink when `sink_path` changes — but only if the plane was
  enabled at boot (the tap object is built once at startup; arming a
  boot-disabled plane needs a restart).
- Per-key opt-out (`rollout_tap: false` on an auth key) always wins over
  the tenant allowlist.
