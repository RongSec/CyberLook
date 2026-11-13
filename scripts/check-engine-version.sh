#!/usr/bin/env bash
# check-engine-version.sh —— 校验平台锁定的扫描引擎版本是不是最新
#
# 背景：扫描引擎（portscan / svcid）只有一份实现，在 github.com/RongSec/CyberLook-Scan。
# 平台通过 go.mod 的 require 引用它的 tag，**不再复制源码**。
# 代价是：引擎改了代码但平台没 bump 版本时，平台跑的还是旧引擎 —— 这个脚本用来暴露这种漂移。
#
# 用法：bash scripts/check-engine-version.sh          # 只报告
#       STRICT=1 bash scripts/check-engine-version.sh # 不一致时非零退出（给 CI 用）
set -euo pipefail

MODULE="github.com/RongSec/CyberLook-Scan"
cd "$(dirname "$0")/.."

PINNED="$(grep -oE "$MODULE v[0-9]+\.[0-9]+\.[0-9]+" go.mod | awk '{print $2}' | head -1)"
[ -n "$PINNED" ] || { echo "❌ go.mod 里找不到 $MODULE 的版本"; exit 1; }

LATEST="$(git ls-remote --tags --refs "https://${MODULE}.git" 'v*' 2>/dev/null \
  | awk -F/ '{print $NF}' | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | sort -V | tail -1)"
if [ -z "$LATEST" ]; then
  echo "⚠️  无法获取引擎仓库的最新 tag（网络问题？），平台锁定版本：$PINNED"
  exit 0
fi

if [ "$PINNED" = "$LATEST" ]; then
  echo "✅ 扫描引擎已是最新：$PINNED"
  exit 0
fi

echo "❌ 平台锁定 $PINNED，引擎最新 $LATEST"
echo "   升级方法："
echo "     go get ${MODULE}@${LATEST} && go mod tidy"
echo "     docker build -f Dockerfile.agent ... （重新出镜像）"
[ "${STRICT:-0}" = "1" ] && exit 2
exit 0
