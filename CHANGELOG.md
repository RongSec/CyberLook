# 更新日志

本文件记录每个正式版本的变更。发版前必须先在此登记，写明相比上个版本**优化**与**修复**的内容。

格式约定：
- 版本号按语义化版本（主.次.修订）
- 每版分「新增」「优化」「修复」三块，只写用户可感知的变化
- 内部重构、临时脚本不写

---

## [0.0.29] - 2026-11-13（扫描引擎收敛为单一实现）

### 变化

- **平台不再复制扫描引擎源码**：`internal/portscan`、`internal/svcid` 两份副本删除，
  改为 `require github.com/RongSec/CyberLook-Scan v1.3.0`。此前是"平台一份、引擎一份"，
  靠手工同步（EMFILE 修复、规则加载优化都各改过两遍），必然会漂。
  现在扫描逻辑只有一处：引擎仓改代码 → 打 tag → 平台 bump 版本，功能与性能天然一致。
- 新增 `scripts/check-engine-version.sh`：比对平台锁定版本与引擎最新 tag，暴露漂移
  （`STRICT=1` 时不一致返回非零，可挂 CI）。
- 引擎 v1.3.0 内容：fd/端口耗尽不再假阴性（错误分类+重试）、纯端口扫描不再加载 11MB 规则、
  存活探测端口集从 10 个扩到 16 个。



### 变化

- **存活探测在队列模式下真正生效，且改为 ICMP + TCP 两阶段**（此前形同虚设）：
  开关打开时由**服务端**先把目标 IP 探一遍 —— 先发 ICMP Echo（非特权 udp4，容器 NET_RAW 即可；
  无权限自动跳过该阶段），没回应的再做 TCP connect 到 16 个常见端口（SYN-ACK 或 RST 都算存活），
  只把判定存活的 IP 放进扫描队列；关闭则全量入队。探测过程有独立进度（已探 IP / 总 IP、命中数），
  零存活直接收尾并说明原因；分阶段统计（ICMP 命中 / TCP 补命中 / 不可达 / 判死 / 耗时）写进任务日志。
  根因：旧实现只存在于分片流水线，而队列模式下调度入口第一行就 return 了。
  **为什么必须两层**：只探 5 个 TCP 端口时，目标把 SYN 静默丢弃会让整台机器被判死。
  实测 10.193.0.0/16：65536 个 IP 探测出 605 台存活，其中 **312 台（52%）五个常用端口全不通**，
  按旧判法会全部漏掉；另有 310 台 ICMP 不通、只有 TCP 能判出来，两层缺一不可。
- **进度改为按 IP 单元计**：`已完成单元 ÷ 单元总数`。旧口径按探针加权，几万个 IP 的任务前几十分钟
  永远是 0%，且单 IP 全端口（65535）的单元权重远大于 top100，进度会莫名跳变。
- **去掉手动调速**：速率由节点按机器规格与运行状态自适应，界面只保留暂停/继续。
- **扫描设置精简**：删掉「扫描引擎」选择器（只有一种引擎）、「下发方式」说明、
  「禁用存活探测(-np)」「禁用启发式扫描(-nosmart)」等与引擎实现重复的行，只留
  「存活探测」与「启发式扫描」两个开关。



### 新增

- **智能调速**：节点按「fd 预算 / CPU 核数 / 内存上限」自动决定在途探针数，不再需要人工配
  K 与并发。天花板 = min(fd预算, CPU×350, 内存MB×3)，起步 3/4，每 10s 依反馈调整：
  出现 EMFILE ×0.6、CPU 连续两窗口 >90% ×0.8、CPU <70% ×1.15（不超天花板）。
  空闲窗口不动、暂停中不碰、人工调控后 5 分钟让位。`SCAN_INFLIGHT_AUTO=off` 可关闭，
  `SCAN_INFLIGHT_MAX` 可设硬上限。每次调整打一行 `[auto] 在途上限 A → B（原因；CPU/核数/内存/fd/窗口探针）`。
- 新增 `GET /api/nodes/rules`：下发指纹规则包（tar.gz，流式打包，令牌校验，空目录拒绝下发）。

### 优化

- 新增 agent 运行状态自报（每 60s 一行）：`goroutines / heap / rss / fds / 在途上限`，
  连续 5 分钟单调上涨会打 ⚠️ 警告 —— 判断泄漏靠趋势而不是等 OOM。
- 节点吞吐不再依赖人工试参数：同机实测 K/limit 从固定值改为自动后，小机器的失败重试明显减少，
  大机器能自己吃到更高的在途数。

### 修复

