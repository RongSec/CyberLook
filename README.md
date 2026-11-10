<div align="center">
  <img src="assets/logo.png" alt="未见" width="200" />
  <h1>未见</h1>
  <p><em>见你未见 · 照你所患</em></p>
  <p>
    <img src="https://img.shields.io/badge/version-0.0.2-blue" alt="version" />
    <img src="https://img.shields.io/badge/Go-1.21+-00ADD8" alt="go" />
    <img src="https://img.shields.io/badge/Vue-3-4FC08D" alt="vue" />
    <img src="https://img.shields.io/badge/PostgreSQL-16-336791" alt="postgres" />
  </p>
</div>

未见是一个企业内网资产发现、合规检测与处置管理平台。致力于基于事实发现的内网资产动态维护。

**核心能力**：分布式端口/Web 扫描 → 存活探测 → 资产处置 → 组织归属 → 合规检测 → 变更感知
## 
本项目无一行人类代码，将来也不会使用一行人类代码。但所有功能均经过人类验证。0.0.2版本为功能完善的第一次发版，目前已进入长期维护阶段。

测试环境：麒麟，ubuntu,windows。

测试目标：真实企业内网共94653个IP

## 特别鸣谢
- https://github.com/TideSec/TscanPlus
- 本系统核心扫描能力均依赖于内嵌的tscanclient,日后也会对tscanclient的更新在第一时间做主动适配。

## TODO
- 暂停在继续后扫描任务的概览会重复一次分片计数（不影响功能和数据）
## 功能特性

- **资产发现**：基于 TscanClient 的端口扫描、Web 指纹识别
- **分布式扫描**：多节点（支持 Windows/Linux）批量调度，可对节点单独设置参数，灵活分片，敏捷调度
- **资产处置**：按分类处置资产（新出现 / 新变更 / 台账导入）
- **SimHash 内容变更检测**：基于页面内容指纹判断资产是否改版，避免重复劳动
- **合规检测**：记录在扫描中发现的可直连数据库等违规操作，可自定义规则
- **页面快照**：Web 资产截图留存
- **定时任务**：支持对扫描任务定时执行，可自动分发到节点
- **通知**：企业微信 / 钉钉 / 飞书 / 通用 Webhook / 邮件，支持按事件类型（任务完成/新资产/违规/故障）分别开关
- **断点续传**：探测进度与待扫池落盘（`data/pipeline/<taskId>.json`），服务重启自动从断点恢复，不丢存活 IP
- **任务控制**：停止 / 暂停 / 继续 / 重跑四态分离；暂停时未完成分片自动收回待扫池，继续时重新派发
- **熔断与自动重派**：节点连续失败达到阈值自动熔断（默认 5 次 / 5 分钟，可在节点页配置），熔断期分片收回并重派其他节点
- **故障与日志**：记录服务端中断、节点离线、节点熔断、分片失败；故障记录默认保留 30 天（可调），可配通知
- **组织架构**：从资产自动同步部门与多负责人，支持部门合并/拆分，拆分时按负责人分配
- **持续存活探测**：探测不等端口扫描消耗，存活 IP 持续攒入待扫池
- **系统在线升级**：从 GitHub 拉取更新，支持代理与回滚

## 技术栈

| 组件 | 技术 |
|---|---|
| 后端 | Go 1.26+ (Gin + GORM) |
| 前端 | Vue 3 + Naive UI + ECharts |
| 数据库 | PostgreSQL 16 + Redis 7 |
| 扫描引擎 | TscanClient |
| 部署 | Docker Compose |

## 快速开始

### 1. 环境要求

- Docker 20+ / Docker Compose 2+
- 仅二次开发需要：Go 1.26+、Node.js 22+

### 2. 一键部署

> **无需手动编译**。`docker compose` 使用多阶段构建（Node + Go），前后端在镜像内自动编译。



**Linux / macOS**：
```bash
git clone https://github.com/RongSec/CyberLook.git
cd CyberLook
bash init.sh
```

**Windows (PowerShell)**：
```powershell
git clone https://github.com/RongSec/CyberLook.git
cd CyberLook
.\init.ps1
```

