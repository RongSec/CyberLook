-- 045_llm_conv_dedupe_kind.sql
--
-- 修两件事（2026-11-18）：
--
-- 1) 同一任务下出现两条同名会话。原因：识别 worker 是并发的，第一次跑某个任务时
--    多个 worker 同时"查不到会话 → 各自创建"，于是留下一条空壳 + 一条真数据。
--    这里把重复会话的消息并到 id 最小的那条，删掉其余的，并补上唯一索引，从库层面堵死竞态。
--
-- 2) kind 全被标成 task。043 的回填把"挂在任务上的会话"一律当 task（任务汇总会话），
--    但这些会话的内容其实全是服务识别流水，导致左侧树只剩「任务会话」一种类型。
--    这里按真实来源改写为 svc_ident。

-- ---------- 1) 合并重复会话 ----------
WITH keepers AS (
    SELECT task_id, MIN(id) AS keep_id
    FROM llm_conversations
    WHERE task_id > 0
    GROUP BY task_id
    HAVING COUNT(*) > 1
)
UPDATE llm_messages m
SET conversation_id = k.keep_id
FROM llm_conversations c
JOIN keepers k ON k.task_id = c.task_id
WHERE m.conversation_id = c.id
  AND c.id <> k.keep_id;

WITH keepers AS (
    SELECT task_id, MIN(id) AS keep_id
    FROM llm_conversations
    WHERE task_id > 0
    GROUP BY task_id
    HAVING COUNT(*) > 1
)
DELETE FROM llm_conversations c
USING keepers k
WHERE c.task_id = k.task_id
  AND c.id <> k.keep_id;

-- ---------- 2) 修正类型 ----------
-- 任务下的会话 = 服务识别流水（识别链路里 抓包 / AI 判定 / 渲染复核 都在里面）
UPDATE llm_conversations SET kind = 'svc_ident'
WHERE task_id > 0 AND kind IN ('task', 'chat');

-- 独立对话不会被误标
UPDATE llm_conversations SET kind = 'chat' WHERE task_id = 0 AND kind = 'task';

-- ---------- 3) 以真实消息数重算统计 ----------
-- 旧逻辑是"每写一条记录 message_count + 2"，历史数据不可靠
UPDATE llm_conversations c
SET message_count = (SELECT COUNT(*) FROM llm_messages m WHERE m.conversation_id = c.id),
    total_tokens  = (SELECT COALESCE(SUM(m.tokens), 0) FROM llm_messages m WHERE m.conversation_id = c.id);

-- ---------- 4) 从库层面堵死并发重复创建 ----------
CREATE UNIQUE INDEX IF NOT EXISTS uniq_llm_conv_task_kind
    ON llm_conversations (task_id, kind) WHERE task_id > 0;
