# 内网资产巡检平台

企业内网资产发现、合规检测与处置管理平台。

## 功能特性

- **资产发现**：基于 TscanClient 的端口扫描、Web 指纹识别、响应长度分析
- **分布式扫描**：多节点（支持 Windows/Linux/ARM64）批量调度，按 IP 分片，完成即领下一批
- **资产处置**：新出现 / 新变更 / 台账导入，按 IP 自动去重合并
- **SimHash 内容变更检测**：基于页面内容指纹判断资产是否改版（与台账标题无关）
- **合规检测**：端口 + 指纹双重判定，支持服务名识别
- **页面快照**：Web 资产截图留存
- **定时任务**：支持 cron 表达式，自动分发到节点
- **通知**：企业微信 / 钉钉 / 飞书 / 通用 Webhook / 邮件
- **系统在线升级**：从 GitHub Release 拉取更新，支持代理与回滚

## 技术栈

| 组件 | 技术 |
|---|---|
| 后端 | Go 1.21+ (Gin + GORM) |
| 前端 | Vue 3 + Naive UI + ECharts |
| 数据库 | PostgreSQL 16 + Redis 7 |
| 扫描引擎 | TscanClient |
| 部署 | Docker Compose |

## 快速开始

### 1. 环境要求

- Docker 20+ / Docker Compose 2+
- Go 1.21+（本地编译）
- Node.js 18+（前端构建）

### 2. 编译

```bash
# 后端
cd asset-platform
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o asset-server-linux ./cmd/server
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o scope-agent-linux ./cmd/agent

# 前端
cd web
npm install
npm run build
```

### 3. 启动

```bash
cd asset-platform
docker compose up -d
```

**默认端口**：`18888`（可在系统配置页修改）

**访问**：`http://localhost:18888`

**默认账号**：`admin` / 首次登录后请修改密码

### 4. 加入扫描节点

在节点页面生成一次性加入令牌，然后在目标机器执行：

**Linux**：
```bash
# 下载安装脚本（替换 <TOKEN>）
curl "http://<服务器>:18888/api/nodes/install.sh?token=<TOKEN>" -o install.sh

# 执行（参数选架构：amd64 或 arm64）
bash install.sh amd64
# 或
bash install.sh arm64
```

**Windows**：
```powershell
# 下载并执行
Invoke-WebRequest "http://<服务器>:18888/api/nodes/install.ps1?token=<TOKEN>" -OutFile install.ps1
.\install.ps1
```

## 目录结构

```
asset-platform/
├── cmd/                    # Go 入口
│   ├── server/             #   服务端
│   └── agent/              #   扫描节点
├── internal/               # Go 业务代码
│   ├── api/                #   HTTP 接口
│   ├── service/            #   业务逻辑
│   ├── scanner/            #   TscanClient 封装
│   ├── similarity/         #   SimHash 相似度
│   ├── model/              #   数据模型
│   └── config/             #   配置
├── web/                    # 前端（Vue 3）
├── migrations/             # SQL 迁移
├── docker-compose.yml      # Docker 编排
└── README.md
```

## 架构说明

```
┌─ 服务端 (asset-server) ──────────────────┐
│  API 服务 + 任务调度 + 数据入库          │
│  端口: 18888                             │
└──────────────────────────────────────────┘
            ↑ HTTP (心跳/任务/结果)
┌─ 扫描节点 (asset-agent) ─────────────────┐
│  TscanClient 扫描 + 结果上报             │
│  支持 Windows / Linux / ARM64            │
└──────────────────────────────────────────┘
            ↓ 扫描结果
┌─ PostgreSQL + Redis ────────────────────┐
│  资产 / 任务 / 服务 / 合规 / 快照        │
└──────────────────────────────────────────┘
```

## 数据持久化

| 数据 | 卷映射 | 说明 |
|---|---|---|
| PostgreSQL | `pg-data` | 资产/任务/扫描结果 |
| Redis | `redis-data` | 缓存 |
| JWT 密钥 | `./data/files` | 登录凭证 |
| 升级备份 | `./data/backup` | 系统升级前的二进制 |
| TscanClient 配置 | `tscan-config` | 扫描结果缓存 |

**更新后数据不丢失**（卷持久化）。

## 常用操作

### 创建扫描任务

1. **扫描** → **新建任务**
2. 填写扫描范围（IP / CIDR / 网段）
3. 选择端口策略（Top100 / 自定义 / 全端口）
4. 选择执行节点（可多选，按并发加权分配）
5. 设置分片大小（默认 100 IP/批）
6. 点「立即执行」

### 资产处置

1. **资产处置** → 查看新出现 / 新变更 / 台账有
2. 勾选 → 点「按台账确认」
3. 已确认的进入**应用服务**

### 系统升级

1. **系统配置** → **系统升级**
2. 填写 GitHub 仓库（`owner/repo`）+ 代理（可选）
3. 点「检查更新」→「更新到 vX.Y.Z」
4. 自动备份旧版本，可一键回滚

## 多平台支持

| 平台 | agent | TscanClient |
|---|---|---|
| Linux amd64 | `scope-agent-linux` | `TscanClient_Linux` |
| Linux ARM64 | `scope-agent-linux-arm64` | `TscanClient_Linux_ARM64` |
| Windows | `scope-agent-windows.exe` | `TscanClient_Win.exe` |

**下载接口**：
```
/api/nodes/agent-binary?os=linux&arch=amd64&token=<TOKEN>
/api/nodes/tscan-binary?os=linux&arch=arm64&token=<TOKEN>
```

## Git 工作流

```bash
# 首次克隆
git clone https://github.com/<用户名>/<仓库>.git
cd asset-platform

# 日常开发
git pull                      # 拉最新
# ... 修改代码 ...
git add .                     # 暂存改动
git commit -m "fix: 修复xxx"  # 提交
git push                      # 推送
```

**提交信息规范**：
- `feat:` 新功能
- `fix:` 修复
- `refactor:` 重构
- `security:` 安全
- `docs:` 文档

## 端口说明

| 端口 | 用途 |
|---|---|
| **18888** | Web 界面 + API（默认，可改） |
| 5432 | PostgreSQL（内部） |
| 6379 | Redis（内部） |

**修改端口**：系统配置 → 服务端口 → 保存 → 重启

## 故障排查

**问题：服务访问不到**
```bash
docker compose ps                    # 查看容器状态
docker compose logs server           # 查看日志
docker compose restart server        # 重启
```

**问题：节点离线**
```bash
# 检查 agent 日志
tail -f /opt/dongruo-agent/agent.log

# 重新加入
bash install.sh amd64
```

**问题：更新后二进制丢失**
```bash
# docker cp 的二进制会被容器重建丢弃，正确顺序：
docker cp asset-server-linux asset-server:/app/asset-server
docker compose restart server        # 不要用 up -d
```

## 许可证

内部使用，未开源。