- **内存 / goroutine / fd 泄漏共 13 项**（专项审计后逐条修，以下是已确认会累积的）：
  - `internal/service/dispatch.go`：`pipelines`（每个任务持有完整 IP 清单，/16 就是十几万字符串）
    与 `stallCache` 任务结束从不清理 → 服务端常驻内存随任务数单调上涨，只能靠重启。
    现在任务终态时显式 `pipelines.Delete` + `delete(stallCache)`。
  - `internal/service/notify.go`：`smtp.NewClient` 失败分支漏 `conn.Close()` → 每封失败告警邮件
    漏一个 socket，持续告警时 fd 被吃光。
  - `cmd/agent`：HTTP 客户端无任何超时（`DefaultClient` 零值）→ 服务端半死不活时
    心跳/领任务的请求永久阻塞，goroutine 与连接都回不来。补 `ResponseHeaderTimeout=30s`
    与总超时 120s。
  - `cmd/agent`：`claimUnits` 返回 `(nil, nil)` 时直接读 `c.Cmd` → nil pointer panic（节点挂掉）。
  - `cmd/agent`：取消后仍按空位继续 claim（只在 `inflight==0` 才退出循环）→ 取消后又有单元被领走、
    扫完上报失败，凭空产生 failed_unit。改为取消即停止补位、只等在途收工。
  - `internal/scanner/builtin.go`：K 路并发首次载入规则时 N 个 goroutine 同时解析 11MB JSON →
    加锁串行化（后到者直接命中缓存）；结果集 `all` 与 `out` 双份保留 → 拷贝后立即 `all = nil`。
  - `internal/scanner/auditdb.go`：清理只删行不回收空间，WAL 只增不减 → 清理后补
    `PRAGMA wal_checkpoint(TRUNCATE)` + `VACUUM`。
  - `cmd/agent`：新增单元硬超时（10min ~ 2h，按探针数×超时估算）—— 暂停是"冻结"不是取消 ctx，
    控制链路失联时单元会永久挂在 limiter 上。
- 裸机安装不下发指纹规则（与镜像那个缺陷同源，但脚本是另一条路径，之前只修了镜像）：
  `install.sh` / `install.ps1` 只下载 `scope-agent` 二进制，没拉 `data/fingerprint`，
  systemd 单元也没有 `SCAN_RULES_DIR` 与 `LimitNOFILE`。结果是裸机节点「端口扫得到、服务名与指纹全空」，
  日志只有一行「规则载入失败，仅做端口扫描」——静默降级。现在安装脚本会拉规则并校验文件数
  （<2 个直接失败），systemd 单元写入 `SCAN_RULES_DIR` 与 `LimitNOFILE=65535`。

## [0.0.27] - 2026-11-11（彻底替换 TscanPlus：自研引擎流式化）

### 新增

- **流式扫描引擎**（自研引擎成为唯一引擎）：扫描发现开放端口即刻投喂识别工作池
  （默认 32 并发，`IDENT_CONCURRENCY` 可配），识别完成进聚合器——攒满 200 条或
  300ms 未发即 flush 上报。此前是「全部端口扫完 → 逐个识别 → 一次性回调」，
  首条结果可见时间从分钟级提前到秒级；agent 侧 `postResults` 逻辑零改动复用。
- **审计本地落库（仅 agent）**：每批扫描结果旁路写入 agent 本地
  `config/config.db`（ipscan/urlscan 表，沿用原 TscanPlus 表列子集），供事后排查
  上报链路；写库失败只记日志、绝不影响扫描。任务 ingest 完成后由平台清理指令
  按 Project 删行，防止占满磁盘。「节点管理 → 数据库管理」入口原样可用。
- **实时进度规范**：引擎每秒 ≤1 行进度（探针 done/total、当前 pps、开放数、
  已识别数），格式兼容既有解析（`(a/b)`、`N%`、`[已用:剩余]`）。
- **规则/探针索引包级缓存**：FingerprintHub 全量载入（约 1.4s）只付一次代价，
  按文件 mtime 自动失效（rules-update 换库后下一任务即生效）。
- **多平台出包**：agent 覆盖 linux amd64/arm64/386/mipsle(softfloat) + windows amd64
  （纯 Go 无 CGO，麒麟等低版本 gcc 环境无关）；`scripts/build-agents.sh` 一键产出。
- **任务调控接口** `POST /api/tasks/:id/control`（当前仅 `cancel`，收敛进既有停止链路）。

### 优化

- **纯端口扫描不再加载 11MB 指纹规则**：`RunBuiltinPortScan` 原来每次进来都无条件
  `loadRulesCached()`（FingerprintHub 全量 11MB，首解析约 1.4s）。队列模式下节点并行跑 K 个单元，
  首轮就是 K 个 goroutine 同时解析同一份 11MB → CPU/内存雪崩。**现象极具迷惑性**：agent 日志只有
  「单元领取成功」、一条引擎行都没有，任务零完成、单元被租约回收判死——只有顺着「缓存未命中 × 并发」
  才能想到。纯端口扫描本来用不到规则（随后 `identOpt.Rules` 就被置 nil），现在按 `wantIdent` 跳过：
  K=16 同参数从「75s 零完成 / 32 单元隔离」变为「33s / 25,600 探针 / 776 pps」。
