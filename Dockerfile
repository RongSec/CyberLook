# 基础镜像可替换：国内内网经常连不上 docker.io，把这三个参数指向私有 registry / 镜像源即可，
# 例如 --build-arg GO_IMAGE=docker.m.daocloud.io/library/golang:1.26-alpine
ARG NODE_IMAGE=node:22-alpine
ARG GO_IMAGE=golang:1.26-alpine
ARG RUNTIME_IMAGE=debian:bookworm-slim

# ---------- 前端构建 ----------
FROM ${NODE_IMAGE} AS web-builder
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm config set registry https://registry.npmmirror.com && npm install --no-audit --no-fund
COPY web/ ./
RUN npm run build

# ---------- 后端构建 ----------
FROM ${GO_IMAGE} AS go-builder
# 版本号由构建参数注入（CI 传 tag，本地构建传 APP_VERSION）
ARG VERSION=dev
# 分发的 agent 架构。默认全量（发布用）；
# 本地快速迭代传 --build-arg AGENT_ARCHES=amd64 —— 交叉编译 sqlite(modernc)
# 单架构就要几分钟，5 个架构在 4 核机器上会跑到 20~40 分钟。
ARG AGENT_ARCHES="amd64 arm64 386 mipsle windows"
ENV GOPROXY=https://goproxy.cn,direct
ENV CGO_ENABLED=0
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
# 精确 COPY：不再 COPY . .
# 之前 web/ 改一行也会让 Go 层缓存整层失效，前端小改动要重跑全架构交叉编译。
COPY cmd ./cmd
COPY internal ./internal
COPY migrations ./migrations
RUN set -eux; \
    go build -trimpath -ldflags="-s -w -X asset-platform/internal/version.Version=${VERSION#v}" -o /out/asset-server ./cmd/server; \
    for a in ${AGENT_ARCHES}; do \
      case "$a" in \
        windows) GOOS=windows GOARCH=amd64 go build -trimpath -ldflags="-s -w -X asset-platform/internal/version.Version=${VERSION#v}" -o /out/scope-agent-windows.exe ./cmd/agent ;; \
        mipsle)  GOOS=linux GOARCH=mipsle GOMIPS=softfloat go build -trimpath -ldflags="-s -w -X asset-platform/internal/version.Version=${VERSION#v}" -o /out/scope-agent-linux-mipsle ./cmd/agent ;; \
        *)       GOOS=linux GOARCH=$a go build -trimpath -ldflags="-s -w -X asset-platform/internal/version.Version=${VERSION#v}" -o /out/scope-agent-linux-$a ./cmd/agent ;; \
      esac; \
    done

# ---------- 运行时 ----------
FROM ${RUNTIME_IMAGE}
# 运行时基础镜像（debian:bookworm-slim）。换成内网私有源时同样用 RUNTIME_IMAGE。
# apt 源可换：国内网络直连 deb.debian.org 经常卡在 Packages 索引上（反复 Ign/超时），
# 一次抖动就让整次构建作废。CI 在境外不需要换，本地构建传
#   --build-arg APT_MIRROR=mirrors.aliyun.com
# 即可，默认行为与官方源完全一致。init.sh 会探测并自动写入 .env。
ARG APT_MIRROR=deb.debian.org
# 备份用的 pg_dump 必须与服务端同大版本：Debian 12 自带的 postgresql-client 是 15.x，
# 而线上库是 16.x，直接 pg_dump 会 "aborting because of server version mismatch"
# （实测 2026-11-12 之后所有自动备份都在静默失败）。这里从 PGDG 补一个 16 版客户端。
# 重试是必需的：deb.debian.org 在国内常返回 502（实测一次构建有 7 个包 502 直接废掉整层），
# apt 自身默认不重试，所以显式给 Acquire::Retries 并在整层失败时再整体补一次。
RUN set -eux; \
    if [ -f /etc/apt/sources.list.d/debian.sources ]; then \
      sed -i "s|deb.debian.org|${APT_MIRROR}|g" /etc/apt/sources.list.d/debian.sources; \
    else \
      sed -i "s|deb.debian.org|${APT_MIRROR}|g" /etc/apt/sources.list; \
    fi; \
    aptg() { apt-get -o Acquire::Retries=6 -o Acquire::http::Timeout=30 -o Acquire::https::Timeout=30 "$@"; }; \
    aptg update; \
    aptg install -y --no-install-recommends ca-certificates tzdata chromium fonts-wqy-zenhei gnupg curl \
      || { echo "首次安装失败，重试一次"; aptg update; aptg install -y --no-install-recommends ca-certificates tzdata chromium fonts-wqy-zenhei gnupg curl; }; \
    . /etc/os-release; \
    echo "deb [signed-by=/usr/share/keyrings/pgdg.gpg] https://apt.postgresql.org/pub/repos/apt ${VERSION_CODENAME}-pgdg main" > /etc/apt/sources.list.d/pgdg.list; \
    curl -fsSL --retry 5 --retry-delay 2 https://www.postgresql.org/media/keys/ACCC4CF8.asc | gpg --dearmor --yes -o /usr/share/keyrings/pgdg.gpg; \
    aptg update; \
    aptg install -y --no-install-recommends postgresql-client-16 \
      || { echo "pgdg 安装失败，重试一次"; aptg update; aptg install -y --no-install-recommends postgresql-client-16; }; \
    rm -rf /var/lib/apt/lists/*
ENV TZ=Asia/Shanghai
WORKDIR /app

COPY --from=go-builder /out/asset-server /app/asset-server
COPY --from=web-builder /web/dist /app/web/dist
COPY migrations /app/migrations
# FingerprintHub 全量指纹规则（MIT）：web 3450 + service 11938
COPY data/fingerprint /app/data/fingerprint
# 分发的 agent 二进制（内置自研扫描引擎，纯 Go 单文件；供一键加入脚本/节点自升级下载）
# 用通配复制：本地构建可能只编了部分架构，缺哪个就少哪个，不影响启动
COPY --from=go-builder /out/ /tmp/build-out
RUN mkdir -p /app/agent \
 && cp /tmp/build-out/scope-agent-* /app/agent/ 2>/dev/null || true; \
    chmod +x /app/agent/scope-agent-linux-* 2>/dev/null || true; \
    rm -rf /tmp/build-out

EXPOSE 8080
CMD ["/app/asset-server"]
