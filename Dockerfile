# onagent server 容器映像。
# build context 為「專案根目錄」,COPY 路徑相對根目錄寫(backend/...、apps/...)。
# backend/go.mod 的 github.com/tim72117/want 透過 GOPRIVATE + GH_PAT
# 從 GitHub 下載(見下方 build 階段),不依賴本地 want/ 源碼。
#
# 前端有兩個獨立 Vite 專案(apps/landing、apps/console),各自 build 後
# 複製進 backend/cmd/server/web/{landing,console}/,對應
# backend/cmd/server/web.go 裡 go:embed all:web/landing、all:web/console
# 期望的路徑 —— 路徑名稱必須完全一致,否則 embed 到的只會是 checked-in
# 的 placeholder index.html(見 web.go 檔頭註解)。
#
# 建置(從專案根目錄,需要 BuildKit):
#   DOCKER_BUILDKIT=1 docker build --secret id=gh_pat,env=GH_PAT -t onagent-server .
# 本機跑(env 由 --env-file 注入,不會把 .env 烤進映像):
#   docker run --rm -p 8080:8080 --env-file backend/.env onagent-server

# ---- 階段 1:build landing 前端 ----
FROM node:22-alpine AS landing-build
WORKDIR /web
COPY apps/landing/package.json apps/landing/package-lock.json ./
RUN npm ci
COPY apps/landing/ ./
RUN npm run build

# ---- 階段 2:build console 前端 ----
FROM node:22-alpine AS console-build
WORKDIR /web
COPY apps/console/package.json apps/console/package-lock.json ./
RUN npm ci
COPY apps/console/ ./
# 跟 landing-build 對稱:輸出到預設的 /web/dist/,對應下面的
# COPY --from=console-build /web/dist/.。(apps/console/vite.config.ts 已
# 移除自訂 outDir,回歸 dist,不再需要在這裡用 CLI flag 覆蓋。)
RUN npm run build

# ---- 階段 3:編譯 Go ----
FROM golang:1.26 AS build

# 用 BuildKit secret mount 而非 ARG:ARG/ENV 會把值烤進 image 的 layer
# history(docker history 或拆開 image 就看得到),secret mount 只在這個
# RUN 步驟執行期間以檔案形式存在,不會被任何 layer 記錄下來。
# 對應的建置指令:DOCKER_BUILDKIT=1 docker build --secret id=gh_pat,env=GH_PAT ...
RUN --mount=type=secret,id=gh_pat \
    git config --global url."https://$(cat /run/secrets/gh_pat)@github.com/".insteadOf "https://github.com/"

# 先單獨複製 go.mod / go.sum 以利 layer 快取(相依沒變時不重抓)。
COPY backend/go.mod backend/go.sum /src/backend/
RUN cd /src/backend && GOPRIVATE=github.com/tim72117/want go mod download

# 再複製完整源碼。
COPY backend/ /src/backend/

# 把兩個前端 dist 放到 web.go 期望的 embed 路徑(見檔頭註解的
# cp -r apps/landing/dist/. backend/cmd/server/web/landing/ 等指令)。
# 用 rm -rf 先清掉 checked-in 的 placeholder index.html,避免殘留檔案
# 混進真正的 build 產物。
RUN rm -rf /src/backend/cmd/server/web/landing/* /src/backend/cmd/server/web/console/*
COPY --from=landing-build /web/dist/. /src/backend/cmd/server/web/landing/
COPY --from=console-build /web/dist/. /src/backend/cmd/server/web/console/

# 靜態編譯:關 CGO 產出不依賴 libc 的單一執行檔,可放進極小的 base image。
RUN cd /src/backend && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
    -o /out/server ./cmd/server

# ---- 階段 4:執行 ----
# distroless:只含執行檔需要的最小 runtime,無 shell、體積小、攻擊面小。
# 內含 CA 憑證,連 Cloud SQL(sslmode=require)的 TLS 才驗得過。
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/server /app/server

# Cloud Run 會注入 PORT(預設 8080);main.go 讀 ADDR 覆寫監聽位址。
EXPOSE 8080
ENTRYPOINT ["/app/server"]
