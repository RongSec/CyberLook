-- 记录目标展开后的 IP 总数，用于判断流水线探测是否全部完成
ALTER TABLE scan_tasks ADD COLUMN IF NOT EXISTS total_ips integer DEFAULT 0;
