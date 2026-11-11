package api

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"os"
	"regexp"
	"sync"
	"time"

	"asset-platform/internal/config"
	"asset-platform/internal/model"

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
// v0.0.27：扫描引擎已内置于 scope-agent（纯 Go 自研，无 CGO），不再分发外部扫描器二进制。
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
  i386|i686) ARCH=386 ;;
  mips|mipsel|mipsle) ARCH=mipsle ;;
  *) echo "不支持的架构: $ARCH（支持 amd64/arm64/386/mipsle）"; exit 1 ;;
esac

echo "[1/3] 安装到 $WORKDIR （架构 $ARCH）"
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

# 扫描引擎已内置：单个 scope-agent 二进制即可执行全部扫描任务
echo "      下载控制文件 scope-agent（内置自研扫描引擎）..."
if ! curl -fsSL "$SERVER/api/nodes/agent-binary?os=linux&arch=$ARCH&token=$TOKEN" -o scope-agent.part; then
  echo "❌ 下载 scope-agent 失败"
  echo "   排查：curl -v -o /tmp/a \"$SERVER/api/nodes/agent-binary?os=linux&arch=$ARCH&token=$TOKEN\""
  rm -f scope-agent.part
  exit 1
fi
mv -f scope-agent.part scope-agent
chmod +x scope-agent

echo "[2/3] 写入 systemd 服务（清除旧凭据，确保用本令牌重新注册）"
rm -f "$WORKDIR/agent.json" "$WORKDIR/agent.token"
cat > "$UNIT" <<EOF
[Unit]
Description=Weijian Scan Agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=$WORKDIR
ExecStart=$WORKDIR/scope-agent -server $SERVER -name $NODE -token $TOKEN
Restart=always
RestartSec=5
StartLimitIntervalSec=0

[Install]
WantedBy=multi-user.target
EOF