- **节点内并行单元（K=6，`QUEUE_UNIT_CONCURRENCY` 可调 1~32）**：IP 租约队列原来
  「领 1 个单元 → 扫完 → 再领」，单 IP 的 2s 级延迟直接决定节点吞吐（实测 47 pps，
  /26 段要 9 分钟）。现在一个节点同时持有 K 个在途单元、单次 claim 批量领 K 个
  （服务端一条 `UPDATE … IN (SELECT … FOR UPDATE SKIP LOCKED LIMIT n)`，仍是原子），
  端到端回到分钟内的秒级量级。**K 路扫描共用同一个限速器**
  （`scanner.ScanOptions.Ctrl`），所以 K 只放大租约在途数、不放大发包速率，
  `/control limit` 与暂停对全部在途单元同时生效。
  ⚠️ 调大 K 前必须同时放开容器 fd 上限：docker 默认 `nofile=1024`，而自研引擎是
  connect 扫描（每个在途探针一个 socket），K×单元端口数 > ~1000 时会撞 fd 墙，
  表现为单元迟迟不回报、租约到期被 reaper 隔离（实测 K=16 时 32 个单元被隔离）。
  compose 已加 `ulimits.nofile: 65535`；默认 K=6（6×100=600 在途）在 1024 下也安全。
- **取消即保留部分结果**：节点收到取消指令后引擎立即收敛，取消前已识别的批次
  照常 flush 入库，不再丢弃整轮扫描（旧 tscan 链路 kill 进程即全丢）。
- 心跳/进度、SSE 唤醒、分片调度等控制面协议不变；`/progress` 响应新增 `cmd`
  字段（当前恒为 none，为后续调控留通道）。

### 移除

- **TscanPlus 彻底移除**：删除 TscanClient 执行链（os/exec 启动、PTY、500ms
  轮询 config.db）、`identifyEnhanced` 旧识别链、`tscan-binary` 分发路由、
  install.sh/install.ps1 的扫描器下载步骤、`third_party/` 整目录（引擎二进制/
  POC 库/License 全部不再需要）、节点 License 配置 UI、`SCAN_ENGINE` 环境变量、
  `-engine-db` flag；`creack/pty` 依赖随 tidy 移除。
  v0.0.27 起**仅自研引擎**；`task.engine` 字段保留兼容存量数据（任意值均走自研）。

### 修复

- **K>1 并行单元下 `/control limit` 打不上、pause 只冻结一个单元（"动态调速"是假功能）**：
  共享 limiter 原来按**单元级 projectID** 投影（`ControlScan(proj)`），而 K 路单元共用的是另一份
  ctrl——K=1 时两者恰好重合，所以单单元测试是好的，一到 K>1 旋钮就失效；更糟的是每轮 claim
  又用节点默认并发重跑一次 `SetLimit`，把刚下发的 60 盖回 1500（现象=「生效一瞬间又回弹」）。
  现在：① 共享 limiter 改为 **taskID 键**（`EnsureTaskCtrl/ApplyTaskCtrl/UnregisterTaskCtrl`），
  K 个单元取同一个 ctrl，任一单元心跳都能投影；② 只有创建时用节点并发定初值，复用路径禁止
  `SetLimit`；③ 投影带单调递增 `seq` 做乱序防护（老心跳晚到丢弃）；④ 任务收敛释放句柄防泄漏。
  实测：K=6 基线 300pps → limit60 稳定段均值≈60 → 改回 600 恢复 300；K=32 全程 limit60
  墙钟 455s（基线 23.8s，19×）且 probes 25600/25600、open 327 零丢失；pause→0、resume→恢复均 ≤2s。
  单测 `internal/scanner/taskctrl_test.go` 四条（复用不重置 limit / seq 乱序丢弃 / pause-resume+生命周期 /
  16 goroutine 并发投影终值由最大 seq 决定）。
- **清理 tscan 命名残留（卷改名需注意审计库不自动迁移）**：compose 里 agent 的审计库卷仍叫
  `agent-tscan-config`（装的其实是自研引擎的 `config.db`），改名 `agent-engine-config`；
  `.dockerignore` 里匹配 `third_party/TscanPlus-*` 的死规则删除（仓库里已无 third_party 内容，
  注释却还写着"保留 tscan 供分发下载"）；README 里"扫描引擎 = TscanClient""`SCAN_ENGINE=tscan` 可回退"
  等失效描述改为自研引擎唯一。**升级注意**：旧卷不会自动迁移，审计库仅用于排障、丢一次可接受；
  需保留时 `docker run --rm -v <项目>_agent-tscan-config:/from -v <项目>_agent-engine-config:/to alpine cp -a /from/. /to/`。
- **`.dockerignore` 把指纹规则也排除了，agent 镜像直接构建失败**：`data` 一行把整个 data 目录
  （含 `data/fingerprint`）挡在构建上下文外，`docker compose build agent` 报
  `/data/fingerprint: not found`。改为 `data/*` + `!data/fingerprint`，只排运行期数据。
- **本地 fd/端口耗尽不再被当成"端口关闭"（假阴性，安全扫描器最致命的错）**：
  connect 扫描里 dial 失败一律算"未开放"，agent 撞 fd 上限（EMFILE/ENFILE/ENOBUFS/EAGAIN）时
  connect 是秒失败 → 每个探针都记 closed → 单元"正常完成、开放=0" → 对外输出"该主机没有开放端口"。
  现在：① 探针错误分类（`errors.As` 解 net.OpError/os.SyscallError/syscall errno + 文本兜底）；
  ② 命中本地资源错误不记 closed，让出许可退让 200ms 后重试，最多 3 次；
  ③ 单元内资源错误占比 >0.5% → 返回 `ErrLocalResourceExhausted`，单元按失败退回队列（走 attempts/隔离），
  **绝不落 done**；低于阈值只 WARN 留痕；④ agent 启动读 `RLIMIT_NOFILE`，soft<2048 时打印醒目告警
  并把扫描在途上限钳到 soft-256（soft≥8192 时按 1500 运行）。
  单测 `internal/portscan/resource_test.go` 注入假 dialer 连续 EMFILE，断言"不判 closed / 确有重试 /
  最终返回资源错误"。
