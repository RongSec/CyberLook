#!/bin/bash
# 手动更新兜底脚本（updater 边车不可用时使用）
# 用法: bash update.sh v0.0.4
set -e

VERSION="$1"
if [ -z "$VERSION" ]; then
  echo "用法: bash update.sh v0.0.4"
  echo "当前版本: $(grep '^APP_VERSION=' .env 2>/dev/null | cut -d= -f2 || echo unknown)"
  exit 1
fi

OLD=$(grep '^APP_VERSION=' .env 2>/dev/null | cut -d= -f2 || echo "")
echo "=== 更新 $OLD -> $VERSION ==="

echo "[1/4] 备份数据库..."
mkdir -p data/backup
docker exec asset-postgres pg_dump -U asset asset_platform > "data/backup/pre-update-$(date +%s).sql" \
  && echo "  备份完成" || echo "  备份失败（继续）"

echo "[2/4] 切换版本..."
if grep -q '^APP_VERSION=' .env 2>/dev/null; then
  sed -i "s|^APP_VERSION=.*|APP_VERSION=$VERSION|" .env
else
  echo "APP_VERSION=$VERSION" >> .env
fi

echo "[3/4] 拉取镜像并重启..."
docker compose pull server
docker compose up -d --no-deps server

echo "[4/4] 健康检查（最多 90 秒）..."
for i in $(seq 1 30); do
  if curl -sf http://localhost:18888/api/health >/dev/null 2>&1; then
    echo "=== 更新完成: $VERSION ==="
    exit 0
  fi
  sleep 3
done

echo "!!! 健康检查失败，回滚到 $OLD"
if [ -n "$OLD" ]; then
  sed -i "s|^APP_VERSION=.*|APP_VERSION=$OLD|" .env
  docker compose up -d --no-deps server
fi
exit 1
