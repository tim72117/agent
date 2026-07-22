#!/usr/bin/env bash
# =============================================================================
# analysis 範例：收掉 dev-up.sh 啟動的本地服務
#
#     用法：bash examples/analysis/dev-down.sh
# =============================================================================

set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
LOG_DIR="${REPO_ROOT}/examples/analysis/tmp"

# is_process_alive / kill_process: on Windows Git Bash, bash's own
# kill/kill -0 operate on MSYS2's internal process tracking, which is a
# *different* numbering scheme than the real Windows PIDs netstat (and
# dev-up.sh's get_listening_pid) report — kill -0 <real-alive-windows-pid>
# reliably fails with "No such process" even though the process genuinely
# exists, which made every stop_service call here report a false "already
# gone" and never actually kill anything. tasklist/taskkill are the native
# tools that agree with what netstat captured the PID as. Mac/Linux (lsof
# present) keep using kill/kill -0, unaffected by this.
is_process_alive() {
  local pid="$1"
  if command -v lsof >/dev/null 2>&1; then
    kill -0 "${pid}" 2>/dev/null
  else
    tasklist //FI "PID eq ${pid}" 2>/dev/null | grep -q "${pid}"
  fi
}

kill_process() {
  local pid="$1"
  if command -v lsof >/dev/null 2>&1; then
    kill "${pid}"
  else
    taskkill //PID "${pid}" //T //F >/dev/null 2>&1
  fi
}

# get_listening_pid: mirrors dev-up.sh's helper of the same name (kept as a
# separate copy rather than sourced — these two scripts are meant to be
# runnable independently). Used as the fallback below when there's no
# pidfile to trust.
get_listening_pid() {
  local port="$1"
  if command -v lsof >/dev/null 2>&1; then
    lsof -ti ":${port}" -sTCP:LISTEN 2>/dev/null | head -1
  else
    netstat -ano 2>/dev/null | grep -E ":${port}[[:space:]]" | grep -i LISTENING | awk '{print $NF}' | head -1
  fi
}

stop_service() {
  local name="$1" pidfile="$2" port="$3"

  if [[ -f "${pidfile}" ]]; then
    local pid
    pid="$(cat "${pidfile}")"
    if is_process_alive "${pid}"; then
      kill_process "${pid}"
      echo "[${name}] 已停止 (PID ${pid})"
    else
      echo "[${name}] PID ${pid} 已經不存在，略過"
    fi
    rm -f "${pidfile}"
    return
  fi

  # No pidfile (e.g. it was never written, or something outside dev-up.sh/
  # dev-down.sh already killed the tracked PID directly) — fall back to
  # whatever's actually listening on this service's known port right now,
  # rather than giving up.
  local port_pid
  port_pid="$(get_listening_pid "${port}")"
  if [[ -n "${port_pid}" ]]; then
    kill_process "${port_pid}"
    echo "[${name}] 沒有 pidfile，改用 port ${port} 找到 PID ${port_pid}，已強制關閉"
  else
    echo "[${name}] 沒有找到 pidfile，port ${port} 也沒有被占用，略過"
  fi
}

stop_service "onagent-backend" "${LOG_DIR}/analysis-dev-onagent-backend.pid" 8081
stop_service "onagent-console" "${LOG_DIR}/analysis-dev-console.pid" 5173
stop_service "analysis-frontend" "${LOG_DIR}/analysis-dev-frontend.pid" 5175
