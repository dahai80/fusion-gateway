#!/usr/bin/env bash
# A5 CI gate: ban bare `go func()` / `go someCall()` launches outside the
# sanctioned safeGo wrapper. A bare goroutine has no panic recovery, so a
# single panic in a background worker (cleanup loop, batch processor, SSE
# relay) crashes the whole gateway process. The CLAUDE.md mandate is
# "Background goroutines always go through safeGo".
#
# Allowed bare-goroutine sites:
#   - internal/safego/safego.go  — the safeGo implementation itself (the one
#     place a bare `go func()` is correct: it IS the recover wrapper).
#   - cmd/gateway/main.go        — safeGo is defined here too (same reason).
#   - internal/cluster/discovery.go — checkAll fans out per-node workers with
#     an inline `defer recover()` + WaitGroup barrier; safeGo is fire-and-
#     forget and cannot pair with a WaitGroup, so these carry their own
#     recovery and are explicitly permitted.
#
# Exit 1 if any other non-test .go file launches a bare goroutine.
set -euo pipefail

cd "$(dirname "$0")/.."

# Match a line whose first non-space token is `go` followed by `func(` or a
# call. Skip test files, the three allowlisted files, and lines that are
# already safego.Go(...) calls (defensive — those are the correct form).
ALLOW='internal/safego/safego.go|cmd/gateway/main.go|internal/cluster/discovery.go'

offenders=$(grep -rEn '^[[:space:]]*go (func\(|[a-zA-Z_])' --include='*.go' internal/ cmd/ \
    | grep -v '_test\.go' \
    | grep -v 'safego\.Go(' \
    | grep -Ev "^(${ALLOW}):" \
    || true)

if [ -n "$offenders" ]; then
    echo "FAIL: bare goroutine launch(es) without safeGo panic recovery found." >&2
    echo "Per CLAUDE.md, background goroutines must go through safeGo." >&2
    echo "If a site genuinely needs a bare goroutine (e.g. a WaitGroup barrier" >&2
    echo "with inline recover), add it to ALLOW in scripts/check_bare_goroutines.sh." >&2
    echo "---" >&2
    echo "$offenders" >&2
    exit 1
fi

echo "OK: no bare goroutine launches outside safeGo."
