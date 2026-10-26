-- 熔断规则可配置（节点页面调整）
ALTER TABLE system_settings ADD COLUMN IF NOT EXISTS fuse_fail_threshold integer DEFAULT 5;
ALTER TABLE system_settings ADD COLUMN IF NOT EXISTS fuse_duration_min integer DEFAULT 5;
