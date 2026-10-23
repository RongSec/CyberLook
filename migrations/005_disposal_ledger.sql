-- 台账确认的标题（用于判断后续扫描是否变更）
ALTER TABLE assets ADD COLUMN IF NOT EXISTS confirmed_title TEXT;
-- 服务是否已按台账确认
ALTER TABLE services ADD COLUMN IF NOT EXISTS confirmed BOOLEAN DEFAULT FALSE;
