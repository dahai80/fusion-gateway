#!/usr/bin/env bash
# start-gateway.sh — fusion-gateway lifecycle manager (start/stop/restart/status/log)
#
# Mirrors ~/claude-home/fusion-mlx/start.sh conventions:
#   - gateway binary + config live in this repo
#   - PID file + logs under the project dir
#   - waits for /health before declaring "started"
#
# Usage:
#   ./start-gateway.sh start    # build (if needed) + launch, wait for health
#   ./start-gateway.sh stop     # graceful SIGTERM, SIGKILL fallback
#   ./start-gateway.sh restart
#   ./start-gateway.sh status   # running? + health probe
#   ./start-gateway.sh log      # tail -f the log
#
# Env overrides:
#   FG_CONFIG   (default: config.yaml in repo root)
#   FG_HOST     (default: from script, 127.0.0.1)
#   FG_PORT     (default: 11432)
#   FG_START_MLX=1  also start fusion-mlx first (default: off — config.yaml
#                   has auto_start.enabled=false, mlx is usually already up)

set -euo pipefail

PROJ_DIR="$(cd "$(dirname "$0")" && pwd)"
BIN="${PROJ_DIR}/fusion-gateway"
CONFIG="${FG_CONFIG:-${PROJ_DIR}/config.yaml}"
PID_FILE="${PROJ_DIR}/.gateway.pid"
LOG_DIR="${PROJ_DIR}/logs"
LOG_FILE="${LOG_DIR}/gateway.log"

HOST="${FG_HOST:-127.0.0.1}"
PORT="${FG_PORT:-11432}"
BASE_URL="http://${HOST}:${PORT}"

mkdir -p "${LOG_DIR}"

log()  { printf '[gateway] %s\n' "$*" >&2; }
fail() { log "ERROR: $*"; exit 1; }

is_running() {
    [[ -f "${PID_FILE}" ]] || return 1
    local pid
    pid="$(cat "${PID_FILE}")"
    [[ -n "${pid}" ]] && kill -0 "${pid}" 2>/dev/null
}

health_ok() {
    curl -fsS -o /dev/null --max-time 2 "${BASE_URL}/health" 2>/dev/null
}

build_if_needed() {
    if [[ ! -x "${BIN}" ]] || find "${PROJ_DIR}" -name '*.go' -newer "${BIN}" -print -quit | grep -q .; then
        log "building (source newer than binary or binary missing)..."
        (cd "${PROJ_DIR}" && go build -o fusion-gateway ./cmd/gateway) || fail "build failed"
    fi
}

maybe_start_mlx() {
    if [[ "${FG_START_MLX:-0}" == "1" ]]; then
        log "starting fusion-mlx first (FG_START_MLX=1)..."
        ~/claude-home/fusion-mlx/start.sh start || log "WARN: fusion-mlx start failed, continuing"
    fi
}

wait_healthy() {
    local timeout="${1:-60}"
    local deadline=$((SECONDS + timeout))
    log "waiting for ${BASE_URL}/health (up to ${timeout}s)..."
    while (( SECONDS < deadline )); do
        if health_ok; then
            log "gateway is healthy at ${BASE_URL}"
            return 0
        fi
        sleep 1
    done
    log "WARN: health check timed out after ${timeout}s (gateway may still be initializing)"
    return 1
}

cmd_start() {
    if is_running; then
        local pid
        pid="$(cat "${PID_FILE}")"
        log "already running (pid ${pid})"
        health_ok && log "healthy at ${BASE_URL}" || log "WARN: process up but /health not OK yet"
        exit 0
    fi

    build_if_needed
    maybe_start_mlx

    log "starting gateway: ${BIN} --config ${CONFIG}"
    nohup "${BIN}" --config "${CONFIG}" >> "${LOG_FILE}" 2>&1 &
    local pid=$!
    echo "${pid}" > "${PID_FILE}"
    log "started (pid ${pid}), log: ${LOG_FILE}"

    wait_healthy 60 || log "check: ${LOG_FILE}"
}

cmd_stop() {
    if ! is_running; then
        log "not running (no live pid file)"
        rm -f "${PID_FILE}"
        exit 0
    fi
    local pid
    pid="$(cat "${PID_FILE}")"
    log "stopping (pid ${pid})..."
    kill "${pid}" 2>/dev/null || true
    # graceful drain: up to 30s for the 30s HTTP shutdown window
    for _ in $(seq 1 30); do
        kill -0 "${pid}" 2>/dev/null || break
        sleep 1
    done
    if kill -0 "${pid}" 2>/dev/null; then
        log "still up after 30s, sending SIGKILL"
        kill -9 "${pid}" 2>/dev/null || true
    fi
    rm -f "${PID_FILE}"
    log "stopped"
}

cmd_status() {
    if is_running; then
        local pid
        pid="$(cat "${PID_FILE}")"
        if health_ok; then
            log "running (pid ${pid}) — healthy at ${BASE_URL}"
        else
            log "running (pid ${pid}) — /health NOT ok"
        fi
    else
        log "stopped"
        exit 1
    fi
}

cmd_log() {
    [[ -f "${LOG_FILE}" ]] || fail "no log file yet: ${LOG_FILE}"
    tail -n 100 -f "${LOG_FILE}"
}

case "${1:-}" in
    start)   cmd_start ;;
    stop)    cmd_stop ;;
    restart) cmd_stop; sleep 1; cmd_start ;;
    status)  cmd_status ;;
    log)     cmd_log ;;
    *)
        sed -n '2,20p' "$0"
        exit 1
        ;;
esac