**脚本自动完成**：
1. 生成 `.env`（**随机数据库密码**，用户无需关心）
2. 启动 Docker 服务（多阶段构建自动编译前后端）

**默认端口**：`18888`（可在系统配置页修改）

**访问**：`http://localhost:18888`

**默认账号**：`admin` / **默认密码**：`admin123`（首次登录后请修改）


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

## 资产从发现到标记的完整流程（业务视角）

```mermaid
flowchart TD
    A["输入 IP / IP 段"] --> B["创建扫描任务<br/>（可设 cron 定期执行）"]
    B --> C{"调度器分配节点"}
    C -->|"节点空闲且未熔断"| D["端口探测"]
    C -->|"全节点熔断/离线"| C1["IP 回待扫池<br/>等待节点恢复"] --> D

    D --> E["服务 / 指纹识别"]
    E --> F{"存在 Web 服务?"}
    F -->|"是"| G["HTTP 探测<br/>标题 + 响应码 + Banner"]
    F -->|"否"| H["仅记录端口<br/>资产直接 confirmed"]

    G --> I[("assets<br/>status = pending")]
    H --> I

    I --> J["资产处置页<br/>待确认池"]
    J --> K{"人工判断"}
    K -->|"确认归属"| L["填 部门 / 负责人 / 系统名"]
    K -->|"误报或无关"| M["删除记录"]

    L --> N[("status = confirmed")]
    N --> O["资产总览 / 应用服务 / 页面快照"]
    O --> R["台账比对<br/>红=台账无 绿=台账有 黄=页面变更"]

    style I fill:#FAEEDA,stroke:#BA7517
    style N fill:#EAF3DE,stroke:#639922
    style J fill:#E6F1FB,stroke:#378ADD
    style R fill:#FBEAF0,stroke:#D4537E
```

---

## IP 流转：从 IP 段到分片到节点（全链路）

> 这一节对应代码：`internal/service/dispatch.go`、`internal/service/fault.go`、`internal/target/`。
> 一句话：**IP 段 → 展开成 IP 列表 → 每 500 个一片做存活探测 → 存活的进 alivePool → 按 batch_size 切分片 → 派给空闲节点 → 失败的 IP 回池重派 → 池空 + 探完 + 无活跃分片 = 任务结束。**

### 1. 目标展开

| 环节 | 细节 |
|---|---|
| 输入字段 | `scan_tasks.ip_ranges`，一个字符串 |
| 分隔符 | `,` `，` `;` `；` 换行 空格 `、` 都算 |
| 支持的写法 | 单个 IP、CIDR（`10.24.1.0/24`）、区间（`10.24.1.1-10.24.1.20`） |
| 展开函数 | `target.Resolve()` |
| 结果落点 | 内存 `taskPipeline.totalIPs`（全量原始 IP，**不受排除段影响后再过滤**）+ `data/pipeline/<taskID>.json` 断点文件 |

### 2. 流水线（pipeline）与存活探测

每条运行中的任务在内存里有一条流水线：

```go
type taskPipeline struct {
    totalIPs  []string // 展开后的全部 IP
    offset    int      // 已经探测到第几个
    alivePool []string // 已确认存活、还没下发的 IP
    probing   bool     // 正在探测中（防止重复探测）
    dispatching bool   // 正在调度（防止重复下发）
    stopped   bool     // 流水线已废弃（重跑/停止/删除后旧协程必须退出）
}
```

| 项 | 值 / 行为 |
|---|---|
| 探测粒度 | `probeSliceSize = 500`（每片 500 个 IP） |
| 探测方式 | `scanner.AliveCheck.Probe()`（ICMP/TCP 存活） |
| 存活结果 | 追加进 `alivePool`，然后 `offset += 500`，接着立刻触发下一轮调度 |
| 关闭探测（`alive_probe=false`） | `offset` 直接置为满，**全部 IP 一次性进 alivePool**（用于禁 ping / 透明代理干扰的网段） |
| 边探边扫 | 池子攒够 `max(batchSize/5, 10)` 就先发一批，不等整段探完（稀疏网段否则永远发不出去） |
| 探测进度日志 | 每片开始/结束都写 `task_logs`：`[存活] 开始探测第 x-y 个…` |