- **agent 缺指纹规则时静默降级为纯端口扫描**：`Dockerfile.agent` 未打包 `data/fingerprint`，
  agent 容器内 `SCAN_RULES_DIR=data/fingerprint` 不存在，引擎只记一行「规则载入失败」
  就继续扫，扫出来「已识别」恒为 0、指纹/产品全空（线上 v0.0.27 实测踩到）。
  现在镜像内置规则（`COPY data/fingerprint /app/data/fingerprint` +
  `ENV SCAN_RULES_DIR=/app/data/fingerprint`），compose 保留只读挂载覆盖入口（默认注释）。
  部署验收见 README「扫描引擎」节。
- **删除任务不通知节点**：`DELETE /api/tasks/:id` 只删库，agent 手上的单元没有任何信号，
  会把在途单元扫完才停（实测删除后仍在打 #664 进度 98%），期间的进度/结果上报都打在
  已删除的会话上。现在删除前置一步与「停止」完全相同的取消广播（会话置 cancelled →
  agent 心跳拿到 `cancelled:true` → ≤5s 内 StopScan 并退出该任务领取循环），
  服务端再等执行方真正松手（最长 8s，超时强制回收）后才删行；`agent_tasks`/`task_runs`
  一并级联清理。批量删除先统一广播再逐个删，避免 8s×N 串行。前端删除确认文案对
  运行中任务补充「会先停止扫描」。

---

## [0.0.26] - 2026-11-10（扫描引擎修复）

### 修复

- **中文产品名乱码**：`svcid.normalizeProduct` 的 Title Case 分支按字节切首字符
  （`p[:1]`），UTF-8 中文首字节被切碎后序列化成 U+FFFD（三方对拍报告 §八 缺陷①，
  实测 13 行受影响）。改为仅对 ASCII 首字节分段做大写首字母，非 ASCII 分段原样保留。

---

## [0.0.25] - 2026-11-10（平台深度适配）

### 新增

- **任务级引擎选择**：ScanTask 新增 `engine` 字段（`builtin`=自研默认 / `tscan`=TscanPlus 回退），
  迁移 040。任务创建/编辑 API、Plans.vue 表单均支持；不指定时按 `SCAN_ENGINE` 环境变量走全局默认。
- **扫描设置页**：新增「扫描引擎」下拉；noPing/noSmart 提示更新为双引擎语义说明。

### 修复

- **ScanResult 字段映射对齐 TscanPlus 语义**：Title←页面 title、Fingerprint←技术栈、
  Response←HTTP 响应体（ingestService 算页面 SimHash 依赖它）、StatusCode/ContentLength←HTTP 实值、
  OS←SMB/RDP 握手、Mid←产品。此前 Response 误填 Banner、StatusCode 恒空，会导致 content_changed 误判。
- **超时单位 bug**：本地任务路径把秒当毫秒传给扫描器（task.Timeout=3 → 3ms）。
  现在统一按毫秒传（`task.Timeout*1000`），引擎侧兼容 <100 视为秒。
- **默认端口对齐**：未指定 Ports 时用 Top100（与 TscanClient 一致），此前是 top1000。
- **Modules/Finger 参数语义**：Modules=port 时跳过 HTTP 识别；Finger=tiny 时不做主动探针。

---

## [0.0.25] - 2026-11-09

### 新增

- **自研端口扫描 + 全量指纹引擎**（v0.0.25 起默认使用；`SCAN_ENGINE=tscan` 可回退）：
  - `internal/portscan`：connect 全连接扫描，并发池 + 进度 + 取消；实测 655,350 探针 / 208s / **3,151 探针每秒**，
    对 133 个真开放端口**精确率 100%、召回 97.7%**（TscanPlus 同条件 2,454/s、97.9%、69.9%）。
  - `internal/svcid`：FingerprintHub **全量 15,388 条**（web 3450 + service 11938，用户要求不裁剪），
    Aho-Corasick 索引 4,737 pattern / 98,297 节点，载入 215ms；含 favicon md5/mmh3、TLS 证书线索、
    自研被动协议探针与 11938 条主动探针分组执行。
  - `cmd/server/bench.go`：`bench-scan` 与 `--reidentify`，用于不落库的实测与对拍。
  - 详细数据见 `docs/端口扫描对拍报告-2026-11-09.md`。
- **协议变更为 GPL-3.0**（用户确认开源、不商用）：`LICENSE` 换成 GNU GPL v3 全文，
  README 授权章节更新，新增 `THIRD_PARTY_NOTICES.md` 登记第三方数据与依赖授权。

### 已知不足（下轮处理）

- 指纹证据覆盖率 13%，低于 tscan 的 63%：694 条 PCRE 正则 Go RE2 不兼容；
  SMB/RDP/DCERPC/WinRM 等 Windows 协议的最小握手尚未实现。
