-- 流水线调度：记录已探测到第几个原始 IP，服务重启后可续探
ALTER TABLE scan_tasks ADD COLUMN IF NOT EXISTS probe_offset integer DEFAULT 0;
