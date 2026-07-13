#!/usr/bin/env bash
# =============================================================================
# onagent — GCP 一次性建置腳本(bootstrap）
#
# ⚠️⚠️⚠️  警告 — 執行前務必詳讀 ⚠️⚠️⚠️
#
#   這支腳本會建立「真實、會計費」的雲端資源(GCP 專案、Artifact Registry、
#   Secret Manager secrets、Cloud Run domain mapping 等)。
#
#   - 這不是 dry-run,執行下面每一條指令都會真的打到 GCP。
#   - 執行前請先整份讀過一遍,確認每個步驟你都理解、都想做。
#   - PROJECT_ID 目前是 placeholder(onagent-prod),下面「EDIT THIS FIRST」
#     區塊沒有改到你自己實際要用的專案 ID 之前,不要執行這支腳本。
#   - 這支腳本只建立「空的」Secret Manager secret 容器,不會寫入任何真實
#     機密值 —— 值要你自己另外用 `echo -n '...' | gcloud secrets versions add`
#     等方式手動加入(見腳本尾端說明),不要把真實密碼寫進這個檔案或 git。
#
# =============================================================================

set -euo pipefail

# -----------------------------------------------------------------------------
# EDIT THIS FIRST — 這是 placeholder,換成你自己要用的專案 ID。
# GCP 專案 ID 全域唯一,onagent-prod 這個名字很可能已經被別人用掉,
# 建議換成類似 onagent-prod-<你的隨機字串> 的名稱。
# -----------------------------------------------------------------------------
PROJECT_ID="onagent-prod"          # EDIT THIS FIRST
BILLING_ACCOUNT_ID=""              # EDIT THIS FIRST — gcloud billing accounts list
REGION="asia-east1"
SERVICE_NAME="onagent-server"
DOMAIN="agent.shuttle.tools"
AR_REPO="cloud-run-source-deploy"

echo "=============================================="
echo " onagent GCP bootstrap"
echo " PROJECT_ID = ${PROJECT_ID}"
echo " REGION     = ${REGION}"
echo "=============================================="
echo
read -r -p "確定要用上面這個 PROJECT_ID 建立真實資源嗎？輸入 yes 繼續: " CONFIRM
if [[ "${CONFIRM}" != "yes" ]]; then
  echo "已取消。"
  exit 1
fi

# -----------------------------------------------------------------------------
# 1. 建立 GCP 專案
# -----------------------------------------------------------------------------
gcloud projects create "${PROJECT_ID}" --name="onagent"

if [[ -n "${BILLING_ACCOUNT_ID}" ]]; then
  gcloud billing projects link "${PROJECT_ID}" \
    --billing-account="${BILLING_ACCOUNT_ID}"
else
  echo "⚠️  BILLING_ACCOUNT_ID 未設定，請手動到 Console 幫這個專案掛上 billing account，"
  echo "   否則下面啟用 API / 建立資源的步驟會失敗。"
fi

gcloud config set project "${PROJECT_ID}"

# -----------------------------------------------------------------------------
# 2. 啟用必要 API
# -----------------------------------------------------------------------------
gcloud services enable \
  run.googleapis.com \
  artifactregistry.googleapis.com \
  secretmanager.googleapis.com \
  cloudbuild.googleapis.com \
  --project="${PROJECT_ID}"

# -----------------------------------------------------------------------------
# 3. 建立 Artifact Registry repo(cloudbuild.yaml / GitHub Actions 推 image 用）
# -----------------------------------------------------------------------------
gcloud artifacts repositories create "${AR_REPO}" \
  --repository-format=docker \
  --location="${REGION}" \
  --description="onagent Cloud Run 部署用 image repo" \
  --project="${PROJECT_ID}"

# -----------------------------------------------------------------------------
# 4. 建立空的 Secret Manager secrets(容器而已，不寫入真實值）
#
#    之後手動填值，例如：
#      echo -n "postgres://user:pass@host/db?sslmode=require" | \
#        gcloud secrets versions add DATABASE_URL --data-file=- --project="${PROJECT_ID}"
#      echo -n "https://example-developer-site.com,https://another-site.com" | \
#        gcloud secrets versions add ALLOWED_ORIGINS --data-file=- --project="${PROJECT_ID}"
#      echo -n "ghp_xxxxxxxxxxxxxxxxxxxx" | \
#        gcloud secrets versions add GH_PAT --data-file=- --project="${PROJECT_ID}"
# -----------------------------------------------------------------------------
for SECRET_NAME in DATABASE_URL ALLOWED_ORIGINS GH_PAT; do
  gcloud secrets create "${SECRET_NAME}" \
    --replication-policy="automatic" \
    --project="${PROJECT_ID}" \
    || echo "secret ${SECRET_NAME} 可能已存在，略過"
done

echo
echo "⚠️  以上只建立了空的 secret 容器，尚未寫入任何真實值。"
echo "   請自行用 'gcloud secrets versions add' 補上 DATABASE_URL / ALLOWED_ORIGINS / GH_PAT 的實際內容。"
echo

# -----------------------------------------------------------------------------
# 5. Cloud Run domain mapping（前提：service 至少已成功部署過一次，
#    否則 domain-mappings create 會失敗；先跑過 cloudbuild.yaml 或
#    .github/workflows/deploy-cloudrun.yml 部署一次 onagent-server 再執行這步）
# -----------------------------------------------------------------------------
gcloud beta run domain-mappings create \
  --service="${SERVICE_NAME}" \
  --domain="${DOMAIN}" \
  --region="${REGION}" \
  --project="${PROJECT_ID}"

echo
echo "=============================================="
echo " domain mapping 已建立（或已存在）。"
echo " 接下來請執行："
echo
echo "   gcloud beta run domain-mappings describe \\"
echo "     --domain=${DOMAIN} --region=${REGION} --project=${PROJECT_ID}"
echo
echo " 取得實際要在 Namecheap 新增的 DNS 記錄內容，"
echo " 詳細步驟見 docs/deployment.md。"
echo "=============================================="
