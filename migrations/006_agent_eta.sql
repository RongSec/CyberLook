-- 分片预计剩余秒数（由 agent 从 TscanClient 进度条的 [已用:剩余] 解析）
ALTER TABLE agent_tasks ADD COLUMN IF NOT EXISTS eta_seconds INTEGER DEFAULT 0;