### 3. 切分片并下发

| 项 | 细节 |
|---|---|
| 分片大小 | `scan_tasks.batch_size`（默认 100 个 IP；用户可改，30 左右更稳） |
| 选节点 | `findIdleNodeExcluding()`：节点 `status=online` + **心跳 < 2 分钟**（`nodeAliveWindow`）+ 未被熔断 + **该节点在本任务里没有 pending/running 分片** |
| 轮询 | 同一轮调度里每个节点最多拿一批（`usedNodes`），避免快的节点吃满、其它节点闲着 |
| 落库 | 一条分片 = `agent_tasks` 一行，`ip_ranges` 是逗号拼接的 IP，`batch_no` 自增 |
| 端口总数预估 | 下发时先按 `IP 数 × 端口数` 估一个 `total`，agent 上报真实值后覆盖 |
| 并发上限 | 一节点一任务同时只有 1 个分片在跑（`findIdleNode` 的第 4 个条件） |

### 4. 节点执行与回报

```mermaid
sequenceDiagram
    participant P as 平台
    participant A as agent（节点）
    P->>A: 分片写入 agent_tasks（pending）
    A->>P: GET /api/agent/nodes/:id/tasks
    P-->>A: 返回 pending 分片
    loop 每 2 秒
        A->>P: POST /api/agent/tasks/:id/progress {total,scanned,log}
        P-->>A: {cancelled: true/false}
    end
    A->>P: POST /api/agent/tasks/:id/finish {ok}
    P->>P: 分片置 done/failed → 触发下一批调度
```

- 分片日志：agent 上报的是**进程级累积日志**，平台按 `[agent-task #id]` 标记切出**本分片**的日志（`updateShardLog`），终端弹窗看的就是它。
- 停止信号：分片状态是 `cancelled` / `done` / `done_ai` / `stalled_ai` / `failed` 时，`/progress` 一律回 `cancelled:true`，agent 收到后杀掉本机扫描进程。

### 5. 三种「IP 回收」路径

> 分片没跑完就把 IP 放回 `alivePool`，由下一次调度重新派给其它节点。这是任务能自愈的关键。

| 触发 | 判定逻辑 | IP 去向 | 分片最终状态 |
|---|---|---|---|
| **节点主动报失败** | `FinishAgentTask(ok=false)` | 回 `alivePool`，`retry_count + 1` | `cancelled`（不计失败） |
| **节点掉线 / 熔断** | `RescheduleOrphanShards()` 每 30 秒巡检：心跳 > 2 分钟或已熔断 | 回 `alivePool` | `cancelled` |
| **扫描停滞** | 看门狗 `checkStalledShards()` 每 30 秒：`scanned` 不增长 **且** 日志不刷新 超过阈值 | `retry_count < 2` 才回池；达到 2 次不再回池 | `stalled_ai`（≥2 次时按进度转 `done_ai` 或 `failed`） |

**看门狗的两个判据**（缺一不可，两个都"死"才判中断）：

1. **进度活性**：`scanned` 在过去 N 分钟内涨过 → 视为活着；
2. **日志活性**：节点终端日志时间戳 `terminal_log_at` 在过去 N 分钟内变过 → 视为活着。

阈值 N：普通任务 **15 分钟**；`port_mode=all`（全端口）**60 分钟**（单片 100×65535 次探测，正常就要 30~60 分钟）。

### 6. 任务什么时候算「完成」

`CheckTaskAllDone()` 三个条件必须同时满足：

1. 存活探测全部结束：`probing == false` 且 `offset >= len(totalIPs)`
2. 待下发池已空：`len(alivePool) == 0`
3. 没有 pending/running 分片：`COUNT(agent_tasks WHERE status IN ('pending','running')) == 0`

满足 → `scan_tasks.status = done`。

特例：一个分片都没下发、且全部 IP 不可达 → 直接 `done`，日志写 `[存活] 全部 N 个 IP 均不可达，任务结束`。

