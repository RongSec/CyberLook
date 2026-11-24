# 未见 - Windows 一键部署脚本
# 用法: .\init.ps1
#
# 与 init.sh 同一口径：只有一条路径——拉镜像。不做本地构建兜底。

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
# 模板是 .env.example（随仓库分发、不含真实密钥），只填部署路径与随机密码，
# 写完立刻收紧 ACL 并且不落 BOM —— .env 已在 .gitignore 里。
if (!(Test-Path ".env")) {
    $Password = -join ((48..57) + (65..90) + (97..122) | Get-Random -Count 24 | ForEach-Object {[char]$_})
    if ([string]::IsNullOrEmpty($Password)) {
        $Password = [Guid]::NewGuid().ToString("N").Substring(0, 24)
    }

    $utf8NoBom = New-Object System.Text.UTF8Encoding($false)
    if (Test-Path ".env.example") {
        # 换行统一为 LF（CRLF 会让 compose 把值读成 "...\r"）
        $lines = (Get-Content ".env.example" -Encoding UTF8) | ForEach-Object {
            if ($_ -match '^HOST_PROJECT_DIR=') { "HOST_PROJECT_DIR=$PWD" }
            elseif ($_ -match '^POSTGRES_PASSWORD=') { "POSTGRES_PASSWORD=$Password" }
            else { $_ }
        }
        $content = ($lines -join "`n") + "`n"
    } else {
        $content = "COMPOSE_PROJECT_NAME=asset-platform`nHOST_PROJECT_DIR=$PWD`nPOSTGRES_PASSWORD=$Password`n"
    }
    [System.IO.File]::WriteAllText((Join-Path $PWD ".env"), $content, $utf8NoBom)

    # 只允许当前用户读写（等价于 chmod 600）
    try {
        $acl = Get-Acl ".env"
        $acl.SetAccessRuleProtection($true, $false)
        $acl.Access | ForEach-Object { $acl.RemoveAccessRule($_) | Out-Null }
        $me = [System.Security.Principal.WindowsIdentity]::GetCurrent().Name
        $acl.AddAccessRule((New-Object System.Security.AccessControl.FileSystemAccessRule($me, "FullControl", "Allow")))
        Set-Acl ".env" $acl
    } catch {
        Write-Host "WARN: 收紧 .env 权限失败（不影响运行），建议手工确认只有当前用户可读" -ForegroundColor Yellow
    }

    Write-Host "OK: 已生成 .env（数据库密码随机；已在 .gitignore 中）" -ForegroundColor Green
} else {
    Write-Host "SKIP: .env 已存在，跳过生成" -ForegroundColor Yellow
}

# 2. 解析版本
$curVer = (Select-String -Path ".env" -Pattern "^APP_VERSION=(.*)$" -ErrorAction SilentlyContinue | Select-Object -First 1).Matches.Groups[1].Value
if ([string]::IsNullOrEmpty($curVer)) {
    $Latest = $null
    try {
        $rel = Invoke-RestMethod -Uri "https://api.github.com/repos/RongSec/CyberLook/releases/latest" -TimeoutSec 8
        $Latest = $rel.tag_name
    } catch { }
    $ver = if ($Latest) { $Latest } else { "latest" }

    $envLines = Get-Content ".env" -Encoding UTF8
    if ($envLines | Where-Object { $_ -match '^APP_VERSION=' }) {
        $envLines = $envLines | ForEach-Object { if ($_ -match '^APP_VERSION=') { "APP_VERSION=$ver" } else { $_ } }
        $utf8NoBom = New-Object System.Text.UTF8Encoding($false)
        [System.IO.File]::WriteAllText((Join-Path $PWD ".env"), (($envLines -join "`n") + "`n"), $utf8NoBom)
    } else {
        Add-Content -Path ".env" -Value "APP_VERSION=$ver" -Encoding ASCII
    }

    if ($Latest) { Write-Host "VER: 最新版本 $Latest" -ForegroundColor Cyan }
    else { Write-Host "VER: 无法查询最新版本（网络受限），使用 latest" -ForegroundColor Yellow }
}

# 3. 拉镜像
Write-Host "PULL: 拉取镜像（server / updater / agent）..." -ForegroundColor Cyan
docker compose pull
if ($LASTEXITCODE -ne 0) {
    Write-Host ""
    Write-Host "FAIL: 镜像拉取失败。排查顺序：" -ForegroundColor Red
    Write-Host "  1) 能否访问 Docker Hub: curl -I https://registry-1.docker.io/v2/"
    Write-Host "     不通就是网络问题。给 Docker Desktop 配加速器（Settings -> Docker Engine 的 registry-mirrors）"
    Write-Host "  2) 内网完全出不去: 把三个镜像同步到自建 harbor，在 .env 加一行"
    Write-Host "     REGISTRY=harbor.example.com/library  然后重跑本脚本"
    Write-Host "  3) 私有仓库需登录: docker login <registry>"
    exit 1
}

# 4. 起服务
Write-Host "START: 启动服务..." -ForegroundColor Cyan
docker compose up -d --no-build

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
