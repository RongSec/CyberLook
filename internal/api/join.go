package api

import (
	"log"

	"asset-platform/internal/config"
	"asset-platform/internal/model"
	"regexp"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

type joinToken struct {
	Expires time.Time
}

var (
	joinMu     sync.Mutex
	joinTokens = map[string]*joinToken{}
)

// CreateJoinToken 生成一次性加入令牌（5 分钟有效，仅能用一次）
func CreateJoinToken(c *gin.Context) {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	tk := hex.EncodeToString(b)
	joinMu.Lock()
	joinTokens[tk] = &joinToken{Expires: time.Now().Add(5 * time.Minute)}
	joinMu.Unlock()
	c.JSON(http.StatusOK, gin.H{"token": tk, "expiresIn": 300})
}

// ConsumeJoinToken 校验并消费令牌（一次性）
func ConsumeJoinToken(tk string) bool {
	joinMu.Lock()
	defer joinMu.Unlock()
	jt, ok := joinTokens[tk]
	if !ok || time.Now().After(jt.Expires) {
		return false
	}
	delete(joinTokens, tk)
	return true
}

// PeekJoinToken 校验令牌有效性（不消费，供脚本下载用）
func PeekJoinToken(tk string) bool {
	joinMu.Lock()
	defer joinMu.Unlock()
	jt, ok := joinTokens[tk]
	return ok && time.Now().Before(jt.Expires)
}

// hostRe 只允许 IP 或域名 + 可选端口，防止 Host 头注入进安装脚本
var hostRe = regexp.MustCompile(`^[A-Za-z0-9._\-]+(:[0-9]{1,5})?$`)

func serverBase(c *gin.Context) string {
	scheme := "http"
	if c.Request.TLS != nil {
		scheme = "https"
	}
	host := c.Request.Host
	// Host 头可被伪造，若含引号/分号/反引号会注入到 install.sh 里被执行
	if !hostRe.MatchString(host) {
		log.Printf("[安全警告] 非法 Host 头被拒绝用于安装脚本: %q", host)
		host = "127.0.0.1:8080"
	}
	return fmt.Sprintf("%s://%s", scheme, host)
}

// InstallSh Linux 一键加入脚本（systemd 常驻 + 开机自启 + 崩溃重启）
func InstallSh(c *gin.Context) {
	tk := c.Query("token")
	if !PeekJoinToken(tk) {
		c.String(http.StatusForbidden, "令牌无效或已过期，请在平台重新生成")
		return
	}
	server := serverBase(c)
	script := fmt.Sprintf(`#!/bin/bash
set -e
SERVER="%s"
TOKEN="%s"
# 安装到「执行脚本时所在的目录」，而不是写死在 /opt
WORKDIR="$(pwd)"
if [ "$WORKDIR" = "/" ] || [ -z "$WORKDIR" ]; then
  WORKDIR="/opt/weijian-agent"
  echo "当前目录不可用（根目录），改用 $WORKDIR"
fi
UNIT="/etc/systemd/system/weijian-agent.service"
NODE="$(hostname)"

if [ "$(id -u)" != "0" ]; then
  echo "需要管理员权限安装为系统服务。请改用："
  echo "  curl -fsSL \"$SERVER/api/nodes/install.sh?token=$TOKEN\" | sudo bash"
  exit 1
fi

ARCH="$(uname -m)"
case "$ARCH" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) echo "不支持的架构: $ARCH"; exit 1 ;;
esac

echo ""
echo "请选择安装内容："
echo "  1) 仅控制文件 —— scope-agent（约 11MB），适合已装过扫描引擎的机器"
echo "  2) 全部安装   —— scope-agent + TscanClient（约 45MB），首次加入选这个"
CHOICE=""
if [ -r /dev/tty ]; then
  printf "输入 1 或 2 [默认 2]: "
  read -r CHOICE < /dev/tty || true
fi
[ -z "$CHOICE" ] && CHOICE=2
case "$CHOICE" in
  1|2) ;;
  *) echo "无效选择，按默认 2（全部安装）处理"; CHOICE=2 ;;
esac

echo "[1/4] 安装到 $WORKDIR （架构 $ARCH）"
mkdir -p "$WORKDIR" && cd "$WORKDIR" || {
  echo "❌ 无法进入目录 $WORKDIR"; exit 1;
}

# 写入自检：root 也可能写不进去（NFS root_squash、只读挂载、磁盘满、配额）
if ! { : > .weijian_write_test; } 2>/dev/null; then
  echo "❌ 当前目录不可写：$WORKDIR"
  echo "   可能原因：NFS/共享盘 root_squash、目录只读、磁盘已满或配额不足"
  echo "   排查：df -h . ; df -i . ; mount | grep -E 'nfs|cifs|ro,'"
  echo "   建议换到本地磁盘目录重试，例如："
  echo "     cd /opt && curl -fsSL \"$SERVER/api/nodes/install.sh?token=$TOKEN\" | sudo bash"
  exit 1
fi
rm -f .weijian_write_test

echo "      下载控制文件 scope-agent ..."
if ! curl -fsSL "$SERVER/api/nodes/agent-binary?os=linux&arch=$ARCH&token=$TOKEN" -o scope-agent.part; then
  echo "❌ 下载 scope-agent 失败"
  echo "   排查：curl -v -o /tmp/a \"$SERVER/api/nodes/agent-binary?os=linux&arch=$ARCH&token=$TOKEN\""
  rm -f scope-agent.part
  exit 1
fi
mv -f scope-agent.part scope-agent
chmod +x scope-agent
if [ "$CHOICE" = "2" ]; then
  echo "      下载扫描引擎 TscanClient ..."
  if ! curl -fsSL "$SERVER/api/nodes/tscan-binary?os=linux&arch=$ARCH&token=$TOKEN" -o TscanClient.part; then
    echo "❌ 下载 TscanClient 失败"; rm -f TscanClient.part; exit 1
  fi
  mv -f TscanClient.part TscanClient
  chmod +x TscanClient
else
  echo "      跳过扫描引擎（仅控制文件）"
  if [ ! -x "$WORKDIR/TscanClient" ]; then
    echo "      注意：本机没有 TscanClient，节点可上线但无法执行扫描任务"
  fi
fi

echo "[2/4] 写入 systemd 服务（清除旧凭据，确保用本令牌重新注册）"
rm -f "$WORKDIR/agent.json" "$WORKDIR/agent.token"
cat > "$UNIT" <<EOF
[Unit]
Description=Weijian Scan Agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=$WORKDIR
ExecStart=$WORKDIR/scope-agent -server $SERVER -name $NODE -token $TOKEN -tscan $WORKDIR/TscanClient
Restart=always
RestartSec=5
StartLimitIntervalSec=0

[Install]
WantedBy=multi-user.target
EOF

echo "[3/4] 启用开机自启"
systemctl daemon-reload
systemctl enable weijian-agent >/dev/null 2>&1

echo "[4/4] 启动"
systemctl restart weijian-agent
sleep 3
systemctl --no-pager --lines=0 status weijian-agent || true
echo ""
echo "完成。节点 [$NODE] 已加入集群并设为开机自启（崩溃自动重启）。"
echo "安装目录：$WORKDIR （请勿移动或删除，服务依赖此路径）"
echo "查看日志：journalctl -u weijian-agent -f"
`, server, tk)
	c.Header("Content-Type", "text/x-shellscript; charset=utf-8")
	c.String(http.StatusOK, script)
}

// InstallPs1 Windows 一键加入脚本（自提权一次 + 计划任务常驻/自启 + 看门狗自愈）
func InstallPs1(c *gin.Context) {
	tk := c.Query("token")
	if !PeekJoinToken(tk) {
		c.String(http.StatusForbidden, "令牌无效或已过期，请在平台重新生成")
		return
	}
	server := serverBase(c)
	script := fmt.Sprintf(`# 见你未见 - Windows 节点一键加入
# 首次会弹一次 UAC，授权后全程自动；之后开机自启、崩溃自动拉起
$ErrorActionPreference = "Stop"
$Server = "%s"
$Token  = "%s"

$isAdmin = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
if (-not $isAdmin) {
    Write-Host "需要管理员权限，正在申请提权（请在弹窗点“是”）..."
    # 提权后新进程默认落在 System32，必须把原始目录带过去
    $WorkDir = (Get-Location).Path
    $self = "Set-Location '" + $WorkDir + "'; irm '" + $Server + "/api/nodes/install.ps1?token=" + $Token + "' | iex"
    $exe = (Get-Process -Id $PID).Path
    Start-Process -FilePath $exe -Verb RunAs -ArgumentList @("-NoProfile","-ExecutionPolicy","Bypass","-NoExit","-Command",$self)
    exit
}

# 安装到「执行脚本时所在的目录」；若在系统目录下执行则退回到 C:\weijian-agent
$Dir = (Get-Location).Path
if ($Dir -match '(?i)\\(System32|SysWOW64|Windows)$' -or -not $Dir) {
    $Dir = 'C:\weijian-agent'
    Write-Host "当前目录为系统目录，改用 $Dir"
}
$Exe = Join-Path $Dir "scope-agent.exe"
$Tsc = Join-Path $Dir "TscanClient.exe"
$Node = $env:COMPUTERNAME

Write-Host ""
Write-Host "请选择安装内容："
Write-Host "  1) 仅控制文件 —— scope-agent.exe（约 11MB），适合已装过扫描引擎的机器"
Write-Host "  2) 全部安装   —— scope-agent.exe + TscanClient.exe（约 46MB），首次加入选这个"
$choice = Read-Host "输入 1 或 2 [默认 2]"
if ($choice -ne "1" -and $choice -ne "2") { $choice = "2" }

Write-Host "[1/4] 安装到 $Dir"
New-Item -ItemType Directory -Force -Path $Dir | Out-Null
Set-Location $Dir
Invoke-WebRequest -Uri "$Server/api/nodes/agent-binary?os=windows&token=$Token" -OutFile $Exe -UseBasicParsing
if ($choice -eq "2") {
    Write-Host "      下载扫描引擎 TscanClient.exe ..."
    Invoke-WebRequest -Uri "$Server/api/nodes/tscan-binary?os=windows&token=$Token" -OutFile $Tsc -UseBasicParsing
} else {
    Write-Host "      跳过扫描引擎（仅控制文件）"
    if (-not (Test-Path $Tsc)) {
        Write-Host "      注意：本机没有 TscanClient.exe，节点可上线但无法执行扫描任务"
    }
}

Write-Host "[2/4] 首次注册（消耗加入令牌，生成本地凭据 agent.json）"
Remove-Item (Join-Path $Dir "agent.json") -Force -ErrorAction SilentlyContinue
Remove-Item (Join-Path $Dir "agent.token") -Force -ErrorAction SilentlyContinue
$p = Start-Process -FilePath $Exe -ArgumentList @("-server",$Server,"-name",$Node,"-token",$Token,"-tscan",$Tsc) -WorkingDirectory $Dir -WindowStyle Hidden -PassThru
Start-Sleep -Seconds 6
if (Test-Path (Join-Path $Dir "agent.json")) {
    Write-Host ("  已注册：" + (Get-Content (Join-Path $Dir 'agent.json')))
} else {
    Write-Host "  注册未完成，请检查网络后重试。"
}

Write-Host "[3/4] 配置开机自启与看门狗"
$Bat = Join-Path $Dir "start-agent.bat"
$lines = @(
  "@echo off",
  "cd /d ""$Dir""",
  "tasklist /FI ""IMAGENAME eq scope-agent.exe"" 2>NUL | find /I ""scope-agent.exe"" >NUL",
  "if not errorlevel 1 exit /b",
  "start """" /min ""$Exe"" -server $Server -name $Node -tscan ""$Tsc"""
)
Set-Content -Path $Bat -Value $lines -Encoding ASCII

$tr = [char]34 + $Bat + [char]34
# 用当前管理员账户运行（/IT 交互式 → 不需要存密码），不再用 SYSTEM：
# SYSTEM 是机器最高权限，对扫描节点来说权限过大。
schtasks /Create /TN "WeijianAgent" /TR $tr /SC ONLOGON /IT /RL HIGHEST /F | Out-Null
schtasks /Create /TN "WeijianAgentWatchdog" /TR $tr /SC MINUTE /MO 1 /IT /RL HIGHEST /F | Out-Null

Write-Host "[4/4] 启动"
schtasks /Run /TN "WeijianAgent" | Out-Null
Start-Sleep -Seconds 3
$alive = Get-Process scope-agent -ErrorAction SilentlyContinue
if ($alive) {
    Write-Host ""
    Write-Host "完成。节点 [$Node] 已加入集群。"
    Write-Host "  运行权限：当前管理员账户（非 SYSTEM）"
    Write-Host "  登录自启：计划任务 WeijianAgent"
    Write-Host "  崩溃自愈：计划任务 WeijianAgentWatchdog（每 1 分钟检查）"
    Write-Host "  程序目录：$Dir （请勿移动或删除，计划任务依赖此路径）"
    Write-Host "  卸载：schtasks /Delete /TN WeijianAgent /F ; schtasks /Delete /TN WeijianAgentWatchdog /F"
} else {
    Write-Host "启动失败，请到 $Dir 手动运行 scope-agent.exe 排查"
}
Read-Host "按回车关闭"
`, server, tk)
	c.Header("Content-Type", "text/plain; charset=utf-8")
	c.String(http.StatusOK, script)
}

// AgentBinary 下载 agent 二进制
// 支持两种认证：
//   - 首次安装：一次性加入令牌（?token=）
//   - 节点升级：节点自身凭据（?nodeId= + X-Node-Token 头）
func AgentBinary(c *gin.Context) {
	authed := false
	if tk := c.Query("token"); tk != "" && PeekJoinToken(tk) {
		authed = true
	}
	if !authed {
		// 升级场景：节点用自己的长期令牌
		if nid := c.Query("nodeId"); nid != "" {
			var n model.Node
			if err := config.DB.First(&n, nid).Error; err == nil &&
				n.Token != "" && n.Token == c.GetHeader("X-Node-Token") {
				authed = true
			}
		}
	}
	if !authed {
		c.String(http.StatusForbidden, "令牌无效或已过期")
		return
	}
	osType := c.DefaultQuery("os", "linux")
	arch := c.DefaultQuery("arch", "amd64") // amd64 / arm64
	path := "/app/agent/scope-agent-linux"
	if osType == "windows" {
		path = "/app/agent/scope-agent-windows.exe"
	} else if arch == "arm64" {
		path = "/app/agent/scope-agent-linux-arm64"
	}
	c.FileAttachment(path, fmt.Sprintf("scope-agent-%s-%s", osType, arch))
}

// TscanBinary 下载 TscanClient
func TscanBinary(c *gin.Context) {
	tk := c.Query("token")
	if !PeekJoinToken(tk) {
		c.String(http.StatusForbidden, "令牌无效或已过期")
		return
	}
	osType := c.DefaultQuery("os", "linux")
	arch := c.DefaultQuery("arch", "amd64") // amd64 / arm64
	path := "/app/tscan/TscanClient_Linux"
	name := "TscanClient"
	if osType == "windows" {
		path = "/app/tscan/TscanClient_Win.exe"
		name = "TscanClient.exe"
	} else if arch == "arm64" {
		path = "/app/tscan/TscanClient_Linux_ARM64"
		name = "TscanClient"
	}
	c.FileAttachment(path, name)
}