### 7. 断点续传

| 项 | 细节 |
|---|---|
| 落盘文件 | `data/pipeline/<taskID>.json`（offset + 待下发池） |
| 写入时机 | 每批下发后、每片探测后、IP 回收后 |
| 恢复时机 | 服务启动时 `ensurePipeline()` 优先读文件（比 DB 的 `probe_offset` 更完整） |
| 废弃保护 | 任务重跑/停止 → `pipeline.stopped = true`，旧协程拿到标记后退出，不会凭空再造一条流水线并行跑 |

---

## 状态流转：所有状态机

### 1. 扫描任务 `scan_tasks.status`

```mermaid
stateDiagram-v2
    [*] --> pending: 创建（仅保存）
    pending --> running: 立即执行 / 到达 cron / 手动执行
    running --> done: 池空+探完+无活跃分片
    running --> failed: 调度异常（无可用节点等）
    running --> stopped: 人工停止
    done --> running: 重跑
    stopped --> running: 重新执行（从断点或全新）
    failed --> running: 重新执行
```

### 2. 分片 `agent_tasks.status`

| 状态 | 谁写的 | 含义 | agent 是否被杀掉 |
|---|---|---|---|
| `pending` | 调度器 | 已下发，等节点领取 | — |
| `running` | agent 首次上报进度 | 正在扫 | — |
| `done` | agent `finish(ok=true)` | 正常跑完 | 是（防止进程残留） |
| `done_ai` | 看门狗诊断 | 日志显示跑完但没收到结束信号，按完成收尾 | 是 |
| `stalled_ai` | 看门狗诊断 | 判定卡死，IP 回收重派 | 是 |
| `failed` | agent `finish(ok=false)` / 多次回收仍停滞 | 失败 | 是 |
| `cancelled` | 人工停止 / 节点掉线回收 / 失败回收 | 已回收，**不计入失败**，IP 已回池 | 是 |

```mermaid
stateDiagram-v2
    [*] --> pending: 调度器写入
    pending --> running: agent 上报进度
    running --> done: finish(ok=true)
    running --> done_ai: 看门狗判「跑完未收尾」
    running --> stalled_ai: 看门狗判「卡死」
    running --> cancelled: 停止 / 节点掉线 / 失败转重派
    pending --> cancelled: 未领取就被回收
    stalled_ai --> [*]: 回收次数≥2 时按 done_ai / failed 收尾
    failed --> [*]
    done --> [*]
```

### 3. 节点 `nodes.status`

| 状态 | 判定 |
|---|---|
| `online` | 每次心跳刷新 `last_heartbeat`，且 `status` 置 online |
| `offline` | 心跳超时（派发时按 **2 分钟** 判定 `nodeAlive`，不看库里的 status，避免用到过期状态） |
| 熔断 | 独立于 status：连续失败会进熔断期（`IsNodeFused`），熔断期内不派发 |

### 4. 服务 `services`

| 字段 | 流转 |
|---|---|
| `first_seen` | 首次扫到时写入，之后不再变 |
| `last_seen` | 每次扫到都刷新 |
| `miss_count` | 本轮没扫到 +1，**连续 3 次没扫到就删除该服务行**（`markMissingServices`） |
| `confirmed` | 处置确认后置 true；**端口级**，确认基线写 `confirmed_title` |
| `confirmed_title` | 该端口确认时的标题基线（迁移 039 新增） |
| `title_source` | `tscan`（扫描器给的）/ `fingerprint`（指纹命中）/ `ai`（AI 识别写回） |
| `prev_title` / `prev_fingerprint` / `prev_status_code` / `prev_content_length` | 发生变更时留旧值，待处置页展示「旧→新」 |
| `page_simhash` | 当前页面内容 SimHash |

### 5. 资产 `assets`（**一个 IP 一行**，`ip` 是唯一索引）

