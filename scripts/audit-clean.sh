#!/usr/bin/env bash
# 发布前自检：确认工作区、Git 历史、镜像里都没有不该外发的东西。
#
# 用法：
#   bash scripts/audit-clean.sh                 # 工作区 + 未跟踪的敏感文件
#   bash scripts/audit-clean.sh --history       # 再加全量历史（慢，几百个提交约数秒）
#   bash scripts/audit-clean.sh --image rongsec/cyberlook:v1.0.3   # 再加镜像内容
#   EXTRA_PATTERNS='某公司|某系统' bash scripts/audit-clean.sh      # 追加自定义关键词
#
# 退出码：0 干净；1 有命中。
#
# 为什么要有这个脚本：
#   .gitignore 只挡未来的文件，挡不住已经提交过的历史；
#   镜像里的字符串是从源码编译进去的，扫源码干净不代表镜像干净。
set -uo pipefail

RED='\033[31m'; YEL='\033[33m'; GRN='\033[32m'; DIM='\033[2m'; RST='\033[0m'

SCAN_HISTORY=0
SCAN_IMAGE=""
while [ $# -gt 0 ]; do
    case "$1" in
        --history) SCAN_HISTORY=1; shift ;;
        --image) SCAN_IMAGE="${2:-}"; shift 2 ;;
        -h|--help) sed -n '2,16p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
        *) echo "未知参数: $1"; exit 2 ;;
    esac
done

cd "$(git rev-parse --show-toplevel 2>/dev/null || echo .)" || exit 2
FAIL=0

# ---------- 要扫的模式 ----------
# 私网地址段（排除 RFC 5737/2544 文档保留段，它们是用来当示例的）
RE_IP='(10\.[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}|172\.(1[6-9]|2[0-9]|3[01])\.[0-9]{1,3}\.[0-9]{1,3}|192\.168\.[0-9]{1,3}\.[0-9]{1,3})'
RE_DOC_IP='(192\.0\.2|198\.51\.100|203\.0\.113|198\.1[89])\.[0-9]{1,3}'
# 公认的「示例用」私网地址：本仓库文档/测试统一用 10.0.0.x 与 10.1.1.x 当占位，
# 这两个 /24 出现在代码里属于正常示例，不算泄漏
ALLOW_IPS='(10\.0\.0\.[0-9]{1,3}|10\.1\.1\.[0-9]{1,3}|172\.16\.0\.1|192\.168\.0\.1|192\.168\.1\.1|127\.0\.0\.1|0\.0\.0\.0)'
# 本机绝对路径
RE_PATH='([A-Za-z]:\\\\?Users\\\\?|[A-Za-z]:/Users/|/home/[a-z][a-z0-9_-]{2,}/|/Users/[A-Za-z][A-Za-z0-9_-]+/)'
# 非占位邮箱
RE_MAIL='[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.(com|cn|net|org|local|io)'
RE_MAIL_OK='(users\.noreply\.github\.com|example\.(com|org)|@localhost|test\.local|team\.local)'
# 密钥
RE_KEY='sk-[A-Za-z0-9_-]{20,}|gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|dckr_pat_[A-Za-z0-9_-]{10,}|AKIA[0-9A-Z]{16}|-----BEGIN (RSA |EC |OPENSSH )?PRIVATE KEY-----'
EXTRA="${EXTRA_PATTERNS:-}"

# 不参与扫描的路径：脚本自身、第三方指纹规则库（含大量第三方作者信息）、截图
SKIP_ALT='(\./)?(scripts/audit-clean\.sh|\.githooks/|\.gitleaks\.toml|data/fingerprint/|docs/images/|web/dist)'
SKIP_PATH="^${SKIP_ALT}"
# 被 .gitignore 覆盖的目录整体跳过
skip_file() {
    echo "$1" | grep -qE "$SKIP_PATH" && return 0
    case "$1" in *.png|*.jpg|*.jpeg|*.gif|*.svg|*.woff|*.woff2|*.ttf|*.ico|*.zip|*.gz|*.tar|*.tgz) return 0 ;; esac
    return 1
}

say() { printf '%b\n' "$*"; }

# ---------- 1. 工作区（已跟踪文件） ----------
say "== 1/4 工作区（已跟踪文件）=="
WORK_HITS=0
# 用 git grep 批量扫描（每个模式一次进程）。逐文件循环在 Windows 上要起上千个子进程，慢到不可用。
# 不用 awk 处理动态正则：不同 awk 实现对 -v 传入的转义处理不一致，会退化成空匹配。
report_hits() {
    local label="$1" hits="$2"
    [ -z "$hits" ] && return
    say "${RED}  ${label}${RST}"
    printf '%s\n' "$hits" | head -6 | cut -c1-140 | sed 's/^/     /'
    WORK_HITS=1
}

# --- 私网地址 ---
# 只查「会给人看的」载体：Markdown、文档、脚本、compose/模板。
# Go 测试里的 10.55.0.1 这类是合成数据，不代表任何真实网段，查它只会制造噪音；
# 历史上真正的泄漏（内网段写进 CHANGELOG）恰好都在这类文档里。
# 想强制全仓库查你单位的网段：SENSITIVE_RANGES='10\.24\.|10\.188\.'
if [ -n "${SENSITIVE_RANGES:-}" ]; then
    ip_hits=$(git grep -nIE "$SENSITIVE_RANGES" -- . 2>/dev/null | grep -vE "^${SKIP_ALT}")
    report_hits "命中指定敏感网段" "$ip_hits"
