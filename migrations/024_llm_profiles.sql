-- LLM 配置表（多 LLM 支持 + 故障轮询 + 熔断）
CREATE TABLE IF NOT EXISTS llm_profiles (
    id                SERIAL PRIMARY KEY,
    name              VARCHAR(100) NOT NULL,
    provider          VARCHAR(20) DEFAULT 'openai',
    base_url          VARCHAR(500) NOT NULL,
    api_key           VARCHAR(500) DEFAULT '',
    api_key_hint      VARCHAR(50) DEFAULT '',
    model             VARCHAR(100) NOT NULL,
    temperature       DOUBLE PRECISION DEFAULT 0.3,
    max_tokens        INTEGER DEFAULT 4096,
    context_window_k  INTEGER DEFAULT 128,
    priority          INTEGER DEFAULT 100,
    enabled           BOOLEAN NOT NULL DEFAULT true,
    pool_exclude      BOOLEAN NOT NULL DEFAULT false,
    fail_threshold    INTEGER DEFAULT 3,
    cooldown_seconds  INTEGER DEFAULT 60,
    timeout_seconds   INTEGER DEFAULT 120,
    created_at        TIMESTAMPTZ DEFAULT now(),
    updated_at        TIMESTAMPTZ DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_llm_profiles_enabled ON llm_profiles(enabled, priority DESC);

-- LLM 用量记录表
CREATE TABLE IF NOT EXISTS llm_usages (
    id             SERIAL PRIMARY KEY,
    profile_id     INTEGER DEFAULT 0,
    profile_name   VARCHAR(100),
    model          VARCHAR(100),
    input_tokens   INTEGER DEFAULT 0,
    output_tokens  INTEGER DEFAULT 0,
    total_tokens   INTEGER DEFAULT 0,
    latency_ms     INTEGER DEFAULT 0,
    status         VARCHAR(20),
    error_msg      VARCHAR(1000) DEFAULT '',
    round          INTEGER DEFAULT 0,
    created_at     TIMESTAMPTZ DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_llm_usages_created ON llm_usages(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_llm_usages_profile ON llm_usages(profile_id);
