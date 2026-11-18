-- 043_llm_conversation_kind.sql
--
-- 给 AI 会话加"类型"，用于左侧树状分组：
--   chat       人工对话（用户在助手页直接问的）
--   svc_ident  服务识别（给引擎证据，让模型判定这是什么系统）
--   svc_review 服务准确性判别（判引擎给的产品名对不对）
--   render     渲染复核（判不了 → 渲染页面后再判）
--   task       任务会话（扫描任务自动建的汇总会话，历史数据兼容）
--
-- 一个扫描任务下可能同时存在多种类型，前端按 (task_id, kind) 分两级展示。

ALTER TABLE llm_conversations ADD COLUMN IF NOT EXISTS kind varchar(32) NOT NULL DEFAULT 'chat';
CREATE INDEX IF NOT EXISTS idx_llm_conversations_kind ON llm_conversations (kind);
CREATE INDEX IF NOT EXISTS idx_llm_conversations_task ON llm_conversations (task_id);

-- 历史数据：挂在任务上的会话归为 task
UPDATE llm_conversations SET kind = 'task' WHERE task_id > 0 AND kind = 'chat';
