-- 扫描选项：是否先做服务端存活探测（关闭则直接按 IP 规划下发，覆盖全量目标）
ALTER TABLE scan_tasks ADD COLUMN IF NOT EXISTS alive_probe boolean DEFAULT true;
