-- 047_activity_multi_owner.sql
--
-- 1) 允许多归属：同一 IP 可以有多条归属记录（不同系统/负责人），去掉 ip 唯一约束
-- 2) 活跃/睡眠：服务级 activity + 缺席计数按任务结算（不再删除记录）
-- 3) host_states：主机级存活/缺席次数，用于「主机不存活连续 3 次 → 旗下全部服务睡眠」

ALTER TABLE assets DROP CONSTRAINT IF EXISTS assets_ip_key;
CREATE INDEX IF NOT EXISTS idx_assets_ip ON assets (ip);

ALTER TABLE services ADD COLUMN IF NOT EXISTS activity varchar(12) NOT NULL DEFAULT 'active';
ALTER TABLE services ADD COLUMN IF NOT EXISTS last_miss_task bigint NOT NULL DEFAULT 0;
CREATE INDEX IF NOT EXISTS idx_services_activity ON services (activity);

CREATE TABLE IF NOT EXISTS host_states (
    ip           inet PRIMARY KEY,
    alive        boolean     NOT NULL DEFAULT true,
    miss_count   integer     NOT NULL DEFAULT 0,
    last_task_id bigint      NOT NULL DEFAULT 0,
    updated_at   timestamptz NOT NULL DEFAULT now()
);
