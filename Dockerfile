# ---------- 前端构建 ----------
FROM node:22-alpine AS web-builder
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm config set registry https://registry.npmmirror.com && npm install --no-audit --no-fund
COPY web/ ./
RUN npm run build

# ---------- 后端构建 ----------
FROM golang:1.26-alpine AS go-builder
# 版本号由构建参数注入（CI 传 tag，本地构建传 APP_VERSION）
ARG VERSION=dev
ENV GOPROXY=https://goproxy.cn,direct
ENV CGO_ENABLED=0
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -trimpath -ldflags="-s -w -X asset-platform/internal/version.Version=${VERSION#v}" -o /out/asset-server ./cmd/server && \
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w -X asset-platform/internal/version.Version=${VERSION#v}" -o /out/scope-agent-linux-amd64 ./cmd/agent && \
    CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w -X asset-platform/internal/version.Version=${VERSION#v}" -o /out/scope-agent-linux-arm64 ./cmd/agent && \
    CGO_ENABLED=0 GOOS=linux GOARCH=386 go build -trimpath -ldflags="-s -w -X asset-platform/internal/version.Version=${VERSION#v}" -o /out/scope-agent-linux-386 ./cmd/agent && \
    CGO_ENABLED=0 GOOS=linux GOARCH=mipsle GOMIPS=softfloat go build -trimpath -ldflags="-s -w -X asset-platform/internal/version.Version=${VERSION#v}" -o /out/scope-agent-linux-mipsle ./cmd/agent && \
    CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags="-s -w -X asset-platform/internal/version.Version=${VERSION#v}" -o /out/scope-agent-windows.exe ./cmd/agent

# ---------- 运行时 ----------
FROM debian:bookworm-slim
# apt 源可换：国内网络直连 deb.debian.org 经常卡在 Packages 索引上（反复 Ign/超时），
# 一次抖动就让整次构建作废。CI 在境外不需要换，本地构建传
#   --build-arg APT_MIRROR=mirrors.aliyun.com
# 即可，默认行为与官方源完全一致。
ARG APT_MIRROR=deb.debian.org
RUN set -eux; \
    if [ -f /etc/apt/sources.list.d/debian.sources ]; then \
      sed -i "s|deb.debian.org|${APT_MIRROR}|g" /etc/apt/sources.list.d/debian.sources; \
    else \
      sed -i "s|deb.debian.org|${APT_MIRROR}|g" /etc/apt/sources.list; \
    fi; \
    apt-get update && apt-get install -y --no-install-recommends ca-certificates tzdata chromium fonts-wqy-zenhei postgresql-client && rm -rf /var/lib/apt/lists/*
ENV TZ=Asia/Shanghai
WORKDIR /app

COPY --from=go-builder /out/asset-server /app/asset-server
COPY --from=web-builder /web/dist /app/web/dist
COPY migrations /app/migrations
# FingerprintHub 全量指纹规则（MIT）：web 3450 + service 11938
COPY data/fingerprint /app/data/fingerprint
# 分发的 agent 二进制（内置自研扫描引擎，纯 Go 单文件；供一键加入脚本/节点自升级下载）
COPY --from=go-builder /out/scope-agent-linux-amd64 /app/agent/scope-agent-linux-amd64
COPY --from=go-builder /out/scope-agent-linux-arm64 /app/agent/scope-agent-linux-arm64
COPY --from=go-builder /out/scope-agent-linux-386 /app/agent/scope-agent-linux-386
COPY --from=go-builder /out/scope-agent-linux-mipsle /app/agent/scope-agent-linux-mipsle
COPY --from=go-builder /out/scope-agent-windows.exe /app/agent/scope-agent-windows.exe
# v0.0.27：不再分发 TscanClient（扫描引擎已内置于 scope-agent，单文件）。
# third_party/ 整目录移除：引擎二进制/POC 库/License 全部不再需要。
RUN chmod +x /app/agent/scope-agent-linux-* 2>/dev/null; true

EXPOSE 8080
CMD ["/app/asset-server"]
