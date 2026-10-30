# 未见 - Windows 一键部署脚本
# 用法: .\init.ps1

$ErrorActionPreference = "Stop"

Write-Host "=== 未见 - 部署初始化 (Windows) ===" -ForegroundColor Cyan

# 1. 生成 .env（如果不存在）
if (!(Test-Path ".env")) {
    # 生成 24 位随机密码（字母数字）
    $Password = -join ((48..57) + (65..90) + (97..122) | Get-Random -Count 24 | ForEach-Object {[char]$_})
    if ([string]::IsNullOrEmpty($Password)) {
        # 兜底：用 GUID 生成
        $Password = [Guid]::NewGuid().ToString("N").Substring(0, 24)
    }

    $envContent = @"
# 自动生成，请勿提交到 git
# PostgreSQL 数据库密码（服务端连接数据库用，用户无需关心）
COMPOSE_PROJECT_NAME=asset-platform
POSTGRES_PASSWORD=$Password

# JWT 密钥（留空则自动生成）
# JWT_SECRET=
"@

    # 用 Set-Content 写入（PowerShell 原生，相对路径可靠）
    Set-Content -Path ".env" -Value $envContent -Encoding UTF8
    Write-Host "OK: 已生成 .env（数据库密码已随机生成，无需手动配置）" -ForegroundColor Green
} else {
    Write-Host "SKIP: .env 已存在，跳过生成" -ForegroundColor Yellow
}

# 2. 启动服务（多阶段构建会自动编译前后端）

Write-Host "START: 启动服务..." -ForegroundColor Cyan
docker compose up -d

Write-Host ""
Write-Host "=== 部署完成 ===" -ForegroundColor Green
Write-Host "访问地址: http://localhost:18888"
Write-Host "默认账号: admin"
Write-Host "默认密码: admin123（首次登录后请修改）"
Write-Host ""
Write-Host "数据库密码已随机生成，保存在 .env 中（用户无需关心）" -ForegroundColor Gray
