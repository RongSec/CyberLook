# 未见 - Windows 一键部署脚本
# 用法: .\init.ps1

$ErrorActionPreference = "Stop"

Write-Host "=== 未见 - 部署初始化 (Windows) ===" -ForegroundColor Cyan

# 0. 前置检查
if (!(Get-Command docker -ErrorAction SilentlyContinue)) {
    Write-Host "FAIL: 未找到 docker，请先安装 Docker Desktop" -ForegroundColor Red
    exit 1
}
docker compose version | Out-Null
if ($LASTEXITCODE -ne 0) {
    Write-Host "FAIL: 未找到 docker compose 插件（Docker Desktop 自带，请确认已启动）" -ForegroundColor Red
    exit 1
}

# 1. 生成 .env（如果不存在）
if (!(Test-Path ".env")) {
    # 生成 24 位随机密码（字母数字）
    $Password = -join ((48..57) + (65..90) + (97..122) | Get-Random -Count 24 | ForEach-Object {[char]$_})
    if ([string]::IsNullOrEmpty($Password)) {
        # 兜底：用 GUID 生成
        $Password = [Guid]::NewGuid().ToString("N").Substring(0, 24)
    }

    # 换行必须用 LF（CRLF 会让 compose 把值读成 "...\r"）；
    # 编码必须是无 BOM 的 UTF-8（BOM 会让第一行变量名变成 "﻿COMPOSE_PROJECT_NAME"）。
    $envContent = @"
# 自动生成，请勿提交到 git
# PostgreSQL 数据库密码（服务端连接数据库用，用户无需关心）
COMPOSE_PROJECT_NAME=asset-platform
HOST_PROJECT_DIR=$PWD
POSTGRES_PASSWORD=$Password

# JWT 密钥（留空则自动生成并持久化到 data/files/jwt.secret）
# JWT_SECRET=
"@ -replace "`r`n", "`n"

    $utf8NoBom = New-Object System.Text.UTF8Encoding($false)
    [System.IO.File]::WriteAllText((Join-Path $PWD ".env"), $envContent, $utf8NoBom)
    Write-Host "OK: 已生成 .env（数据库密码已随机生成，无需手动配置）" -ForegroundColor Green
} else {
    Write-Host "SKIP: .env 已存在，跳过生成" -ForegroundColor Yellow
}

# 2. 解析版本（与 init.sh 口径一致：GitHub 最新 release 的 tag，查不到用 latest）
if (!(Select-String -Path ".env" -Pattern "^APP_VERSION=" -Quiet -ErrorAction SilentlyContinue)) {
    $Latest = $null
    try {
        $rel = Invoke-RestMethod -Uri "https://api.github.com/repos/RongSec/CyberLook/releases/latest" -TimeoutSec 8
        $Latest = $rel.tag_name
    } catch { }
    if ($Latest) {
        Add-Content -Path ".env" -Value "APP_VERSION=$Latest" -Encoding ASCII
        Write-Host "VER: 最新版本 $Latest" -ForegroundColor Cyan
    } else {
        Add-Content -Path ".env" -Value "APP_VERSION=latest" -Encoding ASCII
        Write-Host "VER: 无法查询最新版本（网络受限），使用 latest" -ForegroundColor Yellow
    }
}

# 3. 只编当前架构的节点二进制（全量 5 架构要 20~40 分钟）
if (!(Select-String -Path ".env" -Pattern "^AGENT_ARCHES=" -Quiet -ErrorAction SilentlyContinue)) {
    Add-Content -Path ".env" -Value "AGENT_ARCHES=amd64" -Encoding ASCII
}

# 4. 启动（server/updater 有官方镜像就用，没有就地构建）
Write-Host "START: 启动服务（首次构建约 5~15 分钟）..." -ForegroundColor Cyan
docker compose up -d --build

# 5. 等健康检查
Write-Host "WAIT: 等待服务就绪（最多 120 秒）..." -ForegroundColor Cyan
$ready = $false
for ($i = 0; $i -lt 40; $i++) {
    try {
        $r = Invoke-WebRequest -Uri "http://localhost:18888/api/health" -TimeoutSec 3 -UseBasicParsing
        if ($r.StatusCode -eq 200) { $ready = $true; break }
    } catch { }
    Start-Sleep -Seconds 3
}

Write-Host ""
if ($ready) {
    Write-Host "=== 部署完成 ===" -ForegroundColor Green
} else {
    Write-Host "=== 容器已启动，但健康检查未在 120 秒内通过 ===" -ForegroundColor Yellow
    Write-Host "排查: docker compose ps ; docker compose logs --tail=50 server"
}
Write-Host "访问地址: http://localhost:18888"
Write-Host "默认账号: admin"
Write-Host "默认密码: admin123（首次登录后请修改）"
Write-Host ""
Write-Host "数据库密码已随机生成，保存在 .env（用户无需关心）"
Write-Host "LLM 在「系统配置 -> LLM 配置」里填，无需改 .env"
