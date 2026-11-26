#!/bin/bash
# 部署初始化：生成 .env → 解析版本 → 拉镜像 → 起服务 → 等健康检查
# 用法: bash init.sh
#
# 只有一条路径：拉镜像。没有任何本地构建兜底——构建链（node/golang/debian/apt/chromium）
# 与部署无关，把它塞进部署流程只会让"拉不到镜像"变成"在服务器上编译半小时再失败"。
#
# 幂等：重复执行跳过已存在的 .env，不会重置数据库密码。

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
# 模板是 .env.example（随仓库分发、不含任何真实密钥），这里只填两项：部署路径与随机密码。
# 生成后立刻 chmod 600，并且 .env 已在 .gitignore 里——任何密钥都不该进版本库。
if [ ! -f .env ]; then
    PASSWORD=$(openssl rand -base64 32 | tr -dc 'a-zA-Z0-9' | head -c 24)
    if [ -f .env.example ]; then
        sed -e "s|^HOST_PROJECT_DIR=.*|HOST_PROJECT_DIR=$(pwd)|" \
            -e "s|^POSTGRES_PASSWORD=.*|POSTGRES_PASSWORD=$PASSWORD|" \
            .env.example > .env
    else
        cat > .env << EOF
COMPOSE_PROJECT_NAME=asset-platform
HOST_PROJECT_DIR=$(pwd)
POSTGRES_PASSWORD=$PASSWORD
EOF
    fi
    chmod 600 .env 2>/dev/null || true
    echo "✅ 已生成 .env（数据库密码随机；权限 600；已在 .gitignore 中）"
else
    echo "⏭️  .env 已存在，跳过生成"
fi

# ---------- 2. 解析版本 ----------
# 为什么不只用 /releases/latest：
#   1) 该指针由 GitHub 维护，标签被 force-update 时它可能指回旧版本；
#   2) 旧版本没有对应镜像 → 用户会看到「镜像拉取失败」，而真实原因是版本选错。
# 因此改为「取最近 15 个正式 release，按语义化版本排序取最大」，latest 仅作兜底。
CUR_VER=$(grep '^APP_VERSION=' .env 2>/dev/null | head -1 | cut -d= -f2)
if [ -z "$CUR_VER" ]; then
    LATEST=$(curl -sf --max-time 10 "https://api.github.com/repos/RongSec/CyberLook/releases?per_page=15" 2>/dev/null \
        | tr ',' '\n' | grep -o '"tag_name": *"[^"]*"' | sed 's/.*"\([^"]*\)"/\1/' \
        | grep -E '^v?[0-9]+\.[0-9]+\.[0-9]+$' \
        | sed 's/^v//' | sort -t. -k1,1n -k2,2n -k3,3n | tail -1)
    if [ -n "$LATEST" ]; then
        LATEST="v$LATEST"
    else
        LATEST=$(curl -sf --max-time 8 https://api.github.com/repos/RongSec/CyberLook/releases/latest 2>/dev/null \
            | grep -o '"tag_name": *"[^"]*"' | head -1 | sed 's/.*"\([^"]*\)"/\1/')
    fi
    VER=${LATEST:-latest}
    if grep -q '^APP_VERSION=' .env; then
        sed -i "s|^APP_VERSION=.*|APP_VERSION=$VER|" .env
    else
        echo "APP_VERSION=$VER" >> .env
    fi
    if [ -n "$LATEST" ]; then
        echo "🔖 最新版本: $LATEST"
    else
        echo "🔖 无法查询最新版本（网络受限），使用 latest"
    fi
fi
APP_VERSION=$(grep '^APP_VERSION=' .env | head -1 | cut -d= -f2)
echo "📌 目标版本: $APP_VERSION"

# ---------- 3. 拉镜像 ----------
echo "🚀 拉取镜像（server / updater / agent）..."
if ! docker compose pull; then
    cat << 'MSG'

❌ 镜像拉取失败。按顺序排查：
   1) 能否访问 Docker Hub：curl -I https://registry-1.docker.io/v2/
      不通就是网络问题。给 Docker 配加速器（阿里云/腾讯云控制台有个人加速地址），
      写进 /etc/docker/daemon.json 的 registry-mirrors 后重启 docker。
   2) 内网完全出不去：把三个镜像同步到自建 harbor，然后在 .env 里加一行
      REGISTRY=harbor.example.com/library
      再执行 bash init.sh
   3) 私有仓库需要登录：docker login <registry>
MSG
    exit 1
fi

# ---------- 4. 起服务 ----------
echo "🚀 启动服务..."
docker compose up -d --no-build

# ---------- 5. 等健康检查 ----------
# 端口必须与 compose 同源：HOST_PORT 在 .env 里可改，没写就是 18888。
HOST_PORT=$(grep '^HOST_PORT=' .env 2>/dev/null | head -1 | cut -d= -f2)
HOST_PORT=${HOST_PORT:-18888}
echo "⏳ 等待服务就绪（http://localhost:$HOST_PORT/api/health，最多 120 秒）..."
READY=0
for i in $(seq 1 40); do
    if curl -sf --max-time 3 "http://localhost:$HOST_PORT/api/health" >/dev/null 2>&1; then
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
    echo "排查：docker compose ps ；docker compose logs --tail=50 server"
fi
echo "访问地址: http://localhost:$HOST_PORT（或 http://<本机IP>:$HOST_PORT）"
echo "默认账号: admin"
echo "默认密码: admin123（首次登录后请修改）"
echo ""
echo "数据库密码已随机生成并保存在 .env（用户无需关心）"
echo "LLM 在「系统配置 → LLM 配置」里填，无需改 .env"