| 字段 | 取值 / 流转 |
|---|---|
| `source` | `ledger`（台账导入）/ `scan`（扫描自动建） |
| `status` | `pending`（有这个 IP 的待处置 web 端口）/ `confirmed`（已处置） |
| `system_name` / `owner` / `department` | 人工确认时写入；**这三个字段非空即视为「有确认痕迹」** |
| `confirmed_title` / `confirmed_simhash` | IP 级确认基线（端口级缺失时兜底） |
| `content_changed` | 页面内容 SimHash 与确认基线汉明距离 > 16 时置 true；重新确认后清零 |

**扫描时资产的建立规则**（`ingestService`）：

- 新 IP + **非 web** 服务 → 建资产 `source=scan, status=confirmed`（主机不需要确认）
- 新 IP + **web** 服务 → 建资产 `source=scan, status=pending`（进待处置）
- 已有主机记录又新发现 web 服务、且该资产没填过归属 → `status` 从 confirmed 转回 `pending`

### 6. 待处置页的四种呈现（判定顺序）

`listDisposalItems()` 逐端口判定，**顺序不能乱**：

| 顺序 | 条件 | 结果 |
|---|---|---|
| 1 | 该 IP 在 `assets` 里不存在 | `new` 新出现（红）；若该端口已确认则不显示 |
| 2 | `assets.source = ledger` | 走台账口径：端口未确认 → `in_ledger`；已确认且有变更 → `changed`；已确认且无变化 → **不显示** |
| 3 | 有确认痕迹（确认过标题/指纹，或填过归属） | 端口未确认 → `in_ledger`（已确认 IP 上的新端口）；已确认且有变更 → `changed`；无变化 → 不显示 |
| 4 | 只有扫描自动建的主机记录（无任何归属） | `new` 新出现（红） |

> `changed` 的两个独立来源：**标题与端口基线不同** 或 **`assets.content_changed = true`**。

### 7. 新旧识别合并口径（重要）

> 代码：`internal/scanner/merge.go` 的 `MergeIdentification()`。
> 目标：**避免刷出无意义「新变更」，但真实变更必须暴露。**

| 序号 | 条件 | 处理 |
|---|---|---|
| 1 | 旧值为空 | 用新值 |
| 2 | 新值为空 | 用旧值（扫描器这次没拿到标题，不覆盖） |
| 3 | 旧值是通用词、新值有信息量 | 用新值 |
| 4 | **两边都是通用词**（Login / Loading…） | **保留旧值**，不标变更（纯抖动） |
| 5 | **表意一致**（`titleSimilar`） | **保留旧值**，不标变更 |
| 6 | **表意不一致** | **用新值**，标记「新变更」 |

`titleSimilar` 的判定（三级，任一命中即算一致）：

1. 归一化后相同 —— 去大小写/空格/标点，并剔掉「系统/平台/管理系统/登录/首页/admin…」等噪声词；
2. 一方包含另一方，且长度比例 ≥ 0.5（例：`中化云盘` ⊂ `中化云盘 - 登录`）；
3. 字符 3-gram Jaccard 相似度 ≥ 0.8。

**页面内容变更**：`similarity.Changed(确认时 SimHash, 当前 SimHash, 16)` —— 汉明距离 > 16 即判定页面实质变更，
写 `assets.content_changed = true`，待处置页标黄。阈值取 16 是实测结果：同一系统小改版约 10~14，完全不同系统约 28~35。

### 8. 识别链路（AI 服务识别）

