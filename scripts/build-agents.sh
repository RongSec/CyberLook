#!/usr/bin/env bash
# build-agents.sh —— 多平台出包 scope-agent（v0.0.27 起扫描引擎内置，单文件即全部）
#
# 产物：<repo>/dist/agents/v<版本>/scope-agent-<os>-<arch>[.exe]
#   linux:   amd64 / arm64 / 386 / mipsle(GOMIPS=softfloat)
#   windows: amd64
# 全部 CGO_ENABLED=0 + -trimpath + -ldflags "-s -w"（纯 Go，麒麟等无 gcc 环境无关）。
# 版本取 internal/version/version.go（可用环境变量 VERSION=x.y.z 覆盖），
# 并注入 -X asset-platform/internal/version.Version。
#
# 用法：
#   bash scripts/build-agents.sh              # 本机有 go 直接用
#   或 docker 环境：SKIP_DOCKER=1 需要有 go；否则自动用 golang:1.26-alpine 容器构建
set -euo pipefail
# Git Bash 下 docker.exe 参数里的 "/src" 会被 MSYS 路径换算改写成宿主盘路径导致
# "working directory ... is invalid"；关闭本次进程的自动换算。
export MSYS2_ARG_CONV_EXCL='*'
cd "$(dirname "$0")/.."
REPO="$(pwd)"

VERSION="${VERSION:-$(sed -n 's/^var Version = "\(.*\)"/\1/p' internal/version/version.go)}"
[ -n "$VERSION" ] || { echo "无法确定版本号"; exit 1; }
OUT="$REPO/dist/agents/v$VERSION"
mkdir -p "$OUT"

LDF="-s -w -X asset-platform/internal/version.Version=${VERSION#v}"
GOFLAGS_ENV="GOFLAGS=-buildvcs=false CGO_ENABLED=0"

# 目标清单：os arch [extra-env]
TARGETS=(
  "linux amd64"
  "linux arm64"
  "linux 386"
  "linux mipsle GOMIPS=softfloat"
  "windows amd64"
)

build_native() { # go 在 PATH
  for t in "${TARGETS[@]}"; do
    # shellcheck disable=SC2086
    set -- $t
    local os=$1 arch=$2 extra=${3:-}
    local name="scope-agent-$os-$arch"
    [ "$os" = windows ] && name="$name.exe"
    echo "==> native $os/$arch ${extra:-}"
    # shellcheck disable=SC2086
    env GOOS=$os GOARCH=$arch $extra $GOFLAGS_ENV \
      go build -trimpath -ldflags="$LDF" -o "$OUT/$name" ./cmd/agent
  done
}

build_docker() {
  local src="$REPO"
  # Windows Git Bash 盘符路径转 docker -v 形式
  case "$src" in
    /?/*) src="$(printf '%s' "${src:1:1}" | tr 'a-z' 'A-Z'):${src:2}" ;;
  esac
  echo "==> docker golang:1.26-alpine 交叉编译"
  docker run --rm --platform linux/amd64 \
    -v "$src:/src" \
    -v gocache-agents:/root/.cache/go-build \
    -v gomod-agents:/go/pkg/mod \
    -e GOFLAGS=-buildvcs=false -e CGO_ENABLED=0 \
    -e GOPROXY="${GOPROXY:-https://goproxy.cn,direct}" \
    -w /src golang:1.26-alpine sh -c '
      set -e
      LDF="-s -w -X asset-platform/internal/version.Version='"${VERSION#v}"'"
      for t in "linux amd64" "linux arm64" "linux 386" "linux mipsle GOMIPS=softfloat" "windows amd64"; do
        set -- $t
        os=$1; arch=$2; extra=${3:-}
        name="scope-agent-$os-$arch"
        [ "$os" = windows ] && name="$name.exe"
        echo "==> $os/$arch ${extra:-}"
        if [ -n "$extra" ]; then
          env GOOS=$os GOARCH=$arch GOMIPS=softfloat go build -trimpath -ldflags="$LDF" -o "dist/agents/v'"$VERSION"'/$name" ./cmd/agent
        else
          GOOS=$os GOARCH=$arch go build -trimpath -ldflags="$LDF" -o "dist/agents/v'"$VERSION"'/$name" ./cmd/agent
        fi
      done
    '
}

if command -v go >/dev/null 2>&1 && [ "${SKIP_DOCKER:-0}" = "1" ]; then
  build_native
elif command -v go >/dev/null 2>&1; then
  build_native
elif command -v docker >/dev/null 2>&1; then
  build_docker
else
  echo "需要 go 或 docker"; exit 1
fi

echo
echo "产物（v$VERSION）："
ls -la "$OUT"
