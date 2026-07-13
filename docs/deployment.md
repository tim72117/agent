# onagent Cloud Run 部署指南

本文件說明如何把 onagent（Go 後端 + `apps/landing`、`apps/console` 兩個
內嵌前端）部署到 Google Cloud Run，並掛上自訂網域
`agent.shuttle.tools`。

> ⚠️ 以下步驟會建立真實付費雲端資源
>
> GCP 專案、Artifact Registry、Cloud Run 服務、Secret Manager secrets 等
> 都是會計費的真實資源，不是模擬環境。執行 `deploy/setup.sh` 或本文任何
> `gcloud` 指令前，請先整份讀過一遍，確認每一步都是你真的想做的事。

## 架構總覽

- **Dockerfile**（專案根目錄）：多階段 build
  1. build `apps/landing`（`npm ci && npm run build`）
  2. build `apps/console`（`npm ci && npm run build`）
  3. 把兩者的 `dist/` 複製進
     `backend/cmd/server/web/landing/`、`backend/cmd/server/web/console/`
     —— 這兩個路徑是 `backend/cmd/server/web.go` 用
     `//go:embed all:web/landing`、`//go:embed all:web/console`
     寫死的路徑，複製目的地名稱必須完全一致，否則編出來的執行檔裡嵌的
     只會是 checked-in 的 placeholder `index.html`。
  4. 編譯 Go（`CGO_ENABLED=0`，`GOPRIVATE=github.com/tim72117/want` +
     `GH_PAT` 抓私有模組)
  5. Runtime image 用 `gcr.io/distroless/static-debian12:nonroot`
- **cloudbuild.yaml**：Cloud Build 用，build image → push 到
  Artifact Registry → `gcloud run deploy`
- **.github/workflows/deploy-cloudrun.yml**：push 到 `main` 時自動走
  GitHub Actions，用 Workload Identity Federation（WIF）認證，不需要
  在 GitHub 存放長期 service account JSON key
- **deploy/setup.sh**：一次性的 GCP 專案 / API / secrets / domain
  mapping 建置腳本
- Console 前端現在跟後端同源（`/app` 路徑），所以**不需要**
  `CONSOLE_ORIGIN` 環境變數；`ALLOWED_ORIGINS` 是給第三方開發者自己的
  網站連 `/ws` WebSocket 用的白名單，跟 console 無關。

## 前置需求

- 已安裝並登入 `gcloud` CLI（`gcloud auth login`）
- 有權限建立新 GCP 專案、掛 billing account
- 一組可存取 `github.com/tim72117/want` 私有 repo 的 GitHub Personal
  Access Token（`GH_PAT`）
- Namecheap 帳號可管理 `shuttle.tools` 網域的 DNS

## 部署步驟

### 1. 建立 GCP 專案與基礎資源

編輯 `deploy/setup.sh` 開頭「EDIT THIS FIRST」區塊：

- `PROJECT_ID`：目前是 placeholder `onagent-prod`。GCP 專案 ID
  全域唯一，這個名字很可能已被別人用掉，請換成你自己的（例如
  `onagent-prod-<random>`）。
- `BILLING_ACCOUNT_ID`：用 `gcloud billing accounts list` 查詢。

確認內容後執行：

```bash
bash deploy/setup.sh
```

這支腳本會依序：

1. 建立 GCP 專案、掛 billing account
2. 啟用 `run`、`artifactregistry`、`secretmanager`、`cloudbuild` API
3. 建立 Artifact Registry docker repo
4. 建立**空的** Secret Manager secrets 容器（`DATABASE_URL`、
   `ALLOWED_ORIGINS`、`GH_PAT`）—— 不含真實值
5. 呼叫 `gcloud beta run domain-mappings create`（前提是 Cloud Run
   service 已經至少成功部署過一次，見步驟 3）

> 腳本第一次執行時，步驟 5（domain mapping）大概率會失敗，因為
> `onagent-server` 這個 Cloud Run service 還不存在。這是預期行為 ——
> 先完成步驟 2、3 部署出一版 service 後，再單獨重跑 domain-mappings
> create 那條指令即可（見下方步驟 4）。

### 2. 填入真實 secret 值

`deploy/setup.sh` 只建立空的 secret 容器，真實值要自己手動加入，
**不要**把它們寫進任何會進 git 的檔案：

```bash
echo -n "postgres://user:pass@host/db?sslmode=require" | \
  gcloud secrets versions add DATABASE_URL --data-file=- --project=<你的 PROJECT_ID>

echo -n "https://example-developer-site.com,https://another-site.com" | \
  gcloud secrets versions add ALLOWED_ORIGINS --data-file=- --project=<你的 PROJECT_ID>

echo -n "ghp_xxxxxxxxxxxxxxxxxxxx" | \
  gcloud secrets versions add GH_PAT --data-file=- --project=<你的 PROJECT_ID>
```

### 3. 設定 GitHub Actions 部署

編輯 `.github/workflows/deploy-cloudrun.yml` 開頭 `env:` 區塊，把
`GCP_PROJECT_ID`（目前是 placeholder `onagent-prod`）與 `IMAGE` 裡的
專案 ID 換成你自己的。

在 GitHub repo 設定以下 secrets（Settings → Secrets and variables →
Actions）：

- `WIF_PROVIDER`：Workload Identity Federation provider 資源名稱
- `WIF_SERVICE_ACCOUNT`：有權限 push image / 部署 Cloud Run 的
  service account email
