-- 041: IP 租约队列（v0.0.27 下发架构升级：分片制 → 单 IP 租约队列）
-- 每个任务展开后逐 IP 建单元；节点按心跳节奏原子领取（work-stealing），
-- 租约 60s、随心跳续租；过期由 reaper 打回 pending（attempts≥3 判 failed_unit）。
-- BatchSize 退役（保留列兼容旧数据）。
-- USE_IP_QUEUE=false 时新任务回退旧分片制（一个版本周期的过渡开关，默认开）。

CREATE TABLE IF NOT EXISTS task_units (
	id            BIGSERIAL PRIMARY KEY,
	task_id       BIGINT NOT NULL,
	ip            VARCHAR(64) NOT NULL,
	status        VARCHAR(16) NOT NULL DEFAULT 'pending', -- pending|leased|done|failed_unit
	node_id       BIGINT NOT NULL DEFAULT 0,              -- 0=未领取/本机
	session_id    BIGINT NOT NULL DEFAULT 0,              -- 领取时的执行槽位（agent_tasks.id，队列模式下是任务队列会话分片）
	lease_expire  TIMESTAMP NULL,                         -- 租约到期时间（now()+60s，心跳续租）
	attempts      INT NOT NULL DEFAULT 0,                 -- 领取次数（≥3 次租约过期判 failed_unit）
	probes_done   BIGINT NOT NULL DEFAULT 0,              -- 该单元已上报探针数（租约内实时值）
	probes_total  BIGINT NOT NULL DEFAULT 0,              -- 该单元总探针数（1 IP × 端口数，创建即真值）
	pps           DOUBLE PRECISION NOT NULL DEFAULT 0,    -- 最近一次心跳的探针速率（ETA 用）
	open_found    BIGINT NOT NULL DEFAULT 0,              -- 已发现开放端口数（进度展示）
	identified    BIGINT NOT NULL DEFAULT 0,              -- 已完成识别数
	updated_at    TIMESTAMP,
	created_at    TIMESTAMP
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_task_units_task_ip ON task_units (task_id, ip);
CREATE INDEX IF NOT EXISTS idx_task_units_claim ON task_units (task_id, status, id);
CREATE INDEX IF NOT EXISTS idx_task_units_lease ON task_units (status, lease_expire);

-- 任务表：队列模式固化列（创建时按 USE_IP_QUEUE 开关定，重启/翻开关不影响在途任务）
ALTER TABLE scan_tasks ADD COLUMN IF NOT EXISTS queue_mode BOOLEAN NOT NULL DEFAULT FALSE;
-- 失败/取消终态摘要（取消=failed+error"已取消（部分结果已入库）"；failed_unit 汇总等）
ALTER TABLE scan_tasks ADD COLUMN IF NOT EXISTS error VARCHAR(512) NOT NULL DEFAULT '';
