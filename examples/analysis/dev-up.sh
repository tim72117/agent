#!/usr/bin/env bash
# =============================================================================
# analysis 範例：一次啟動三個相關本地服務
#
#   1. onagent 平台後端       (backend/cmd/server)       :8080
#   2. onagent console dev server (apps/console)          :5173
#   3. analysis 前端 dev server (examples/analysis)          :5175
#
# 不處理資料庫 —— 假設 onagent 平台後端需要的 Postgres 已經另外在跑，這支
# 腳本不會啟動/檢查它。
#
# 每個服務背景執行，log 分別寫到 /tmp/analysis-dev-*.log，PID 存在
# /tmp/analysis-dev-*.pid，方便之後用 dev-down.sh 或手動 kill 收掉。
# 啟動前會先檢查對應 port 有沒有已經被占用，占用就跳過該服務、不會重複啟動
# 一個新的（重複啟動會像疊加多個 process 搶同一個 port，徒增混亂）。
#
# 前端可選要連本機 mock 環境還是正式環境（examples/analysis 的
# .env / .env.production，見 Vite 的 --mode）。用 Vite 自己的預設 mode 名稱
# （development/production），不要自創名字 —— 曾經試過用 "local"，Vite 直接
# 拒絕啟動："local" cannot be used as a mode name because it conflicts with
# the .local postfix for .env files"，這是 Vite 保留字，不是這支腳本能繞過的。
#   --mode development   （預設）前端連本機的 onagent-backend + mock 後端
#   --mode production     前端改連 wss://agent.shuttle.tools/ws（.env.production），
#                          這種情況下本機的 onagent-backend 用不到，不會啟動它
#                          （反正本機沒有 Postgres 給它連，啟動也只會失敗）。
#
#     用法：bash examples/analysis/dev-up.sh [--mode development|production]
#     收掉：bash examples/analysis/dev-down.sh
# =============================================================================

set -uo pipefail  # 不用 -e：單一服務啟動失敗不該讓整支腳本中止，其餘服務仍要嘗試启动

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
LOG_DIR="${REPO_ROOT}/examples/analysis/tmp"
mkdir -p "${LOG_DIR}"

MODE="development"
while [[ $# -gt 0 ]]; do
  case "$1" in
    --mode)
      MODE="$2"
      shift 2
      ;;
    *)
      echo "unknown argument: $1" >&2
      exit 1
      ;;
  esac
done

case "${MODE}" in
  development|production) ;;
  *)
    echo "invalid --mode: ${MODE} (must be 'development' or 'production')" >&2
    exit 1
    ;;
esac

# get_listening_pid prints the PID of whatever's listening (not just
# connected — see the lsof flags below) on $1, or nothing if there isn't
# one. lsof isn't available on Windows Git Bash, so this falls back to
# parsing `netstat -ano` there; both paths only ever report a LISTEN-state
# socket, not a stale/closed connection that merely references the port
# (e.g. a dead client socket left behind by another process), which would
# otherwise make callers wrongly treat the port as in-use.
get_listening_pid() {
  local port="$1"
  if command -v lsof >/dev/null 2>&1; then
    lsof -ti ":${port}" -sTCP:LISTEN 2>/dev/null | head -1
  else
    netstat -ano 2>/dev/null | grep -E ":${port}[[:space:]]" | grep -i LISTENING | awk '{print $NF}' | head -1
  fi
}

is_port_in_use() {
  [[ -n "$(get_listening_pid "$1")" ]]
}

start_service() {
  local name="$1" port="$2" logfile="$3" pidfile="$4"
  shift 4

  if is_port_in_use "${port}"; then
    echo "[${name}] port ${port} 已被占用，略過啟動（可能已經在跑）"
    return
  fi

  ("$@" > "${logfile}" 2>&1 &)
  sleep 1
  local pid
  pid="$(get_listening_pid "${port}")"
  if [[ -n "${pid}" ]]; then
    echo "${pid}" > "${pidfile}"
    echo "[${name}] 已啟動 (PID ${pid})，port ${port}，log: ${logfile}"
  else
    echo "[${name}] 啟動後沒有偵測到 port ${port} 開始監聽，檢查 ${logfile} 確認錯誤訊息"
  fi
}

echo "=============================================="
echo " analysis 本地開發環境啟動中... (mode: ${MODE})"
echo "=============================================="
echo

# -----------------------------------------------------------------------------
# 1. onagent 平台後端 (:8080) —— 只有 --mode development 才需要：production
#    模式下前端改連 wss://agent.shuttle.tools/ws（見
#    examples/analysis/.env.production），本機這份後端完全用不到，啟動了也只是空跑
#    （而且本機沒有它需要的 Postgres，啟動只會失敗），所以直接跳過。
# -----------------------------------------------------------------------------
if [[ "${MODE}" == "development" ]]; then
  (
    cd "${REPO_ROOT}/backend" && \
    start_service "onagent-backend" 8080 \
      "${LOG_DIR}/analysis-dev-onagent-backend.log" \
      "${LOG_DIR}/analysis-dev-onagent-backend.pid" \
      go run ./cmd/server
  )
else
  echo "[onagent-backend] --mode production：前端改連正式後端，略過本機啟動"
fi

# -----------------------------------------------------------------------------
# 2. onagent console dev server (:5173) —— 同樣只有 --mode development 需要：
#    這是拿來編輯 analysis-app 的 tool schema（在 onagent-backend 的資料庫
#    裡）用的，production 模式下要編輯的是正式環境的資料，本機這份 console
#    連的是本機後端，編輯了也不會反映到正式環境，意義不大，所以跳過。
#    apps/console/package.json 的 dev script 預設就是 :5173（Vite 預設
#    port），沒有另外指定。
# -----------------------------------------------------------------------------
if [[ "${MODE}" == "development" ]]; then
  (
    cd "${REPO_ROOT}/apps/console" && \
    start_service "onagent-console" 5173 \
      "${LOG_DIR}/analysis-dev-console.log" \
      "${LOG_DIR}/analysis-dev-console.pid" \
      npm run dev
  )
else
  echo "[onagent-console] --mode production：編輯本機 console 不會影響正式環境，略過啟動"
fi

# -----------------------------------------------------------------------------
# 3. analysis 前端 (:5175) —— npm run dev 用的是 vite.dev.config.js（不是
#    build 用、目前缺依賴的 vite.config.js，見 package.json 的 dev script）。
#    --mode production 讓 Vite 改讀 .env.production（真正的 AgentBridge 連線
#    目標）。問卷題目資料現在是 examples/analysis/data/questions.js 裡的靜態
#    資料（原本 mock 後端回應的快照），不再需要任何後端 API，所以不管哪個
#    mode 都不用額外啟動什麼來提供這份資料。
# -----------------------------------------------------------------------------
(
  cd "${REPO_ROOT}/examples/analysis" && \
  start_service "analysis-frontend" 5175 \
    "${LOG_DIR}/analysis-dev-frontend.log" \
    "${LOG_DIR}/analysis-dev-frontend.pid" \
    npm run dev -- --mode "${MODE}"
)

echo
echo "=============================================="
echo " 完成。analysis 前端：http://localhost:5175"
if [[ "${MODE}" == "development" ]]; then
  echo "       console：http://localhost:5173/app/"
fi
echo " 收掉全部服務：bash examples/analysis/dev-down.sh"
echo "=============================================="
