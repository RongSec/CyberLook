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

# 2. 启动服务（多阶段构建会自动编译前后端）

echo "🚀 启动服务..."
docker compose up -d

echo ""
echo "=== 部署完成 ==="
echo "访问地址: http://localhost:18888"
echo "默认账号: admin"
echo "默认密码: admin123（首次登录后请修改）"
echo ""
echo "数据库密码已随机生成，保存在 .env 中（用户无需关心）"
