-- AI 识别会话归属：扫描任务触发的 AI 调用归档到同一个会话（task_id > 0 即"任务会话"）
ALTER TABLE llm_conversations ADD COLUMN IF NOT EXISTS task_id bigint DEFAULT 0;
CREATE INDEX IF NOT EXISTS idx_llm_conv_task ON llm_conversations(task_id);
