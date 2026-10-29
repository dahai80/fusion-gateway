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

# #401 follow-up: the stop wait must outlast the gateway's own drain window
# (server.graceful_shutdown_timeout, 30s when unset) plus the ~10s
# persistence-sink flush that runs after it. A flat 35s SIGKILLed a healthy
# drain mid-flight and cut exactly the streams the drain exists to save.
stop_wait_seconds() {
    local drain
    drain="$(awk '/^server:/{f=1;next} /^[^ ]/{f=0} f && /graceful_shutdown_timeout:/{gsub(/[^0-9]/,"",$2); print $2; exit}' "$CONFIG" 2>/dev/null)"
    [ -n "$drain" ] || drain=30
    echo $((drain + 15))
}

# #401: adopt a gateway that is already serving on the port but was not
# started by this script (fusion-supervisor relaunch). Writes the pidfile so
# stop/status keep governing the real process. Only our own binary is adopted:
# anything else stays a foreign listener and is reported as such.
adopt_listener() {
    local pid cmd
    pid="$(listener_pid 2>/dev/null)" || return 1
    [ -n "$pid" ] || return 1
    cmd="$(ps -p "$pid" -o command= 2>/dev/null)" || return 1
    case "$cmd" in
        "$BINARY"*) ;;
        *) return 1 ;;
    esac
    echo "$pid" > "$PID_FILE"
    echo "Adopted running Fusion-Gateway (PID $pid, relaunched outside start.sh)"
    return 0
}

start() {
    if pid="$(get_pid)"; then
        echo "Fusion-Gateway already running (PID $pid)"
        return 0
    fi
    # #401: fusion-supervisor relaunches the gateway the instant it exits, so
    # `restart` (stop; start) can reach here while the replacement is still
    # coming up. Adopt it before treating the port as foreign-owned.
    if adopt_listener; then
        return 0
    fi
    # #356: a foreign process on the port makes the health probe lie — the
    # spawned child dies on bind failure while the foreign process answers
    # /healthz, so start() used to report "healthy" for a dead PID. Refuse
    # to start instead, naming the PID to kill.
    if foreign="$(listener_pid)" && [ -n "$foreign" ]; then
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
            # #401: our child lost the startup race (fusion-supervisor got
            # there first and now holds the instance lock, so the loser exits
            # with EX_TEMPFAIL). The winner is the gateway on the port — adopt
            # it rather than reporting a failure and leaving no pidfile, which
            # made `status` claim "not running" for a serving gateway.
            for _ in $(seq 1 10); do
                adopt_listener && return 0
                sleep 1
            done
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
                # #401: the preflight adoption ran before the spawn, so a
                # supervisor relaunch landing between the preflight and the
                # bind still leaves us with a loser child and no pidfile —
                # while a healthy gateway serves the port. Adopt it when the
                # listener is our own binary; a foreign one stays an error.
                if adopt_listener; then
                    return 0
                fi
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
    local wait_s
    wait_s="$(stop_wait_seconds)"
    echo "Stopping Fusion-Gateway (PID $pid, waiting up to ${wait_s}s for in-flight requests)..."
    kill -TERM "$pid" 2>/dev/null || true
    for _ in $(seq 1 "$wait_s"); do
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
    # #401: a serving gateway with no pidfile is "running", not "not running".
    if adopt_listener > /dev/null; then
        echo "Fusion-Gateway running (PID $(cat "$PID_FILE"), adopted: not started via start.sh)"
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