- v2 参数仍有 3 个偶发漏报（超时端口需二次复测）。

---

## [0.0.24] - 2026-11-09

### 优化

- **识别链路去掉一次纯浪费的 LLM 调用**：原来的「AI·信息充分性判定」输入的正是刚被判过"不清晰"的
  标题/指纹，等于花一次调用问模型"够不够"（实测 100 条里只有 5 条据此提前收手）。
  改为**本地产品指纹库**（ESXi / Spring Boot / Kubelet / Tomcat / Lotus / SAP / 深信服 / 用友…约 45 条规则）
  做前置闸门，命中零成本直接出结果。
- **抓包后再过一遍本地库**（Server 头 + 页面摘要），命中就不再调模型。
- **合并 AI 调用**：识别结果里同时返回 `sufficient`（信息是否够），模型说够但给不出名字就不再渲染。

### 修复

- **浏览器渲染 85% 失败**：6 路并发时 chromium 抢内存崩溃（实测 72 次渲染失败 61 次）。
  现在渲染限流 3 并发 + 失败重试 1 次。
- **「未知业务系统」被当成有效标题**：`TitleClear` 只精确匹配「未知」，模型输出
  「未知业务系统（Spring Boot 默认错误页）」这种带括号的拦不住，审计里 10 条被算作"识别成功"。现在包含「未知/无法识别/未识别」一律不采纳。
- **分片被无限次回收**：`shardMaxRetry=2`、`shardStallAcceptRatio=90` 只在常量里声明、从未使用（死代码），
  导致同一批 IP 被反复重派（任务 #115 连判 19 次）。现在回收次数达上限即收尾：进度 ≥90% 转 `done_ai`，否则 `failed`。

---

## [0.0.23] - 2026-11-09

### 优化

- **新旧标题合并改为「表意一致就用旧」**：原来只做"去标点空格后严格相等"，
  `中化云盘` → `中化云盘 - 登录` 会被判成变更，刷出大量无意义「新变更」。
  现在按三级判定（归一化去噪声词 → 包含关系 ≥50% → 3-gram Jaccard ≥80%），一致就保留旧值；
  不一致才用新值并标变更；两边都是通用词（Login/Loading）的抖动也保留旧值。

### 新增

- **识别链路审计子命令**（dry-run，不写业务表）：
  `asset-server audit-identify --unclear 100 --clear 30 --workers 4 --out /tmp/audit.json`
  用真实服务跑完整识别链路，输出每条的分级命中、最终标题、耗时与汇总（LLM 调用次数、各级命中分布）。

---

## [0.0.22] - 2026-11-09

### 新增

- **节点进度每个分片新增「诊断记录」按钮**：之前 AI 诊断结果只写库、界面只弹一个 toast，事后完全查不到。
  现在能看诊断结论、说明、时间、扫描进度，以及该分片的监控/诊断日志（接口 `GET /agent-tasks/:id/diagnose-log`）。

### 修复

- **分片被判「AI中断」后没有通知节点停止扫描**：`AgentProgress` 只对 cancelled/done 回 `cancelled=true`，
  `stalled_ai` 没管 → 节点上旧扫描进程继续跑，调度又把新分片派给同一个节点 → 两个进程抢资源 → 双双变慢再被判中断（雪崩）。
  现在 done_ai / stalled_ai / failed 也会让 agent 终止进程。
- **看门狗误杀全端口扫描**：判据只有「节点日志 15 分钟没刷新」，而 `port_mode=all` 的分片是 100×65535 次探测，
  正常要 30~60 分钟，tscan 十几分钟不打印日志很常见（任务 #115 连续 19 个分片被判中断、反复重派）。
  现在：① `port_mode=all` 阈值放宽到 60 分钟；② 增加「scanned 是否在增长」作为活性信号，进度在涨就不判死。
- **同一分片被重复诊断**：看门狗每 30 秒扫一次、诊断是异步的，同一分片会连续触发多次
  （实测 #624 一秒内写了 7 条相同的 AI诊断日志，IP 被重复回收进待扫池）。现在加了 in-flight 去重；
  且只有真正把状态从 running 改掉的那一次才写日志、才回收 IP。
- **规则判定用错日志导致「完成」误判**：`heuristicVerdict` 读的是**节点累积日志**，里面残留着别的分片的「扫描结束」，
  分片 #632 只扫了 66% 就被判「已完成」。现在读**本分片日志**，且进度 <95% 时不再认完成标识（交给模型判）。

---

## [0.0.21] - 2026-11-09

### 修复

- **勾选「新出现」的行时「按台账确认」是灰的，点了没反应**：现在只要有勾选就能点，
  但若所选资产不在台账里，会明确提示「请用『批量处置』填写部门/负责人来登记归属」，
  按钮也加了悬浮说明。之前这种情况只会表现为"按钮失效"。

---

## [0.0.20] - 2026-11-09

### 修复

- **「按台账确认」依旧确认不掉（v0.0.18/0.0.19 只修了部分）**：真正的漏洞是 **标题为空** 的端口。
  端口基线为空时会回退到 IP 级基线（如 `Loading...`），`"" != "Loading..."` 恒成立 → 确认完仍判「新变更」。
  现在 **只看该端口自己确没确认过**：确认过就比「自己的基线 vs 当前标题」（空对空=没变化），
  没确认过就是「台账有」，不再混用 IP 级状态。