```mermaid
flowchart LR
    A["服务入库<br/>title_source=tscan"] --> B{"L0 规则：标题是否清晰"}
    B -->|"清晰"| Z["不动，结束（0 次 LLM）"]
    B -->|"不清晰"| C{"L0.5 本地产品库<br/>(标题/指纹/Banner)"}
    C -->|"命中"| Z2["直接用产品名"]
    C -->|"未命中"| D["L1 抓请求响应包<br/>最多 4 跳"]
    D --> E{"响应包再匹配本地库"}
    E -->|"命中"| Z2
    E -->|"未命中"| F["L2 一次 LLM<br/>title + product + category + sufficient"]
    F -->|"拿到标题"| Z3["写回 title_source=ai"]
    F -->|"sufficient=true"| Z4["信息够了但给不出名字，收手"]
    F -->|"sufficient=false"| G["L3 浏览器渲染（限流 3 并发）"]
    G --> H["L4 用渲染结果再调一次 LLM"]
    H -->|"拿到标题"| Z3
    H -->|"仍不行"| Z5["保持原值"]
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


## 所有功能介绍

### 首页
![alt text](docs/images/image.png)
首页主要是对各种数据进行汇总展示，并绘制趋势图。除此之外没有别的功能。

### 资产总览
![alt text](docs/images/image-1.png)
1. 根据可下载的模板导入现有台账，其中的组织和各组织负责人会被自动添加在组织结构页面。
2. 根据每一次扫描的结果查看态势，连续三次扫描均不存活的资产会被剔除。
3. 可以再次对资产进行标注。

### 资产处置
![alt text](docs/images/image-2.png)
1. 对扫描到的web服务均放置于此处，即便是导入的台账的资产依旧会需要再次处置。
2. 有三种标签，新出现，台账有，新变更。新出现指的是不在台账中的新被发现的web服务；台账有指台账中的被扫描到的资产；新变更指的是以前处置过的但是服务产生变更的。
3. 判断旧服务是否变更使用的技术是simhash,类似于yakit的页面相似度。

### 应用服务
![alt text](docs/images/image-3.png)
1. 对已处置的web服务进行展示和编辑操作。

### 合规检测
![alt text](docs/images/image-4.png)
1. 对扫描中发现的可直连的数据库，ssh等进行展示，同时可以一键复测。
2. 可以自定义规则。

### 扫描
![alt text](docs/images/image-5.png)
![alt text](docs/images/image-6.png)
1. 下发任务，可以选择节点和配置各项参数，任务成果会在最右侧显示，显示结果随每次任务刷新。
2. 鉴于目前tscanclient的存活探测功能失效，服务端采用了icmp+tcp的方式先判定存活探测，在探测过程中，依据设定的分片数动态给子节点下发任务，如果暂停再继续，存活探测可以断点续传，下发的没有完成的分片会重新下发。
3. 可以编辑扫描策略模板。
4. 可以设定定时任务，定时任务子页面可以看到每一次定时任务的详情。
5. 扫描任务中可以便捷的设置各类参数。

### 节点
![alt text](docs/images/image-7.png)
1. 一键加入集群，兼容linux和windows，可以选择单独下载控制文件或者完整下载（包含引擎），仅有控制文件无法完成扫描。
2. 根据节点性能可以设置每一个节点的并发。
3. 可以对子节点数据库进行管理，节约存储。
4. 可以设置熔断规则，对于连续失败的节点会熔断，规定时间内不再下发任务。
### IP规划
![alt text](docs/images/image-8.png)
![alt text](docs/images/image-9.png)
1. 预先导入一些内网段，扫描时一键选中。
2. 预先设置一些免扫描范围，扫描时自动排除。

### 组织结构
![alt text](docs/images/image-10.png)
1. 可以合并拆分组织，可以编辑组室内负责人。

### 故障和日志
![alt text](docs/images/image-11.png)
1. 显示故障和日志。

### 通知
![alt text](docs/images/image-12.png)
1. 可以通知，支持大多数常用的软件。

### 系统配置
![alt text](docs/images/image-13.png)
1. 一键升级，支持回滚。
2. 可以修改密码。

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

## 版本更新

### 发布（维护者）

打 tag 即可，GitHub Actions 会自动构建并推送镜像：

```bash
git tag v0.0.4
git push origin v0.0.4
```

产出：
```
ghcr.io/rongsec/cyberlook:v0.0.4         主镜像（后端 + 前端 + agent + TscanClient）
ghcr.io/rongsec/cyberlook-updater:v0.0.4 更新边车镜像
```

> 首次发布需在 GitHub → Settings → Actions → General 里允许 workflow 写入 packages（默认已允许）。

### 在线更新（使用者）

**方式一：页面点更新**

```
系统配置 → 系统更新 → 检查更新 → 更新
```

背后流程：
```
page 调 /api/system/update/apply {version}
  → 主应用转发给 updater 边车（http://updater:9999/update）
  → updater: pg_dump 备份 → 改 .env 的 APP_VERSION → docker compose pull server
             → docker compose up -d --no-deps server → 健康检查
             → 失败自动回滚到旧版本
