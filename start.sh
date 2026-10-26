#!/bin/bash
# Fusion-Gateway — start/stop/restart/status/log
#
# Issue #344: fusion-supervisor manages every fusion-* service through
# `bash <repo>/start.sh <cmd>`; the gateway had no start.sh, so the
# supervisor's crash-recovery loop failed with "脚本不存在" and the gateway
# stayed down until manually restarted.
#
# Secrets (FUSION_GW_PG_DSN etc.) load from a .env file next to this script
# (KEY=VALUE lines, never committed — .gitignore covers it).

set -u

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

PID_FILE="$SCRIPT_DIR/.fusion-gateway.pid"
LOG_DIR="$SCRIPT_DIR/logs"
STDOUT_LOG="$LOG_DIR/gateway.stdout.log"
BINARY="$SCRIPT_DIR/fusion-gateway"
CONFIG="${FUSION_GW_CONFIG:-$SCRIPT_DIR/config.yaml}"
HEALTH_URL="${FUSION_GW_HEALTH_URL:-http://127.0.0.1:11432/healthz}"

mkdir -p "$LOG_DIR"

# Load .env (FUSION_GW_PG_DSN etc.). Real env wins over the file.
if [ -f "$SCRIPT_DIR/.env" ]; then
    while IFS='=' read -r key value; do
        case "$key" in
            ''|\#*) continue ;;
        esac
        if [ -z "${!key:-}" ]; then
            export "$key=$value"
        fi
    done < "$SCRIPT_DIR/.env"
    echo "Loaded env from $SCRIPT_DIR/.env"
fi

get_pid() {
    if [ -f "$PID_FILE" ]; then
        local pid
        pid="$(cat "$PID_FILE" 2>/dev/null)"
        if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
            echo "$pid"
            return 0
        fi
        rm -f "$PID_FILE"
    fi
    return 1
}

# Preflight: persistence.enabled=true needs the DSN env, otherwise the
# gateway exits fatally at startup (by design — fail visibly). Refuse to
# start here with a loud message instead of a start/die crash loop.
check_persistence_dsn() {
    if [ ! -f "$CONFIG" ]; then
        echo "ERROR: config not found: $CONFIG" >&2
        exit 1
    fi
    local enabled
    enabled="$(awk '/^persistence:/{f=1;next} /^[^ ]/{f=0} f && /enabled:/{print $2; exit}' "$CONFIG")"
    if [ "$enabled" = "true" ] && [ -z "${FUSION_GW_PG_DSN:-}" ]; then
        echo "ERROR: persistence.enabled=true in $CONFIG but FUSION_GW_PG_DSN is empty." >&2
        echo "       Set it in $SCRIPT_DIR/.env (never committed) or export it before start." >&2
        exit 1
    fi
}

ensure_binary() {
    if [ ! -x "$BINARY" ]; then
        echo "Binary missing, building..."
        if ! go build -o "$BINARY" ./cmd/gateway; then
            echo "ERROR: build failed" >&2
            exit 1
        fi
    fi
}

start() {
    if pid="$(get_pid)"; then
        echo "Fusion-Gateway already running (PID $pid)"
        return 0
    fi
    check_persistence_dsn
    ensure_binary
    echo "Starting Fusion-Gateway (config: $CONFIG)..."
    nohup "$BINARY" --config "$CONFIG" >> "$STDOUT_LOG" 2>&1 &
    echo $! > "$PID_FILE"
    # Wait for /healthz up to 60s (auto_start of fusion-mlx can take a while)
    for _ in $(seq 1 60); do
        if ! kill -0 "$(cat "$PID_FILE")" 2>/dev/null; then
            echo "ERROR: process died during startup. Last log lines:" >&2
            tail -10 "$STDOUT_LOG" >&2
            rm -f "$PID_FILE"
            exit 1
        fi
        if curl -sf "$HEALTH_URL" > /dev/null 2>&1; then
            echo "Started (PID $(cat "$PID_FILE")) — healthy at $HEALTH_URL"
            return 0
        fi
        sleep 1
    done
    echo "WARNING: process alive but $HEALTH_URL not healthy after 60s — check $STDOUT_LOG"
    return 0
}

stop() {
    local pid
    if ! pid="$(get_pid)"; then
        echo "Fusion-Gateway is not running"
        return 0
    fi
    echo "Stopping Fusion-Gateway (PID $pid)..."
    kill -TERM "$pid" 2>/dev/null || true
    for _ in $(seq 1 35); do
        if ! kill -0 "$pid" 2>/dev/null; then
            rm -f "$PID_FILE"
            echo "Stopped (graceful)"
            return 0
        fi
        sleep 1
    done
    echo "Force stopping..."
    kill -9 "$pid" 2>/dev/null || true
    rm -f "$PID_FILE"
    echo "Stopped (forced)"
}

status() {
    local pid
    if pid="$(get_pid)"; then
        echo "Fusion-Gateway running (PID $pid)"
        if curl -sf "$HEALTH_URL" > /dev/null 2>&1; then
            echo "Health: OK ($HEALTH_URL)"
        else
            echo "Health: FAIL ($HEALTH_URL)"
        fi
        return 0
    fi
    echo "Fusion-Gateway is not running"
    return 1
}

log() {
    tail -n "${1:-50}" -f "$STDOUT_LOG"
}

case "${1:-start}" in
    start)   start ;;
    stop)    stop ;;
    restart) stop; start ;;
    status)  status ;;
    log)     shift; log "$@" ;;
    *)
        echo "Usage: $0 {start|stop|restart|status|log [N]}" >&2
        exit 1
        ;;
esac
