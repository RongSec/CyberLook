-- 故障记录：服务端中断 / 节点故障 / 分片失败 / 熔断
CREATE TABLE IF NOT EXISTS faults (
    id          bigserial PRIMARY KEY,
    kind        text NOT NULL,               -- server_restart / node_offline / node_fused / shard_failed
    node_id     integer DEFAULT 0,
    node_name   text DEFAULT '',
    task_id     integer DEFAULT 0,
    message     text DEFAULT '',
    created_at  timestamptz DEFAULT now(),
    resolved_at timestamptz
);
CREATE INDEX IF NOT EXISTS idx_faults_kind ON faults (kind, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_faults_node ON faults (node_id, created_at DESC);

-- 通知开关：故障告警可单独控制
ALTER TABLE notify_settings ADD COLUMN IF NOT EXISTS notify_faults boolean DEFAULT true;
