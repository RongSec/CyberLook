-- 台账重复导入冲突：已确认资产不被覆盖，先落到这里等人工决定
CREATE TABLE IF NOT EXISTS import_conflicts (
    id              BIGSERIAL PRIMARY KEY,
    asset_id        BIGINT NOT NULL,
    ip              VARCHAR(64) NOT NULL,
    old_system_name TEXT,
    old_owner       TEXT,
    old_department  TEXT,
    new_system_name TEXT,
    new_owner       TEXT,
    new_department  TEXT,
    created_at      TIMESTAMPTZ DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_import_conflicts_ip ON import_conflicts(ip);
