-- 批量下发：每批 IP 数（默认 100）
ALTER TABLE scan_tasks ADD COLUMN IF NOT EXISTS batch_size INTEGER DEFAULT 100;
-- 分片记录是第几批（从 1 开始，便于日志显示）
ALTER TABLE agent_tasks ADD COLUMN IF NOT EXISTS batch_no INTEGER DEFAULT 0;
ALTER TABLE agent_tasks ADD COLUMN IF NOT EXISTS batch_total INTEGER DEFAULT 0;