else
    ip_all=$(git grep -hoIE "$RE_IP" -- '*.md' '*.sh' '*.ps1' '*.yml' '*.yaml' '*.example' 'docs' 2>/dev/null | sort -u)
    ip_bad=$(printf '%s\n' "$ip_all" | grep -vE "^($ALLOW_IPS)$" | grep -vE "^($RE_DOC_IP)$" | grep -v '^$' || true)
    if [ -n "$ip_bad" ]; then
        say "${RED}  文档/脚本里的私网地址（非文档保留段、非公认示例）${RST}"
        printf '%s\n' "$ip_bad" | head -5 | sed 's/^/     地址 /'
        for one in $(printf '%s\n' "$ip_bad" | head -3); do
            git grep -nIE "$one" -- '*.md' '*.sh' '*.ps1' '*.yml' '*.example' 'docs' 2>/dev/null                 | grep -vE "^${SKIP_ALT}" | head -2 | cut -c1-130 | sed 's/^/       /'
        done
        WORK_HITS=1
    fi
fi

# --- 本机绝对路径 / 非占位邮箱 / 密钥 ---
report_hits "本机绝对路径" "$(git grep -nIE "$RE_PATH" -- . 2>/dev/null | grep -vE "^${SKIP_ALT}")"
report_hits "非占位邮箱" "$(git grep -nIE "$RE_MAIL" -- . 2>/dev/null | grep -vE "$RE_MAIL_OK" | grep -vE "^${SKIP_ALT}")"
report_hits "疑似密钥" "$(git grep -nIE "$RE_KEY" -- . 2>/dev/null | grep -vE "^${SKIP_ALT}")"
if [ -n "$EXTRA" ]; then
    report_hits "自定义关键词" "$(git grep -nIE "$EXTRA" -- . 2>/dev/null | grep -vE "^${SKIP_ALT}")"
fi

[ "$WORK_HITS" = "0" ] && say "${GRN}  ✓ 无命中${RST}"
FAIL=$((FAIL + WORK_HITS))

say "== 2/4 本机敏感文件（不该存在的东西）=="
UNTRACK_HITS=0
for p in .env data/backup data/files/jwt.secret; do
    if [ -e "$p" ]; then
        if git check-ignore -q "$p" 2>/dev/null; then
            say "${DIM}  $p 存在，但已被 .gitignore 覆盖（正常）${RST}"
        else
            say "${RED}  $p 存在且未被忽略！${RST}"; UNTRACK_HITS=1
        fi
    fi
done
ls -1 *.bundle *.pem *.key 2>/dev/null | while read -r f; do
    say "${YEL}  $f 建议移出仓库目录${RST}"
done
[ "$UNTRACK_HITS" = "0" ] && say "${GRN}  ✓ 无风险文件${RST}"
FAIL=$((FAIL + UNTRACK_HITS))

# ---------- 3. Git 历史 ----------
if [ "$SCAN_HISTORY" = "1" ]; then
    say "== 3/4 Git 历史（全部提交）=="
    HIST_HITS=0
    revs=$(git rev-list --all)
    # 历史里同样跳过第三方数据与脚本自身；示例地址在被检文件里占比极低，命中后人工确认即可
    check_hist() {
        local pat="$1" label="$2"
        local files
        files=$(git grep -I -lE "$pat" $revs 2>/dev/null \
            | sed 's/^[0-9a-f]\{7,\}://' | sort -u \
            | grep -vE "$SKIP_PATH" || true)
        if [ -n "$files" ]; then
            say "${RED}  历史中命中${label}：${RST}"
            printf '%s\n' "$files" | head -4 | sed 's/^/     /'
            HIST_HITS=1
        fi
    }
    check_hist "$RE_KEY" "疑似密钥"
    if [ -n "$EXTRA" ]; then check_hist "$EXTRA" "自定义关键词"; fi
    [ "$HIST_HITS" = "0" ] && say "${GRN}  ✓ 历史干净${RST}"
    FAIL=$((FAIL + HIST_HITS))
else
    say "== 3/4 Git 历史 ${DIM}(跳过，加 --history 启用)${RST}"
fi

# ---------- 4. 镜像 ----------
if [ -n "$SCAN_IMAGE" ]; then
    say "== 4/4 镜像 $SCAN_IMAGE =="
    if ! command -v docker >/dev/null 2>&1; then
        say "${YEL}  未安装 docker，跳过${RST}"
    else
        out=$(docker run --rm --entrypoint sh "$SCAN_IMAGE" -c '
            for p in "'"$RE_IP"'" "'"$RE_KEY"'"; do
                find / -xdev -type f -size -200M \( -perm -u+x -o -name "*.json" -o -name "*.js" \) 2>/dev/null \
                  | head -400 | xargs -r grep -alE "$p" 2>/dev/null | head -3
            done' 2>/dev/null)
        if [ -n "$out" ]; then
            say "${RED}  镜像内命中：${RST}"; echo "$out" | sed 's/^/     /'; FAIL=$((FAIL+1))
        else
            say "${GRN}  ✓ 镜像内无命中${RST}"
        fi
    fi
else
    say "== 4/4 镜像 ${DIM}(跳过，加 --image <tag> 启用)${RST}"
fi

say ""
if [ "$FAIL" = "0" ]; then
    say "${GRN}结论：可以发布${RST}"
    exit 0
else
    say "${RED}结论：有 $FAIL 类问题，先清理再发布${RST}"
    exit 1
fi
