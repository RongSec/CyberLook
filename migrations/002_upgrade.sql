-- 002_upgrade.sql — 幂等升级（已有库加列 / 改类型 / 去重）
ALTER TABLE scan_tasks ADD COLUMN IF NOT EXISTS template_id BIGINT;
ALTER TABLE scan_tasks ADD COLUMN IF NOT EXISTS modules TEXT DEFAULT 'port';
ALTER TABLE scan_tasks ADD COLUMN IF NOT EXISTS finger TEXT DEFAULT 'tiny';
ALTER TABLE scan_tasks ADD COLUMN IF NOT EXISTS thread INT DEFAULT 600;
ALTER TABLE scan_tasks ADD COLUMN IF NOT EXISTS timeout INT DEFAULT 3;
ALTER TABLE scan_tasks ADD COLUMN IF NOT EXISTS no_ping BOOLEAN DEFAULT false;
ALTER TABLE scan_tasks ADD COLUMN IF NOT EXISTS no_smart BOOLEAN DEFAULT false;
ALTER TABLE scan_tasks ADD COLUMN IF NOT EXISTS extra_args TEXT;
ALTER TABLE scan_tasks ADD COLUMN IF NOT EXISTS report_path TEXT;
ALTER TABLE scan_tasks ADD COLUMN IF NOT EXISTS node_ids TEXT;
ALTER TABLE nodes ADD COLUMN IF NOT EXISTS timeout INT DEFAULT 3000;
ALTER TABLE nodes ADD COLUMN IF NOT EXISTS concurrency INT DEFAULT 1000;

ALTER TABLE scan_templates ADD COLUMN IF NOT EXISTS modules TEXT DEFAULT 'port';
ALTER TABLE scan_templates ADD COLUMN IF NOT EXISTS finger TEXT DEFAULT 'tiny';

-- services 加 title 列（web 标题）
ALTER TABLE services ADD COLUMN IF NOT EXISTS title TEXT;
-- services 加 status_code（HTTP 响应码）
ALTER TABLE services ADD COLUMN IF NOT EXISTS status_code TEXT;
-- departments 加预设负责人
ALTER TABLE departments ADD COLUMN IF NOT EXISTS owners TEXT;
-- services 增加连续缺席计数（连续 3 次未发现才剔除）
ALTER TABLE services ADD COLUMN IF NOT EXISTS miss_count INT DEFAULT 0;

-- fingerprint 若为 jsonb 改为 TEXT（存指纹字符串）
DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_name = 'services' AND column_name = 'fingerprint' AND data_type = 'jsonb'
  ) THEN
    ALTER TABLE services ALTER COLUMN fingerprint TYPE TEXT USING fingerprint::text;
  END IF;
END $$;

-- ip_ranges 若还是数组类型，改为 TEXT
DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_name = 'scan_tasks' AND column_name = 'ip_ranges' AND data_type = 'ARRAY'
  ) THEN
    ALTER TABLE scan_tasks ALTER COLUMN ip_ranges TYPE TEXT USING array_to_string(ip_ranges, ',');
  END IF;
END $$;

-- 合规规则去重（保留每组最小 id），并加唯一约束防止重复插入
DELETE FROM compliance_rules a USING compliance_rules b
WHERE a.id > b.id AND a.name = b.name AND a.port = b.port AND a.protocol = b.protocol;

DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint WHERE conname = 'compliance_rules_name_port_protocol_key'
  ) THEN
    ALTER TABLE compliance_rules ADD CONSTRAINT compliance_rules_name_port_protocol_key UNIQUE (name, port, protocol);
  END IF;
END $$;

-- 子任务表（分布式分片）
CREATE TABLE IF NOT EXISTS agent_tasks (
    id BIGSERIAL PRIMARY KEY,
    task_id BIGINT NOT NULL,
    node_id BIGINT NOT NULL,
    node_name TEXT,
    ip_ranges TEXT,
    status TEXT DEFAULT 'pending',
    total INT DEFAULT 0,
    scanned INT DEFAULT 0,
    progress INT DEFAULT 0,
    message TEXT,
    created_at TIMESTAMPTZ DEFAULT now(),
    started_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_agent_tasks_task ON agent_tasks(task_id);
CREATE INDEX IF NOT EXISTS idx_agent_tasks_node ON agent_tasks(node_id);
