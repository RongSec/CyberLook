package api

import (
	"log"
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

// InstallSh Linux 一键加入脚本
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
WORKDIR="/opt/dongruo-agent"
echo "[1/3] 创建目录 $WORKDIR"
mkdir -p $WORKDIR && cd $WORKDIR
echo "[2/3] 下载 agent 与扫描引擎..."
curl -fsSL "$SERVER/api/nodes/agent-binary?os=linux&token=$TOKEN" -o scope-agent
curl -fsSL "$SERVER/api/nodes/tscan-binary?os=linux&token=$TOKEN" -o TscanClient
chmod +x scope-agent TscanClient
echo "[3/3] 启动并加入集群..."
nohup ./scope-agent -server "$SERVER" -name "$(hostname)" -token "$TOKEN" -tscan ./TscanClient > agent.log 2>&1 &
sleep 1
echo "完成。节点 [$(hostname)] 已加入集群，日志: $WORKDIR/agent.log"
`, server, tk)
	c.Header("Content-Type", "text/x-shellscript; charset=utf-8")
	c.String(http.StatusOK, script)
}

// InstallPs1 Windows 一键加入脚本
func InstallPs1(c *gin.Context) {
	tk := c.Query("token")
	if !PeekJoinToken(tk) {
		c.String(http.StatusForbidden, "令牌无效或已过期，请在平台重新生成")
		return
	}
	server := serverBase(c)
	script := fmt.Sprintf(`# 洞若观火 - Windows 节点一键加入
$ErrorActionPreference = "Stop"
$Server = "%s"
$Token = "%s"
$Dir = Join-Path $env:ProgramData "dongruo-agent"
Write-Host "[1/3] 创建目录 $Dir"
New-Item -ItemType Directory -Force -Path $Dir | Out-Null
Set-Location $Dir
Write-Host "[2/3] 下载 agent 与扫描引擎..."
Invoke-WebRequest -Uri "$Server/api/nodes/agent-binary?os=windows&token=$Token" -OutFile "scope-agent.exe" -UseBasicParsing
Invoke-WebRequest -Uri "$Server/api/nodes/tscan-binary?os=windows&token=$Token" -OutFile "TscanClient.exe" -UseBasicParsing
Write-Host "[3/3] 启动并加入集群..."
$p = Start-Process -FilePath ".\scope-agent.exe" -ArgumentList "-server","$Server","-name","$env:COMPUTERNAME","-token","$Token","-tscan",".\TscanClient.exe" -WindowStyle Hidden -PassThru
Start-Sleep -Seconds 2
if ($p.HasExited) { Write-Host "启动失败，退出码 $($p.ExitCode)" } else { Write-Host "完成。节点 [$env:COMPUTERNAME] 已加入集群" }
`, server, tk)
	c.Header("Content-Type", "text/plain; charset=utf-8")
	c.String(http.StatusOK, script)
}

// AgentBinary 下载 agent 二进制
func AgentBinary(c *gin.Context) {
	// 与 install.sh 一致：需要有效的一次性加入令牌
	tk := c.Query("token")
	if !PeekJoinToken(tk) {
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
