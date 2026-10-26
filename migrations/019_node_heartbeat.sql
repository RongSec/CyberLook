-- 节点心跳超时阈值可配置（节点页面）
ALTER TABLE system_settings ADD COLUMN IF NOT EXISTS node_heartbeat_timeout_sec integer DEFAULT 90;
