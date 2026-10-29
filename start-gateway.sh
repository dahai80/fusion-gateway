#!/usr/bin/env bash
# start-gateway.sh — deprecated alias for ./start.sh
#
# #401: this file used to be a second, divergent lifecycle manager. It tracked
# its own PID file (.gateway.pid vs .fusion-gateway.pid), probed /health instead
# of /healthz, and its `stop` exited 0 in the "not running" branch, which
# aborted `restart` before it could start anything. Two managers for one
# process meant a manual stop from here was invisible to start.sh and to
# fusion-supervisor, and `restart` silently did nothing.
#
# start.sh is the one manager (fusion-supervisor runs `bash start.sh <cmd>`).
# Everything here now delegates to it so the two cannot drift again.

set -u

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

echo "NOTE: start-gateway.sh is deprecated, delegating to start.sh (#401)" >&2

exec "$SCRIPT_DIR/start.sh" "$@"
