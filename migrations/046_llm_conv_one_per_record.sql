-- 046_llm_conv_one_per_record.sql
--
-- v0.0.47 口径：一次问答 = 一条会话，一次识别 = 一条会话。
-- 不再有"一个任务一条大会话"——历史库里已经攒出 842/1772/2000 条的巨型会话，
-- 这里按原始写入顺序「两两一问一答」拆成独立会话。
--
-- 同时去掉 045 建的 (task_id, kind) 唯一索引：现在同一任务同一类型必然有多条。
--
-- 注意：plpgsql 里 RECORD 变量名不要和 SQL 别名重名（`m` 撞上 `FROM llm_messages m`
-- 会报 "could refer to either a PL/pgSQL variable or a table column"，整个 DO 块回滚）。

DROP INDEX IF EXISTS uniq_llm_conv_task_kind;
CREATE INDEX IF NOT EXISTS idx_llm_conv_task_kind_id ON llm_conversations (task_id, kind, id DESC);

DO $$
DECLARE
    r      RECORD;
    rec    RECORD;
    mids   bigint[];
    i      int;
    new_id bigint;
    tgt    text;
BEGIN
    -- 只拆"一条会话里塞了多轮记录"的历史数据；已经是两两一句的保持不动
    FOR r IN
        SELECT id, task_id, kind, title
        FROM llm_conversations
        WHERE task_id > 0 AND message_count > 2
        ORDER BY id
    LOOP
        SELECT array_agg(id ORDER BY id) INTO mids
        FROM llm_messages WHERE conversation_id = r.id;

        IF mids IS NULL THEN
            DELETE FROM llm_conversations WHERE id = r.id;
            CONTINUE;
        END IF;

        new_id := NULL;
        FOR i IN 1..array_length(mids, 1) LOOP
            SELECT * INTO rec FROM llm_messages WHERE id = mids[i];

            IF i % 2 = 1 THEN
                -- 记录正文首行形如「目标：198.18.12.151:80（HTTP）」，用它当标题更好认
                tgt := substring(rec.content from '目标：([^（[:space:]]+)');
                INSERT INTO llm_conversations (title, kind, task_id, message_count, total_tokens, created_at, updated_at)
                VALUES (COALESCE(NULLIF(btrim(tgt), ''), r.title), r.kind, r.task_id, 0, 0, rec.created_at, rec.created_at)
                RETURNING id INTO new_id;
            END IF;

            INSERT INTO llm_messages (conversation_id, role, content, tool_calls, meta, tokens, created_at)
            VALUES (new_id, rec.role, rec.content, rec.tool_calls, rec.meta, rec.tokens, rec.created_at);
        END LOOP;

        DELETE FROM llm_messages WHERE id = ANY(mids);
        DELETE FROM llm_conversations WHERE id = r.id;
    END LOOP;

    -- 以真实消息数重算（别名别和上面的 RECORD 撞名）
    UPDATE llm_conversations AS cv
    SET message_count = (SELECT COUNT(*) FROM llm_messages AS t WHERE t.conversation_id = cv.id);
END $$;
