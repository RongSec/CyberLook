#!/bin/bash
# 部署初始化脚本：自动生成随机数据库密码
# 用法: bash init.sh

set -e

echo "=== 未见 - 部署初始化 ==="

# 1. 生成 .env（如果不存在）
if [ ! -f .env ]; then
    # 生成 24 位随机密码（只含字母数字）
    PASSWORD=$(openssl rand -base64 32 | tr -dc 'a-zA-Z0-9' | head -c 24)
    cat > .env << EOF
# 自动生成，请勿提交到 git
# PostgreSQL 数据库密码（服务端连接数据库用，用户无需关心）
POSTGRES_PASSWORD=$PASSWORD

# JWT 密钥（留空则自动生成）
# JWT_SECRET=
EOF
    echo "✅ 已生成 .env（数据库密码已随机生成，无需手动配置）"
else
    echo "⏭️  .env 已存在，跳过生成"
fi

# 1.5 决定用远程镜像还是本地构建
# 优先拉 ghcr.io 上的官方镜像（免编译、版本可控）；拉不到再退回本地构建
if [ -z "$(grep '^APP_VERSION=' .env 2>/dev/null)" ]; then
    LATEST=$(curl -sf --max-time 8 https://api.github.com/repos/RongSec/CyberLook/releases/latest 2>/dev/null         | grep -o '"tag_name": *"[^"]*"' | head -1 | sed 's/.*"\([^"]*\)"/\1/')
    if [ -n "$LATEST" ]; then
        echo "APP_VERSION=$LATEST" >> .env
        echo "🔖 最新版本: $LATEST"
    else
        echo "APP_VERSION=latest" >> .env
        echo "🔖 无法查询最新版本，使用 latest"
    fi
fi

# 2. 启动服务

echo "🚀 拉取镜像..."
if docker compose pull server updater 2>/dev/null; then
    echo "✅ 镜像已就绪（官方镜像，无需编译）"
    echo "🚀 启动服务..."
    docker compose up -d --pull never
else
    echo "⚠️  镜像拉取失败（离线或私有），改用本地构建（需要 Go/Node 环境，约 5-10 分钟）"
    echo "🚀 构建并启动服务..."
    docker compose up -d --build
fi

echo ""
echo "=== 部署完成 ==="
echo "访问地址: http://localhost:18888"
echo "默认账号: admin"
echo "默认密码: admin123（首次登录后请修改）"
echo ""
echo "数据库密码已随机生成，保存在 .env 中（用户无需关心）"
