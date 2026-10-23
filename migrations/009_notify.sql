-- 通知设置（单行配置）
CREATE TABLE IF NOT EXISTS notify_settings (
    id                INTEGER PRIMARY KEY DEFAULT 1,
    wecom_webhook     TEXT DEFAULT '',
    dingtalk_webhook  TEXT DEFAULT '',
    feishu_webhook    TEXT DEFAULT '',
    generic_webhook   TEXT DEFAULT '',
    email_smtp        TEXT DEFAULT '',
    email_port        INTEGER DEFAULT 465,
    email_ssl         BOOLEAN DEFAULT TRUE,
    email_user        TEXT DEFAULT '',
    email_pass        TEXT DEFAULT '',
    email_to          TEXT DEFAULT '',
    on_task_done      BOOLEAN DEFAULT TRUE,
    on_new_asset      BOOLEAN DEFAULT TRUE,
    on_violation      BOOLEAN DEFAULT TRUE,
    updated_at        TIMESTAMPTZ DEFAULT NOW()
);
INSERT INTO notify_settings (id) VALUES (1) ON CONFLICT (id) DO NOTHING;