- 存量 1030 个"已确认但没有端口基线"的端口已回填基线（多端口 IP 以当前标题为准，单端口沿用资产级旧基线）。

---

## [0.0.19] - 2026-11-09

### 修复

- **「按台账确认」后行不消失，反而变成「新变更」并永久卡住**（v0.0.18 只修了一半）：
  根因是「已确认」分支里还拿 **标题清不清晰** 当变更依据 —— 像 `webserver`、`Loading...` 这类标题
  永远被判为"不清晰" → 确认完立刻又是「新变更」→ 再确认还是 → 死循环。
  现在已确认的端口**只看标题/内容与确认基线是否真的不同**；"标题不清晰"只用于提示未确认的行。
  实测「新变更」从 1071 条回落到 260 条（这批本来就是确认过、没变化的）。

---

## [0.0.18] - 2026-11-09

### 修复

- **「按台账确认」点了没反应、确认完又变回「新变更」**：确认时的标题基线写在了 `assets.confirmed_title`（一个 IP 一个值），
  而且更新语句带了 `source = 'ledger'` 条件 —— 扫描建的资产基线压根没写进去，于是对不上基线 → 立刻判「新变更」→ 永远确认不掉。
  现在基线按**端口**记录（新字段 `services.confirmed_title`，迁移 039），确认时每个 web 端口各写各的标题。
- **批量处置后行不消失**：`confirmOneIP` 只改资产、没标记服务已确认，也没写基线。已补齐。
- **多端口 IP 互相误判「新变更」**：一个 IP 多个端口标题不同，却只有一个 IP 级基线，除一个端口外全对不上。
  存量已用迁移回填（多端口 IP 的已确认端口以当前标题为基线；单端口 IP 保持原基线，不洗掉真实变更）。

---

## [0.0.17] - 2026-11-09

### 优化

- **资产处置页勾选改为「精确到端口」**：勾一行只选这一行，「复制 URL」可以只拿单条；
  同一 IP 有多个端口时，IP 列右侧出现「N 端口」按钮，一键选中/取消整个 IP。
  「删除选中」同步改为只删选中行（不再连带同 IP 的其它端口），并加了二次确认。

### 修复

- **已确认过的 IP 上又发现新端口，被标成「新出现」**：分类只认 `source='ledger'` 的台账，
  扫描确认过的资产（`source='scan'`）被当成不存在。现在只要该 IP 有**人工确认痕迹**
  （确认过标题/内容指纹，或填过归属），新端口归「台账有」。
  仅扫描自动建过主机记录、没有任何归属信息的 IP 仍算「新出现」，否则该分类会被淹没
  （实测：改后 47 个端口/16 个 IP 从「新出现」转为「台账有」，另有 932 条保持原样）。

---

## [0.0.16] - 2026-11-09

### 优化

- **资产处置页「台账登记」拆成三个独立筛选条件**：系统名 / 负责人 / 部门各自一条条件（AND 组合），
  不再把三者拼成一个字符串做联合模糊匹配。

### 修复

- **按「台账登记」筛选永远 0 条**：该列是聚合列（key=`ledger`），后端返回的是
  `ledgerSystem`/`ledgerOwner`/`ledgerDept`，检索时取 `it.ledger` 恒为空。现在检索字段直接映射到真实字段。
- **应用服务页「N 条」重复显示两次**：模板里同一个计数标签写了两遍。
- **应用服务页「导出 URL」按钮点击无反应**：`exportUrls` 函数从未定义（handler 为 undefined）。
  现在按**当前筛选条件**导出全量 URL（一行一条，TXT，UTF-8 BOM，IP 按数值升序）。
- **Excel 导出忽略台账条件**：处置导出只认 `ledgerSystem`，服务导出缺 `systemName`，
  勾了筛选导出的结果和页面不一致。两处已补齐，与列表接口口径统一。
- **应用服务页翻页后点「查询」，页码停在旧页但数据回到第 1 页**：后端只返回 `{total, items}`，
  前端拿 `res.page/res.size` 恒为 `undefined`，分页器成了非受控组件。改为以本地的 `page/pageSize` 为准。

---

## [0.0.15] - 2026-10-31

### 修复

- **Dockerfile.agent 语法错误导致 agent 镜像构建失败**：`ARG` 被写进 `RUN` 里（`RUN ARG VERSION=dev`），
  且 `-ldflags` 出现两次（后者覆盖前者，版本注入丢失）。改为独立 `ARG` + 单个 `-ldflags`。
- **`.env.example` 的 `APP_VERSION` 还是 `v0.0.5`**：照它生成 `.env` 会停在旧版本。改为 `v0.0.15`。
- **页面更新后误报「更新失败」**：server 容器重建时 HTTP 流必然断开，前端把断流当失败。
  现在断流后自动轮询 `/api/health`（最多 90 秒），版本对上就显示成功并自动刷新页面。

### 优化

- compose 的 agent 服务加 `build.args.VERSION`，本地构建时也能注入版本号。

---

## [0.0.14] - 2026-10-31

### 修复

