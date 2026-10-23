# 娲炶嫢瑙傜伀 - Windows 涓€閿儴缃茶剼鏈?# 鐢ㄦ硶: .\init.ps1

$ErrorActionPreference = "Stop"

Write-Host "=== 娲炶嫢瑙傜伀 - 閮ㄧ讲鍒濆鍖?(Windows) ===" -ForegroundColor Cyan

# 1. 鐢熸垚 .env锛堝鏋滀笉瀛樺湪锛?if (!(Test-Path ".env")) {
    # 鐢熸垚 24 浣嶉殢鏈哄瘑鐮侊紙瀛楁瘝鏁板瓧锛?    $Password = -join ((48..57) + (65..90) + (97..122) | Get-Random -Count 24 | ForEach-Object {[char]$_})

    $envContent = @"
# 鑷姩鐢熸垚锛岃鍕挎彁浜ゅ埌 git
# PostgreSQL 鏁版嵁搴撳瘑鐮侊紙鏈嶅姟绔繛鎺ユ暟鎹簱鐢紝鐢ㄦ埛鏃犻渶鍏冲績锛?POSTGRES_PASSWORD=$Password

# JWT 瀵嗛挜锛堢暀绌哄垯鑷姩鐢熸垚锛?# JWT_SECRET=
"@

    # 鐢?UTF-8 鏃?BOM 缂栫爜鍐欏叆锛堥伩鍏?PowerShell 榛樿 UTF-16 BOM 瀵艰嚧涔辩爜锛?    $envPath = Join-Path $PWD.Path ".env"
    [System.IO.File]::WriteAllText($envPath, $envContent, [System.Text.UTF8Encoding]::new($false))
    Write-Host "OK: 宸茬敓鎴?.env锛堟暟鎹簱瀵嗙爜宸查殢鏈虹敓鎴愶紝鏃犻渶鎵嬪姩閰嶇疆锛? -ForegroundColor Green
} else {
    Write-Host "SKIP: .env 宸插瓨鍦紝璺宠繃鐢熸垚" -ForegroundColor Yellow
}

# 2. 缂栬瘧鍓嶇锛堝鏋滄湭缂栬瘧锛?if (!(Test-Path "web/dist")) {
    Write-Host "BUILD: 缂栬瘧鍓嶇..." -ForegroundColor Cyan
    Push-Location web
    if (!(Test-Path "node_modules")) {
        npm install
    }
    npm run build
    Pop-Location
    Write-Host "OK: 鍓嶇缂栬瘧瀹屾垚" -ForegroundColor Green
} else {
    Write-Host "SKIP: 鍓嶇宸茬紪璇戯紝璺宠繃" -ForegroundColor Yellow
}

# 3. 鍚姩鏈嶅姟
Write-Host "START: 鍚姩鏈嶅姟..." -ForegroundColor Cyan
docker compose up -d

Write-Host ""
Write-Host "=== 閮ㄧ讲瀹屾垚 ===" -ForegroundColor Green
Write-Host "璁块棶鍦板潃: http://localhost:18888"
Write-Host "榛樿璐﹀彿: admin"
Write-Host "榛樿瀵嗙爜: admin123锛堥娆＄櫥褰曞悗璇蜂慨鏀癸級"
Write-Host ""
Write-Host "鏁版嵁搴撳瘑鐮佸凡闅忔満鐢熸垚锛屼繚瀛樺湪 .env 涓紙鐢ㄦ埛鏃犻渶鍏冲績锛? -ForegroundColor Gray