```

**updater 边车**是唯一挂载 `/var/run/docker.sock` 的容器，主应用不碰 docker 权限。

**方式二：命令行兜底**

```bash
bash update.sh v0.0.4
```

脚本做同样的事（备份 → 改版本 → pull → 重启 → 健康检查 → 失败回滚）。

### 回滚

```bash
# 改回旧版本号，重新拉取
bash update.sh v0.0.3
```

数据库备份在 `data/backup/pre-update-<时间戳>.sql`。

### 部署形态说明

| 组件 | 更新方式 |
|---|---|
| 后端 + 前端 + agent | **镜像更新**（`docker compose pull`） |
| 数据库 schema | 启动时自动执行 `migrations/*.sql`（向前兼容） |
| 业务数据 | 挂载卷，不随镜像变化 |

## 版本记录

### v0.0.2（2026-10-26）

**扫描**
- 断点续传：探测进度 + 待扫池文件落盘，重启自动恢复
- 任务控制拆分：停止 / 暂停 / 继续 / 重跑
- 持续存活探测：不再等端口扫描消耗，池子持续积累
- 节点熔断：连续失败阈值触发，自动退避 + 分片重派

**资产**
- 修复处置后不从列表消失（`category=new` 未检查已确认状态）
- 修复处置后资产不出现在总览/应用服务（确认时自动创建 assets 记录）
- 首页「Web 资产」改为服务条数统计，与服务页口径统一
- 任务概览改为「本次执行」时间窗口，不再跨多次执行叠加

**组织**
- 从资产同步组织架构，支持一个部门多个负责人（逗号/顿号/分号/斜杠分隔）
- 部门编辑、合并、拆分
- 部门/负责人新建按钮（处置弹窗内）

**监控**
- 新增「故障和日志」页
- 故障记录保留期可配（默认 30 天，每日 03:00 清理）
- 服务端重启仅在中断运行中任务时记录，正常重启不产生噪音

**通知**
- 修复 webhook 失败被静默吞掉（现在解析 errcode/code，返回真实错误）
- 新增「故障告警」开关

**安全**
- 修复 `AgentTaskAuth` 未携带 token 时放行的问题（默认拒绝）
- 修复截图路径遍历、页面图片路径遍历
- 故障记录范围收窄，避免误报

### v0.0.1（初始版本）

- 分布式端口/Web 扫描、资产台账、处置、合规检测、页面快照、定时任务、通知、在线升级

## 扫描引擎

v0.0.25 起默认使用自研引擎（`internal/portscan` + `internal/svcid`，全量 FingerprintHub 规则），
不再依赖 TscanPlus。需要回退时给 agent 设置 `SCAN_ENGINE=tscan`。
实测对比见 `docs/端口扫描对拍报告-2026-11-09.md`。

## 许可证

**GPL-3.0** — 详见 [LICENSE](LICENSE)（GNU GENERAL PUBLIC LICENSE, Version 3, 29 June 2007）

本项目按 GPL-3.0 开源，**不作商业用途**。由此可以合法使用同样采用 GPL-3.0 的第三方数据
（如 Wappalyzer 系列规则库）；引入时须在 `THIRD_PARTY_NOTICES.md` 中登记来源与授权。

当前随项目分发的规则数据：
- `data/fingerprint/web_fingerprint_v4.json`、`service_fingerprint_v4.json` — FingerprintHub（0x727），**MIT**，
  随目录附 `LICENSE.md`
- 所有 Go 依赖均为 MIT / Apache-2.0 / BSD 类，**未引入 GPL 数据的 Go 模块**（详见 `THIRD_PARTY_NOTICES.md`）

> 若将来需要以闭源/一体机形式交付客户，必须先移除 GPL 数据依赖并重新评估协议，
> 因为 MIT 包装无法消除 GPL 数据集的 copyleft 义务。


**使用前请遵守**：
- 不得用于非法用途
- 扫描目标需获得授权
- 遵守当地法律法规