- **updater 容器内路径解析导致容器无法启动**：`docker compose` 在 updater 容器里运行时，
  相对路径 `./data/files` 被解析成 `/project/data/files`，原样交给宿主机 Docker 守护进程，
  而宿主机没有这个目录，容器创建成功但启动失败。
  现在 volumes 用 `${HOST_PROJECT_DIR:-.}` 确保容器内外路径一致。
  `.env`/`init.sh`/`init.ps1`/`.env.example` 都已包含 `HOST_PROJECT_DIR` 配置。

### 优化

- `Dockerfile.agent` 加版本号注入（`-X version.Version`），agent 版本不再显示 dev。

---

## [0.0.13] - 2026-10-31

### 修复

- **updater 内 compose 项目名不一致导致容器冲突**：updater 容器内 `docker compose` 从目录名 `/project`
  推导项目名为 `project`，与宿主机的 `asset-platform` 不一致，导致 `up -d --force-recreate` 时
  容器名冲突。现在 `.env` 显式指定 `COMPOSE_PROJECT_NAME=asset-platform`，
  `init.sh`/`init.ps1`/`.env.example` 都已包含此配置。

---

## [0.0.12] - 2026-10-31

### 测试版

- 端到端验证更新流程：页面按钮 → 流式日志 → 自动更新 → 健康检查
- 无功能变更，仅用于测试 v0.0.11 的更新加固是否生效

---

## [0.0.11] - 2026-10-31

### 修复（更新功能全面加固）

- **开发版不再触发自动更新**：`CompareVersion` 对 `dev`/`latest`/空版本的处理不当，
  导致任何版本都比 `dev` 新。现在开发版直接返回 `hasUpdate=false`。
- **compose 去掉 `build:` 段**：镜像找不到时 compose 会回退到本地构建，
  构建产物版本号是 `dev`，造成「标签是新版、版本是 dev」。生产环境只用镜像，不本地构建。
- **后端加并发锁**：`ApplyUpdate` 用 `sync.Mutex.TryLock()` 防止多个请求同时触发更新。
- **`.env` 空文件处理**：`os.ReadFile` 失败时不再只写一行 `APP_VERSION=...`，
  而是保留默认的 `POSTGRES_PASSWORD=asset123`。
- **更新后清理旧镜像**：`docker image prune -f` 释放磁盘空间（失败不影响更新结果）。
- **版本号校验**：目标版本为 `dev`/`latest`/空时拒绝更新，避免误操作。

---

## [0.0.10] - 2026-10-30

### 修复

- **updater 硬编码项目名导致容器冲突**：compose 命令写死 `-p asset-platform`，
  当 `.env` 里 `COMPOSE_PROJECT_NAME` 不是 `asset-platform` 时，`up -d --force-recreate`
  会创建属于 `asset-platform` 项目的容器，与现有容器名冲突。
  去掉 `-p`，让 compose 自动读 `.env` 里的 `COMPOSE_PROJECT_NAME`。
- **rollbackEnv 写坏 .env 版本号**：`version.Version` 不带 `v` 前缀（如 `0.0.8`），
  回滚时直接写入 `APP_VERSION=0.0.8`，而 GHCR tag 是 `v0.0.8`，导致下次 compose 找不到镜像
  会回退本地构建（版本变 dev）。现在统一补 `v` 前缀。

---

## [0.0.9] - 2026-10-30

### 优化

- **更新日志实时显示**：点击「更新」后每执行一步立即显示日志（备份/切换版本/拉取镜像/重启/健康检查），
  不再是转圈等结果。updater 改为流式响应（text/plain + flush），后端透传，前端 fetch ReadableStream 实时渲染。
- **更新按钮文案**：更新中显示「更新中…（预计 2-5 分钟）」。

### 修复

- **更新超时假报错**：axios 默认 60 秒超时导致拉镜像时报「更新失败」，改为 fetch（无超时限制）。
- **updater 挂载路径错误**：`/project` 挂载的是工作区根目录，`docker-compose.yml` 在子目录里导致
  `compose up` 报 `no configuration file provided`。改为挂载 `asset-platform` 子目录。
- **备份目录不存在**：`data/backup/` 目录不存在导致备份失败，updater 启动时自动创建。

---

## [0.0.8] - 2026-10-30

### 修复

- **首页统计口径错误**：
  - 「Web资产」「已登记资产」两个数字完全相同（1632）——SQL 写重复了。
    改为：Web 资产 = 有可达 Web 服务的 IP 去重数（746），已登记资产 = assets.status=confirmed（660）。
  - 「新存活主机」显示 12852 台——只有一次扫描时把全部存活 IP 当成「新存活」。
    改为：只有 ≥2 次扫描才计算差值，单次扫描返回空。
- **趋势图颜色区分度低**：Web资产/待确认/导入资产三条线颜色相近（橙黄系）。
  改为蓝/绿/橙/紫/红五色，线宽 2.4→3.5，加阴影。
- **扫描节点控制文件版本显示 dev**：Dockerfile 只给 server 注入版本号，
  agent 的三个编译产物（linux/amd64、linux/arm64、windows）都没注入。
  现已全部注入，CI 构建后 agent 版本号与 tag 一致。

### 优化

- **扫描节点控制文件自动更新**：心跳时自动比对 agent 版本与服务端版本，
  不一致就自动下发升级（不再依赖手动点「升级」按钮）。