echo "[3/3] 启用开机自启并启动"
systemctl daemon-reload
systemctl enable weijian-agent >/dev/null 2>&1
systemctl restart weijian-agent
sleep 3
systemctl --no-pager --lines=0 status weijian-agent || true
echo ""
echo "完成。节点 [$NODE] 已加入集群并设为开机自启（崩溃自动重启）。"
echo "安装目录：$WORKDIR （请勿移动或删除，服务依赖此路径）"
echo "查看日志：journalctl -u weijian-agent -f"
`, server, tk)
	c.Header("Content-Type", "text/x-shellscript; charset=utf-8")
	// 注意：shell 脚本不能加 BOM —— BOM 会粘在 #!/bin/bash 前面，
	// bash 把它当成一条命令（"BOM#!/bin/bash: command not found"）。
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
# v0.0.27：扫描引擎已内置于 scope-agent.exe，单文件安装
$ErrorActionPreference = "Stop"
$ProgressPreference    = "SilentlyContinue"
$Server = "%s"
$Token  = "%s"

# === 编码自愈 ===
# 如果用 iwr -useb ... | iex 进来：iwr 返回的是 WebResponse 对象，管道交给 iex 时
# 按 ToString() 转字符串，每个非 ASCII 字节都被换成 "?"（中文全废，BOM 也变成???）。
# 那种损坏在进入 iex 之前就发生了，脚本自身无法还原 —— 只能丢掉重新取：
# irm(Invoke-RestMethod) 直接返回字符串，按 Content-Type 的 charset=utf-8 解码，不丢字符。
$probe = -join @([char]0x6D4B, [char]0x8BD5)
if ("测试" -ne $probe) {
    if ($env:WJ_HEAL -eq "1") {
        Write-Host "Script encoding broken and auto-heal already tried once. Abort."
        Read-Host "Press Enter to close"
        return
    }
    $env:WJ_HEAL = "1"
    Write-Host "Script text arrived garbled (iwr pipe). Refetching via Invoke-RestMethod ..."
    $healUrl = $Server + "/api/nodes/install.ps1?token=" + $Token
    $healSrc = Invoke-RestMethod -Uri $healUrl
    & ([scriptblock]::Create($healSrc))
    return
}
$env:WJ_HEAL = $null

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
$Node = $env:COMPUTERNAME

Write-Host "[1/4] 安装到 $Dir"
New-Item -ItemType Directory -Force -Path $Dir | Out-Null
Set-Location $Dir

# 旧 agent 还在跑会占着 scope-agent.exe：覆盖时报「正由另一进程使用，无法访问」
# 顺序很重要：先删旧计划任务（否则旧看门狗会把刚杀掉的进程再拉起来），再杀进程
foreach ($tn in @("WeijianAgent", "WeijianAgentWatchdog")) {
    try { schtasks /Delete /TN $tn /F 2>$null | Out-Null } catch { }
}
Get-Process scope-agent -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
Start-Sleep -Seconds 1
Remove-Item $Exe -Force -ErrorAction SilentlyContinue
if (Test-Path $Exe) {
    Write-Host "❌ $Exe 仍被占用（杀进程没成功），请先手动结束 scope-agent.exe 再重跑"
    Read-Host "按回车关闭"
    return
}

Write-Host "      下载 scope-agent.exe（内置自研扫描引擎，单文件）..."
Invoke-WebRequest -Uri "$Server/api/nodes/agent-binary?os=windows&token=$Token" -OutFile $Exe -UseBasicParsing

Write-Host "[2/4] 首次注册（消耗加入令牌，生成本地凭据 agent.json）"
Remove-Item (Join-Path $Dir "agent.json") -Force -ErrorAction SilentlyContinue
Remove-Item (Join-Path $Dir "agent.token") -Force -ErrorAction SilentlyContinue
$p = Start-Process -FilePath $Exe -ArgumentList @("-server",$Server,"-name",$Node,"-token",$Token) -WorkingDirectory $Dir -WindowStyle Hidden -PassThru
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
  "start """" /min ""$Exe"" -server $Server -name $Node"
)
# 用系统 ANSI 编码写 .bat：ASCII 会把中文目录写成 "?"，任务起来找不到文件
try { Set-Content -Path $Bat -Value $lines -Encoding Default } catch { Set-Content -Path $Bat -Value $lines -Encoding ASCII }

# 注册计划任务
# 旧写法 schtasks /Create /TR $tr 的坑：路径含空格时 /TR 的引号会被二次包裹，
# schtasks 报"参数无效"，任务建不出来 → 节点上线失败。
# 改用 Register-ScheduledTask（参数按对象传递，路径含空格/中文都不会被拆坏）。
# 身份仍用当前管理员账户，不用 SYSTEM（SYSTEM 权限过大）。
$who = $env:USERDOMAIN + "\" + $env:USERNAME
$TaskNames = @("WeijianAgent", "WeijianAgentWatchdog")
$taskOK = $false
foreach ($tn in $TaskNames) {
    try {
        if (Get-ScheduledTask -TaskName $tn -ErrorAction SilentlyContinue) {
            Unregister-ScheduledTask -TaskName $tn -Confirm:$false -ErrorAction Stop
        }
    } catch {
        Write-Host ("  旧任务 " + $tn + " 清理失败（继续）：" + $_.Exception.Message)
    }
}
if (Get-Command Register-ScheduledTask -ErrorAction SilentlyContinue) {
    $act = New-ScheduledTaskAction -Execute $Bat -WorkingDirectory $Dir
    $set = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -StartWhenAvailable -MultipleInstances IgnoreNew
    $trgWatch = New-ScheduledTaskTrigger -Once -At (Get-Date) -RepetitionInterval (New-TimeSpan -Minutes 1)
    $watchOK = $false
    # 看门狗每分钟触发一次：必须用 S4U（后台会话，无桌面）注册，
    # 否则交互式会话下每分钟都会闪一个 cmd 黑窗（就是"老是弹窗"的来源）。
    try {
        $prnBg = New-ScheduledTaskPrincipal -UserId $who -LogonType S4U -RunLevel Highest
        Register-ScheduledTask -TaskName "WeijianAgentWatchdog" -Action $act -Trigger $trgWatch -Settings $set -Principal $prnBg -Force -ErrorAction Stop | Out-Null
        # 立刻试跑一次，确认后台会话真能起来（策略禁止 batch 登录会失败）
        Start-ScheduledTask -TaskName "WeijianAgentWatchdog"
        Start-Sleep -Seconds 4
        $lr = (Get-ScheduledTaskInfo -TaskName "WeijianAgentWatchdog").LastTaskResult
        if ($lr -eq 0 -or $lr -eq 267009) {
            $watchOK = $true
            Write-Host "  看门狗：后台会话运行（不会弹窗）"
        } else {
            Write-Host ("  后台会话不可用（LastTaskResult=0x" + ("{0:X}" -f $lr) + "），改用交互式")
        }
    } catch {
        Write-Host ("  S4U 注册失败：" + $_.Exception.Message)
    }
    try {
        if (-not $watchOK) {
            $prnBg = New-ScheduledTaskPrincipal -UserId $who -LogonType Interactive -RunLevel Highest
            Register-ScheduledTask -TaskName "WeijianAgentWatchdog" -Action $act -Trigger $trgWatch -Settings $set -Principal $prnBg -Force -ErrorAction Stop | Out-Null
            Write-Host "  看门狗：交互式（每分钟会闪一下窗口）"
        }
        $trgL = New-ScheduledTaskTrigger -AtLogOn
        Register-ScheduledTask -TaskName "WeijianAgent" -Action $act -Trigger $trgL -Settings $set -Principal (New-ScheduledTaskPrincipal -UserId $who -LogonType Interactive -RunLevel Highest) -Force -ErrorAction Stop | Out-Null
        $taskOK = $true
    } catch {
        Write-Host ("  计划任务注册失败：" + $_.Exception.Message)
    }
} else {
    Write-Host "  当前系统无计划任务模块，跳过开机自启"
}
if (-not $taskOK) {
    Write-Host "  警告：未注册计划任务，本次仍可上线，但重启后不会自启"
}

Write-Host "[4/4] 启动"
if ($taskOK) {
    # 注册阶段试跑的进程交给计划任务接管：它挂在当前控制台下，关窗就死
    Stop-Process -Id $p.Id -Force -ErrorAction SilentlyContinue
    Start-Sleep -Seconds 1
    Start-ScheduledTask -TaskName "WeijianAgent"
} else {
    # 兜底：直接拉起（先看是否已在跑，避免双实例）
    if (-not (Get-Process scope-agent -ErrorAction SilentlyContinue)) {
        Start-Process -FilePath $Exe -ArgumentList @("-server",$Server,"-name",$Node) -WorkingDirectory $Dir -WindowStyle Hidden | Out-Null
    }
}
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
	// charset=utf-8 必须显式给：PS 5.1 的 irm/iwr 不带 charset 时按 ISO-8859-1 解码，中文全部乱码
	c.Header("Content-Type", "text/plain; charset=utf-8")
	// 不能加 BOM。实测（PS 5.1）：`iwr -useb ... | iex` 里 iwr 返回的是响应对象，
	// 管道按 ToString() 转字符串时 BOM 会被拆成 3 个 "?"，首行变成 "???# ..." 直接报
	// 「无法将"???#"项识别为 cmdlet」；中文也逐字节变 "?"。BOM 只对 `-File` 有用，
	// 而平台给的是 irm 管道安装，靠 charset 头解码即可，因此这里不写 BOM。
	c.String(http.StatusOK, script)
}

// agentBinaryName 按 os/arch 给出容器内 agent 二进制路径。
// 出包脚本 scripts/build-agents.sh 产出全 5 平台（linux: amd64/arm64/386/mipsle + windows: amd64）。
// 找不到对应架构文件时回退 amd64（老镜像/手工部署常见只有 amd64）。
func agentBinaryName(osType, arch string) (string, string) {
	if osType == "windows" {
		return "/app/agent/scope-agent-windows.exe", "scope-agent-windows.exe"
	}
	base := "/app/agent/scope-agent-linux-"
	switch arch {
	case "arm64":
		return base + "arm64", "scope-agent-linux-arm64"
	case "386":
		return base + "386", "scope-agent-linux-386"
	case "mipsle":
		return base + "mipsle", "scope-agent-linux-mipsle"
	default: // amd64
		return base + "amd64", "scope-agent-linux-amd64"
	}
}

// AgentBinary 下载 agent 二进制（内置自研扫描引擎，单文件即全部）
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
	arch := c.DefaultQuery("arch", "amd64") // amd64 / arm64 / 386 / mipsle
	path, name := agentBinaryName(osType, arch)
	// 兼容历史命名（老镜像里是 scope-agent-linux 无架构后缀）
	if fi, err := os.Stat(path); err != nil || fi.IsDir() {
		if osType != "windows" && arch == "amd64" {
			if fi2, e2 := os.Stat("/app/agent/scope-agent-linux"); e2 == nil && !fi2.IsDir() {
				path = "/app/agent/scope-agent-linux"
			}
		}
	}
	c.FileAttachment(path, name)
}
