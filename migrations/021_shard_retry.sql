-- 分片重试计数（故障转移用，默认重试 3 次）
ALTER TABLE agent_tasks ADD COLUMN IF NOT EXISTS retry_count integer DEFAULT 0;