- **API 文档**：26 条接口描述从机械文案改为中文说明；补充 import-conflicts 三个接口。
- **README**：
  - 部署段补 git clone https://github.com/RongSec/CyberLook.git
  - cd asset-platform 改为 cd CyberLook
  - 环境要求 Go 1.21+→1.26+、Node.js 18+→22+
  - 新增「拉取镜像部署」方式（跳过编译）

---

## [0.0.7] - 2026-10-29

### 修复

- **检查更新后按钮仍显示「已是最新」**：`HasUpdate` 原判断依赖 Release 里的二进制附件，
  而 0.0.4 起改为镜像发布后不再产出附件，导致版本检测成功但无法点更新。
  改为按语义化版本号比较（`v0.0.7` vs `当前版本`）。
- **「当前版本」显示的是二进制哈希**：改为真实版本号。
- **镜像内版本号与 tag 无关**：Dockerfile 之前只有 `-ldflags="-s -w"`，
  版本号永远等于 `version.go` 里的字面值。现改为构建参数注入：
  `-X asset-platform/internal/version.Version=${VERSION#v}`，
  CI 传 `github.ref_name`，本地构建传 `APP_VERSION`。
- **镜像只有 amd64，Apple Silicon 无法拉取**：
  `release.yml` 增加 `platforms: linux/amd64,linux/arm64`。
  之前 `docker compose pull` 在 arm64 上报 `no matching manifest`，
  compose 回退到本地构建，把旧二进制打上新 tag，造成「标签是新的、版本是旧的」。

### 优化

- `version.go` 默认值改为 `dev`，由构建注入真实版本，避免手改漏改。

---

## [0.0.6] - 2026-10-29

### 新增

- **检索栏（SearchBar）**：资产总览 / 资产处置 / 应用服务三页统一换用新的条件胶囊检索框
  - 一个带边框的容器，每个字段一个小方块，点击方块直接改字段 / 条件 / 值
  - 方块常驻覆盖当前表格所有列，不留「+ 筛选」按钮
  - 查询 / 高级查询 / 重置 三个按钮与检索框同一行
- **高级查询**：SPL 输入框 + 条件积木（默认 1 行，可加条件 / 条件组），按钮同样与输入框同行
- **固定选项字段改用下拉**：是否有WEB、来源、状态、类别、协议、响应码
  - 固定选项字段的操作符只保留「等于 / 不等于」
  - 响应码支持 `filterable + tag + 多选`（可搜可自定义，可多选）
- **清空按钮**：检索面板底部由「删除条件」改为「清空」（只清值，保留条件行）
- **重复资产处理**（资产总览 → 导出右侧）
  - 台账重新导入时，**已人工确认的资产不再被覆盖**，冲突转入侧边抽屉
  - 抽屉卡片可**直接编辑**系统名 / 负责人 / 部门，再「按旧」或「应用修改」
  - 支持「全部按旧」「全部按新」批量处理
  - 系统名、责任人、部门三项完全相同的冲突**不再列出**
- **多责任人支持**
  - 责任人用逗号分隔，表格每人对一个标签
  - 导入时自动把 `/ ， 、 ; ； | +` 等分隔符归一
  - 检索「李鑫」能命中「黄亮,李鑫」
- **责任人自动注册**：导入台账时自动把责任人挂到对应部门（部门不存在则新建）

### 优化

- **检索字段自动派生**：从表格 `columns` 生成，页面新增列时检索条件自动多一项
- **导出列自动派生**：全部 8 类导出（资产 / 处置 / 服务 / 合规检测 / 合规规则 / 扫描任务 / 组织结构 / 页面快照）改为跟随表格列，不再漏列
- **导出遵循当前筛选**：导出内容 = 列表当前筛选结果（支持 SPL 条件），不再是全量
- **首页「待确认资产」改按 IP 计**（从 2314 服务条数改为 703 个 IP），与资产处置页口径一致
- **资产台账导入**：单格多 IP 自动拆分；部门取「使用部门/公司」列（此前误取「隶属公司」）

### 修复

- 资产总览上半部分（检索区）不渲染 —— `splFields` 等变量未定义导致组件被 Vue 丢弃
- 检索条件批量失效：
  - 「是否有WEB」是计算列，SQL 无此字段 → 改走 `EXISTS` 子查询
  - 应用服务的「部门 / 负责人」在 assets 表 → 改走 `EXISTS` 关联
  - 应用服务「响应长度 / 指纹 / 最近发现」未映射 → 补齐
  - 资产处置页仅映射 5 个字段 → 改为通用匹配器覆盖全列
- 导出缺列（后端 `exportLabels` / `fetchExportRows` 字段不全）
- 合规检测页白屏（`computed` 未导入）
- 服务页白屏（同因，`computed` 未导入）

---

## [0.0.5] - 2026-10-28

基线版本（资产测绘平台首个可用版本）。

- 资产总览 / 资产处置 / 应用服务 / 合规检测 / 页面快照 / 扫描任务 / 节点 / IP 规划 / 组织结构 / 故障日志 / 通知设置 / 系统配置
- 台账 Excel 导入、资产标注归属、批量编辑、导出
