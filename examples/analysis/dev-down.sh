#!/usr/bin/env bash
# =============================================================================
# analysis 範例：收掉 dev-up.sh 啟動的本地服務
#
#     用法：bash examples/analysis/dev-down.sh
# =============================================================================

set -uo pipefail

LOG_DIR="/tmp"

stop_service() {
  local name="$1" pidfile="$2"

  if [[ ! -f "${pidfile}" ]]; then
    echo "[${name}] 沒有找到 pidfile，略過（可能不是用 dev-up.sh 啟動的）"
    return
  fi

  local pid
  pid="$(cat "${pidfile}")"
  if kill -0 "${pid}" 2>/dev/null; then
    kill "${pid}"
    echo "[${name}] 已停止 (PID ${pid})"
  else
    echo "[${name}] PID ${pid} 已經不存在，略過"
  fi
  rm -f "${pidfile}"
}

stop_service "onagent-backend" "${LOG_DIR}/analysis-dev-onagent-backend.pid"
stop_service "onagent-console" "${LOG_DIR}/analysis-dev-console.pid"
stop_service "analysis-mock-backend" "${LOG_DIR}/analysis-dev-mock-backend.pid"
stop_service "analysis-frontend" "${LOG_DIR}/analysis-dev-frontend.pid"
