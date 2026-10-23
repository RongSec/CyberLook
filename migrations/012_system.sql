-- 系统设置（单行）：GitHub 仓库 + 代理（仅用于获取更新）
CREATE TABLE IF NOT EXISTS system_settings (
    id          INTEGER PRIMARY KEY DEFAULT 1,
    github_repo TEXT DEFAULT '',
    github_token TEXT DEFAULT '',
    proxy       TEXT DEFAULT '',
    updated_at  TIMESTAMPTZ DEFAULT NOW()
);
INSERT INTO system_settings (id) VALUES (1) ON CONFLICT (id) DO NOTHING;
