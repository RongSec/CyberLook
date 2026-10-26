# ---------- 前端构建 ----------
FROM node:22-alpine AS web-builder
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm config set registry https://registry.npmmirror.com && npm install --no-audit --no-fund
COPY web/ ./
RUN npm run build

# ---------- 后端构建 ----------
FROM golang:1.26-alpine AS go-builder
ENV GOPROXY=https://goproxy.cn,direct
ENV CGO_ENABLED=0
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -trimpath -ldflags="-s -w" -o /out/asset-server ./cmd/server && \
    GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /out/scope-agent-linux ./cmd/agent && \
    GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o /out/scope-agent-linux-arm64 ./cmd/agent && \
    GOOS=windows GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /out/scope-agent-windows.exe ./cmd/agent

# ---------- 运行时 ----------
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates tzdata chromium fonts-wqy-zenhei && rm -rf /var/lib/apt/lists/*
ENV TZ=Asia/Shanghai
WORKDIR /app

COPY --from=go-builder /out/asset-server /app/asset-server
COPY --from=web-builder /web/dist /app/web/dist
COPY migrations /app/migrations
# 分发的 agent 二进制与扫描引擎（供一键加入脚本下载）
COPY --from=go-builder /out/scope-agent-linux /app/agent/scope-agent-linux
COPY --from=go-builder /out/scope-agent-linux-arm64 /app/agent/scope-agent-linux-arm64
COPY --from=go-builder /out/scope-agent-windows.exe /app/agent/scope-agent-windows.exe
COPY third_party/tscanclient_linux/TscanClient_Linux_Amd64 /app/tscan/TscanClient_Linux
COPY third_party/tscanclient_win/TscanClient_Win.exe /app/tscan/TscanClient_Win.exe
COPY third_party/tscanclient_linux/TscanClient_Linux_Amd64 /app/third_party/tscanclient_linux/TscanClient
RUN chmod +x /app/third_party/tscanclient_linux/TscanClient /app/tscan/TscanClient_Linux /app/agent/scope-agent-linux

EXPOSE 8080
CMD ["/app/asset-server"]
