-- 任务概览：记录应用/违规由哪个任务首次发现、最近一次扫到
ALTER TABLE services ADD COLUMN IF NOT EXISTS task_id integer DEFAULT 0;
ALTER TABLE services ADD COLUMN IF NOT EXISTS last_task_id integer DEFAULT 0;
ALTER TABLE compliance_results ADD COLUMN IF NOT EXISTS task_id integer DEFAULT 0;
ALTER TABLE compliance_results ADD COLUMN IF NOT EXISTS last_task_id integer DEFAULT 0;

-- 任务维度的存活统计（探测阶段累加）
ALTER TABLE scan_tasks ADD COLUMN IF NOT EXISTS alive_count integer DEFAULT 0;

-- 定时任务每次执行的快照
CREATE TABLE IF NOT EXISTS task_runs (
    id             bigserial PRIMARY KEY,
    task_id        integer NOT NULL,
    status         text DEFAULT 'running',
    started_at     timestamptz DEFAULT now(),
    finished_at    timestamptz,
    duration_sec   integer DEFAULT 0,
    total_ips      integer DEFAULT 0,
    alive_ips      integer DEFAULT 0,
    apps_found     integer DEFAULT 0,
    apps_new       integer DEFAULT 0,
    violations     integer DEFAULT 0,
    violations_new integer DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_task_runs_task ON task_runs (task_id, started_at DESC);
