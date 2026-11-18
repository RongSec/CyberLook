# 服务识别工作流

本文描述「一个端口被发现之后，系统怎么判断它是什么」。当前版本 0.0.41，识别判定全部交给模型，
引擎只负责抓证据。

---

## 一、总览

```
扫描节点                                    平台（服务端）
────────                                    ──────────────
TCP 存活/端口开放
    │
    ├─► 引擎识别（fingerhub 15,388 条规则）
    │     协议探针 → Windows 握手 → HTTP 抓页 → 规则匹配 → SPA 取证 → 主动探针
    │     产出：product / title / banner / 证据 / **needAi 标记**
    │
    └──────────── 上报 ────────────────────► 入库（services）
                                                  │
                                        needAi == true ?
                                                  │
                                    ┌─────────────┴─────────────┐
                                    │ 否                        │ 是
                                    ▼                           ▼
                              直接落台账                    AI 识别链路（下方）
```

关键点：**要不要 AI 判，由引擎按「证据来源」标记，不按内容正则。**
产品名长得怪从来不是拒绝理由（`SAP Message Server,release 722 (SP8)` 是合法名字）。

---

## 二、引擎侧：抓证据，不下结论

对每个开放端口按顺序尝试，拿到就停：

| 阶段 | 做什么 | 产出 |
|---|---|---|
| ① 协议探针 | 被动 banner / 握手：SSH、SMB、MySQL、Redis、RDP、LDAP… | protocol、banner、version |
| ② Windows 握手 | 445/139/3389/135 取真实 OS 与主机名 | OS |
| ③ HTTP 抓页 | GET 首页 + favicon，容忍 3xx/4xx/自签证书；**GB2312/GBK 自动转 UTF-8** | title、status、body |
| ④ 规则匹配 | 3,450 条 web 规则（word/regex/favicon），Aho-Corasick 一次过 | 候选产品名（最多并列 3 个） |
| ⑤ SPA 取证 | 空壳页面：按 `<base href>` 抓同源 JS，抽只读配置接口取 `platformName` | 产品名（source=`spa-rule`） |
| ⑥ 主动探针 | 按端口发对应载荷，每端口 ≤3 组 | 产品名（source=`service-probe`） |
| ⑦ 端口兜底 | 以上全空但有协议线索 | 协议名（source=`port-hint`） |

**`needAi` 的判定只看来源**：

```
product 为空            → true
source = port-hint      → true   只知道协议名（"HTTP"/"SSH"）
source = tls-cert       → true   证书里的名字（常是厂商/单位名）
source = http-version   → true   只知道 Web 服务器版本（nginx/1.24）
source = http-title     → true   页面标题（可能只是"Login"）
source = web-rule / spa-rule / probe / win-probe → false   有具体特征支撑
```

---

## 三、平台侧：AI 识别链路

对 `needAi = true` 的端口异步入队（3 个 worker，每任务上限 2000 条，`AI_IDENTIFY_MAX_PER_TASK` 可调）。

```
① 抓完整请求/响应包（最多 4 跳，含 302）
      │  记录：请求行、请求头、响应行、响应头、正文摘要
      ▼
② AI 判定 → { title, product, category, confidence, sufficient, reason }
      │
      ├─ confidence ≥ 60 ──────────────────────► 写回台账（title_source=ai）
      │
      ├─ sufficient = true（信息够，但给不出确切名字）──► 收手，不渲染
      │
      └─ sufficient = false（证据不足）
                    ▼
③ 浏览器真渲染（chromium --headless，8s）
      │  取：渲染后标题、meta 描述、正文摘要
      ▼
④ AI 再判
      │
      └─ confidence ≥ 60 → 写回；否则保留原值，记「未达写回门槛」
```

### 为什么先抓包、不直接渲染

渲染一个页面 5~8 秒；抓一次完整请求/响应（含 302 链）通常 0.5~1 秒。
设备类目标（BMC、审计、防火墙）几乎都能从**响应头**认出来——`Server`、`X-Graylog-Node-Id`、
`WWW-Authenticate`、错误页样式，这些在抓包阶段就齐了。渲染只留给真正的 SPA。

### 写回规则

| 情况 | 动作 |
|---|---|
| `confidence ≥ 60` 且有名字 | 写 `title` / `fingerprint`，`title_source=ai`，旧值存 `prev_title` |
| `confidence < 60` | 不写。台账保留原值，`ai_checked_at` 记时间 |
| `sufficient=true` 但没名字 | 不写，不再渲染（渲染也拿不到更多） |

宁可留着"未知"，也不要写一个低把握的名字——错名字比空名字更难被发现。

---

## 四、AI 判定的输入长什么样

模型看到的是**真实报文**，不是摘要猜测：

```
来源：请求响应包
GET / HTTP/1.1
Host: 198.19.124.23:443
User-Agent: Mozilla/5.0 ...
Accept: text/html,...

HTTP/1.1 200 OK
Server: nginx/1.24.0 (Ubuntu)
X-Graylog-Node-Id: 4b1f...
Content-Type: text/html

<title>Graylog Web Interface</title>
...
```

模型输出：

```json
{"title":"Graylog 日志管理平台","product":"Graylog（Graylog Web Interface）",
 "category":"日志管理/SIEM","confidence":96,"sufficient":true,
 "reason":"标题与X-Graylog-Node-Id头直接标明Graylog"}
```

---

## 五、结果落库与会话归档

- 服务表：`title` / `fingerprint` / `title_source`（`ai` 表示模型判过）/ `ai_need` / `ai_checked_at`
- 会话表：每次识别链路写进**该任务的 `svc_ident` 会话**，完整请求响应包放在消息 `meta` 里，
  前端折叠成按钮（点开是四栏报文对比）
- AI 助手左侧按 `任务 → 会话类型` 两级展示：对话 / 服务识别 / 服务准确性判别 / 渲染复核

---

## 六、边界与已知限制

1. **只覆盖 HTTP(S)**。SSH/SMB/数据库这类端口走引擎规则，不进 AI 链路
   （banner 里信息足够，且没有"渲染"这一步可补）。
2. **节点镜像不含 chromium** 时渲染会跳过，链路停在 ②，只记录"渲染不可用"。
3. **每条服务一次模型调用**，成本随 `needAi` 数量线性增长。全库 2 万条服务里
   `needAi` 约占 30%（约 6,000 条），按 3 worker、1.5s/条估算约 50 分钟。
4. **模型会说错**。`confidence ≥ 60` 只是门槛，不是保证。人工复核仍然必要，
   所以每次判定都留了 `reason` 和完整报文。
5. **不跨主机跟随跳转**：`Location` 指向别的 IP 时不跟，避免把无关资产拉进这次判定。
