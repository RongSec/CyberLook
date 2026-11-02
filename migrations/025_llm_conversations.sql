-- LLM 会话表
CREATE TABLE IF NOT EXISTS llm_conversations (
    id          SERIAL PRIMARY KEY,
    title       VARCHAR(200) DEFAULT '',
    profile_name VARCHAR(100) DEFAULT '',
    message_count INTEGER DEFAULT 0,
    total_tokens  INTEGER DEFAULT 0,
    created_at  TIMESTAMPTZ DEFAULT now(),
    updated_at  TIMESTAMPTZ DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_llm_conv_updated ON llm_conversations(updated_at DESC);

-- LLM 会话消息表
CREATE TABLE IF NOT EXISTS llm_messages (
    id              SERIAL PRIMARY KEY,
    conversation_id INTEGER NOT NULL REFERENCES llm_conversations(id) ON DELETE CASCADE,
    role            VARCHAR(20) NOT NULL,
    content         TEXT DEFAULT '',
    tool_calls      TEXT DEFAULT '',
    tokens          INTEGER DEFAULT 0,
    created_at      TIMESTAMPTZ DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_llm_msg_conv ON llm_messages(conversation_id, id);