- `GH_PAT`：同上，用來讓 Docker build 階段抓 `github.com/tim72117/want`

WIF 的 provider / service account 需要你自己在 GCP 專案裡建立
（`gcloud iam workload-identity-pools` 系列指令），本文件不重複展開，
可參考
[google-github-actions/auth](https://github.com/google-github-actions/auth)
官方文件。

設定完成後，push 到 `main` 分支且變更到 `backend/**`、`apps/**`、
`Dockerfile` 等路徑就會自動觸發部署，也可以用 GitHub Actions 的
`workflow_dispatch` 手動觸發。

### 4. 首次部署 Cloud Run service

第一次部署可以直接推一個 commit 到 `main` 觸發 GitHub Actions，或手動跑：

```bash
gcloud builds submit --config=cloudbuild.yaml --project=<你的 PROJECT_ID>
```

部署完成後，用以下指令確認 service 正常：

```bash
gcloud run services describe onagent-server \
  --region=asia-east1 --project=<你的 PROJECT_ID> \
  --format='value(status.url)'
```

打開這個 URL，確認 `/healthz` 回 `ok`，`/` 顯示 landing page，`/app`
顯示 console。

### 5. 建立網域對應（domain mapping）

service 部署成功後，執行（或重跑 `deploy/setup.sh` 裡對應那段）：

```bash
gcloud beta run domain-mappings create \
  --service=onagent-server \
  --domain=agent.shuttle.tools \
  --region=asia-east1 \
  --project=<你的 PROJECT_ID>
```

### 6. 到 Namecheap 手動新增 DNS 記錄

**重要：DNS 是在 Namecheap 管理，不是 Cloud DNS，本文件/腳本都無法幫你自動加這筆記錄，必須手動操作。**

先跑這條指令，取得 Cloud Run 實際要求的 DNS 記錄內容：

```bash
gcloud beta run domain-mappings describe \
  --domain=agent.shuttle.tools \
  --region=asia-east1 \
  --project=<你的 PROJECT_ID>
```

輸出的 `status.resourceRecords` 裡會列出實際要設定的記錄類型
（通常是一筆 `CNAME`，對應到類似 `ghs.googlehosted.com.` 這樣的目標，
但**確切的值請以這次指令的實際輸出為準，不要照抄本文件的任何範例值，
不同狀態、不同 Cloud Run 版本回傳的目標值可能不同**）。

拿到實際記錄內容後：

1. 登入 Namecheap → Domain List → 找到 `shuttle.tools` → Manage
2. 切到 **Advanced DNS** 分頁
3. Add New Record，Host 填 `agent`（對應 `agent.shuttle.tools`），
   Type / Value 依照上一步指令輸出的實際內容填寫
4. 儲存後等待 DNS 生效（通常幾分鐘到數小時不等，視 TTL 而定）

生效後可用以下指令確認 mapping 狀態變成 `Ready`：

```bash
gcloud beta run domain-mappings describe \
  --domain=agent.shuttle.tools \
  --region=asia-east1 \
  --project=<你的 PROJECT_ID> \
  --format='value(status.conditions)'
```

以及直接用瀏覽器開 `https://agent.shuttle.tools` 確認 HTTPS 憑證
（Cloud Run 網域對應會自動簽發 Google-managed 憑證）已經生效。

## 環境變數 / secrets 一覽

| 名稱 | 來源 | 說明 |
|---|---|---|
| `APP_ENV` | `--update-env-vars` | 設為 `production`，讓 main.go 對缺漏設定改成直接拒絕啟動，而不是印警告後繼續跑 |
| `COOKIE_SECURE` | `--update-env-vars` | 設為 `true`，session cookie 只透過 HTTPS 傳送 |
| `DATABASE_URL` | Secret Manager | Postgres 連線字串（例如 Neon，`sslmode=require`） |
| `ALLOWED_ORIGINS` | Secret Manager | 逗號分隔的來源網址白名單，給**第三方開發者自己的網站**連 `/ws` WebSocket、以及呼叫 `/console/*`、`/auth/*` 用；與 console 本身無關（console 現在走同源 `/app`，不需要 `CONSOLE_ORIGIN`） |
| `ADDR` | 不需設定 | main.go 預設 `:8080`，符合 Cloud Run 的 `PORT=8080` 慣例，通常不需要覆寫 |
| `GH_PAT` | Secret Manager（Cloud Build）／ GitHub Actions secret | 只在 build 階段使用，抓 `github.com/tim72117/want` 私有模組，不會進最終 runtime image |

## 常見問題

- **`/app` 或 `/` 顯示的還是 placeholder 頁面**：代表 Docker build 時
  `apps/landing/dist`、`apps/console/dist` 沒有正確複製到
  `backend/cmd/server/web/landing/`、`backend/cmd/server/web/console/`。
  檢查 Dockerfile 的 `COPY --from=landing-build` / `COPY --from=console-build`
  那兩行路徑是否跟 `backend/cmd/server/web.go` 檔頭註解一致。
- **`domain-mappings create` 一直失敗**：確認 Cloud Run service
  （`onagent-server`）已經部署成功且能正常回應，`domain-mappings`
  必須綁定一個已存在的 service。
- **HTTPS 憑證一直不生效**：確認 Namecheap 的 CNAME 記錄的 Host、Type、
  Value 跟 `domain-mappings describe` 輸出完全一致，且沒有其他衝突的
  `agent` 子網域記錄（例如舊的 A 記錄）殘留。
