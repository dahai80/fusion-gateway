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
    # #401: also rebuild when the source is newer than the binary. Without
    # this, `start.sh restart` after a `git pull` kept running the previous
    # binary while reporting a successful restart.
    if [ ! -x "$BINARY" ]; then
        echo "Binary missing, building..."
    elif find "$SCRIPT_DIR" -name '*.go' -newer "$BINARY" -print -quit | grep -q .; then
        echo "Source newer than binary, rebuilding..."
    else
        return 0
    fi
    if ! go build -o "$BINARY" ./cmd/gateway; then
        echo "ERROR: build failed" >&2
        exit 1
    fi
}

# Preflight: validate the config before launching. The binary self-heals a
# corrupt config.yaml from config.yaml.lastgood (--check-config runs the same
# recovery), so this fails only when recovery is impossible too. Exit 78
# (EX_CONFIG) so fusion-supervisor can distinguish config-fatal from a crash.
check_config() {
    if ! "$BINARY" --check-config "$CONFIG"; then
        echo "ERROR: config check failed for $CONFIG" >&2
        echo "       The gateway auto-restores from $CONFIG.lastgood when possible." >&2
        echo "       Fix the config (see config.yaml.corrupt-* quarantined copies) and retry." >&2
        exit 78
    fi
}

# Port the gateway serves on, derived from HEALTH_URL (default 11432).
gw_port() {
    echo "$HEALTH_URL" | sed -n 's|.*:\([0-9][0-9]*\)/.*|\1|p'
}

# PID currently listening on the port. -sTCP:LISTEN is mandatory — bare
# `lsof -ti :PORT` also matches outbound sockets and returns the wrong PID.
listener_pid() {
    lsof -ti ":$(gw_port)" -sTCP:LISTEN 2>/dev/null | head -1
}

start() {
    if pid="$(get_pid)"; then
        echo "Fusion-Gateway already running (PID $pid)"
        return 0
    fi
    # #356: a foreign process on the port makes the health probe lie — the
    # spawned child dies on bind failure while the foreign process answers
    # /healthz, so start() used to report "healthy" for a dead PID. Refuse
    # to start instead, naming the PID to kill.
    if foreign="$(listener_pid)" && [ -n "$foreign" ]; then
        # #401: fusion-supervisor relaunches the gateway immediately after a
        # manual stop, so `restart` raced a process that was already running
        # the binary we just built. Adopt it instead of reporting the port as
        # foreign-owned.
        case "$(ps -p "$foreign" -o command= 2>/dev/null)" in
            "$BINARY"*)
                echo "$foreign" > "$PID_FILE"
                echo "Adopted running Fusion-Gateway (PID $foreign, relaunched outside start.sh)"
                return 0
                ;;
        esac
        echo "ERROR: port $(gw_port) is already held by PID $foreign (not started via start.sh)." >&2
        echo "       Stop it first: kill $foreign" >&2
        exit 1
    fi
    check_persistence_dsn
    ensure_binary
    check_config
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
            # #356: health answered — confirm OUR child is the listener, not
            # a process that grabbed the port between preflight and bind.
            local child listener
            child="$(cat "$PID_FILE")"
            listener="$(listener_pid)"
            if [ "$listener" != "$child" ]; then
                echo "ERROR: $HEALTH_URL answered but the listener is PID ${listener:-?}, not the spawned PID $child." >&2
                echo "       The child likely failed to bind. Kill ${listener:-the foreign PID} and retry." >&2
                exit 1
            fi
            echo "Started (PID $child) — healthy at $HEALTH_URL"
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
            warn_foreign_listener
            return 0
        fi
        sleep 1
    done
    echo "Force stopping..."
    kill -9 "$pid" 2>/dev/null || true
    rm -f "$PID_FILE"
    echo "Stopped (forced)"
    warn_foreign_listener
}

# #356: after stop, the port may still be held by an externally started
# process the pidfile never governed — surface it so the next start()'s
# preflight failure is not a surprise.
warn_foreign_listener() {
    local foreign
    if foreign="$(listener_pid)" && [ -n "$foreign" ]; then
        echo "WARNING: port $(gw_port) is still held by PID $foreign (not started via start.sh)." >&2
        echo "         Stop it first: kill $foreign" >&2
    fi
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
    # Build before stopping: fusion-supervisor relaunches the moment the
    # process exits, so stopping first would let it resurrect the old binary.
    restart) ensure_binary; stop; start ;;
    status)  status ;;
    log)     shift; log "$@" ;;
    *)
        echo "Usage: $0 {start|stop|restart|status|log [N]}" >&2
        exit 1
        ;;
esac
