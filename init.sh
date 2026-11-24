#!/bin/bash
# 部署初始化脚本：生成随机数据库密码 → 解析最新版本 → 拉起服务 → 等健康检查
# 用法: bash init.sh
# 幂等：重复执行只会跳过已存在的 .env，不会重置数据库密码。

set -e

echo "=== 未见 - 部署初始化 ==="

# ---------- 0. 前置检查 ----------
if ! command -v docker >/dev/null 2>&1; then
    echo "❌ 未找到 docker，请先安装 Docker Engine 24+ 与 compose 插件"
    exit 1
fi
if ! docker compose version >/dev/null 2>&1; then
    echo "❌ 未找到 docker compose 插件（v2），请安装 docker-compose-plugin"
    exit 1
fi
if ! docker info >/dev/null 2>&1; then
    echo "❌ 当前用户无法访问 docker daemon，请用 root 或加入 docker 组"
    exit 1
fi

# ---------- 1. 生成 .env（如果不存在） ----------
if [ ! -f .env ]; then
    # 生成 24 位随机密码（只含字母数字）
    PASSWORD=$(openssl rand -base64 32 | tr -dc 'a-zA-Z0-9' | head -c 24)
    cat > .env << EOF
# 自动生成，请勿提交到 git
# PostgreSQL 数据库密码（服务端连接数据库用，用户无需关心）
COMPOSE_PROJECT_NAME=asset-platform
HOST_PROJECT_DIR=$(pwd)
POSTGRES_PASSWORD=$PASSWORD

# JWT 密钥（留空则自动生成并持久化到 data/files/jwt.secret）
# JWT_SECRET=
EOF
    echo "✅ 已生成 .env（数据库密码已随机生成，无需手动配置）"
else
    echo "⏭️  .env 已存在，跳过生成"
fi

# ---------- 1.5 解析版本 ----------
# 优先取 GitHub 最新 release 的 tag（与镜像 tag 一致）；查不到则用 latest。
if [ -z "$(grep '^APP_VERSION=' .env 2>/dev/null)" ]; then
    LATEST=$(curl -sf --max-time 8 https://api.github.com/repos/RongSec/CyberLook/releases/latest 2>/dev/null \
        | grep -o '"tag_name": *"[^"]*"' | head -1 | sed 's/.*"\([^"]*\)"/\1/')
    if [ -n "$LATEST" ]; then
        echo "APP_VERSION=$LATEST" >> .env
        echo "🔖 最新版本: $LATEST"
    else
        echo "APP_VERSION=latest" >> .env
        echo "🔖 无法查询最新版本（网络受限），使用 latest"
    fi
fi
APP_VERSION=$(grep '^APP_VERSION=' .env | head -1 | cut -d= -f2)
echo "📌 目标版本: $APP_VERSION"

# 本地构建时只编当前架构的 agent 二进制（全量 5 架构要 20~40 分钟）
if [ -z "$(grep '^AGENT_ARCHES=' .env 2>/dev/null)" ]; then
    case "$(uname -m)" in
        x86_64|amd64) ARCH=amd64 ;;
        aarch64|arm64) ARCH=arm64 ;;
        *) ARCH=amd64 ;;
    esac
    echo "AGENT_ARCHES=$ARCH" >> .env
fi

# apt 源探测：deb.debian.org 在国内常返回 502，直接构建会在 chromium 那一层随机失败。
# 只探测一次，不通就换国内镜像；用户已在 .env 里写死 APT_MIRROR 时不覆盖。
if [ -z "$(grep '^APT_MIRROR=' .env 2>/dev/null)" ]; then
    CODE=$(curl -s -o /dev/null -m 6 -w '%{http_code}' http://deb.debian.org/debian/dists/bookworm/Release 2>/dev/null || echo 000)
    if [ "$CODE" = "200" ]; then
        echo "APT_MIRROR=deb.debian.org" >> .env
    else
        echo "APT_MIRROR=mirrors.aliyun.com" >> .env
        echo "🔀 deb.debian.org 不可用（$CODE），apt 源改用 mirrors.aliyun.com"
    fi
fi

# ---------- 2. 拉镜像；拉不到就地构建 ----------
# agent 服务固定走本地构建（release 只发布 server / updater 两个镜像）。
echo "🚀 拉取官方镜像（server / updater）..."
if docker compose pull server updater 2>/dev/null; then
    echo "✅ 官方镜像已就绪"
    echo "🔨 构建扫描节点镜像（首次约 2~5 分钟，需拉 golang 基础镜像）..."
    docker compose build agent
    echo "🚀 启动服务..."
    docker compose up -d --pull never
else
    echo "⚠️  官方镜像拉取失败（离线 / 私有网络 / ghcr 不可达）"
    echo "🔨 改为本地构建全部镜像（需 Docker 可拉 node/golang/debian 基础镜像，约 5~15 分钟）..."
    echo "   若基础镜像也拉不到，在 .env 里指向内网镜像源后重跑本脚本："
    echo "     NODE_IMAGE=<registry>/library/node:22-alpine"
    echo "     GO_IMAGE=<registry>/library/golang:1.26-alpine"
    echo "     RUNTIME_IMAGE=<registry>/library/debian:bookworm-slim"
    echo "     DOCKER_IMAGE=<registry>/library/docker:27-cli"
    docker compose up -d --build
fi

# ---------- 3. 等健康检查 ----------
echo "⏳ 等待服务就绪（最多 120 秒）..."
READY=0
for i in $(seq 1 40); do
    if curl -sf --max-time 3 http://localhost:18888/api/health >/dev/null 2>&1; then
        READY=1
        break
    fi
    sleep 3
done

echo ""
if [ "$READY" = "1" ]; then
    echo "=== 部署完成（$APP_VERSION）==="
else
    echo "=== 容器已启动，但健康检查未在 120 秒内通过 ==="
    echo "排查：docker compose ps && docker compose logs --tail=50 server"
fi
echo "访问地址: http://localhost:18888（或 http://<本机IP>:18888）"
echo "默认账号: admin"
echo "默认密码: admin123（首次登录后请修改）"
echo ""
echo "数据库密码已随机生成并保存在 .env（用户无需关心）"
echo "LLM 在「系统配置 → LLM 配置」里填，无需改 .env"
