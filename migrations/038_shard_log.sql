-- 分片级终端日志：每个节点的每次分片任务独立保存回显，不再跨分片累积
ALTER TABLE agent_tasks ADD COLUMN IF NOT EXISTS terminal_log text DEFAULT '';
