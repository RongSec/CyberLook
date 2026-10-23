-- 页面内容指纹（SimHash 64 位）
ALTER TABLE services ADD COLUMN IF NOT EXISTS page_simhash BIGINT DEFAULT 0;
-- 确认资产时的页面指纹基线 + 内容变更标记
ALTER TABLE assets ADD COLUMN IF NOT EXISTS confirmed_simhash BIGINT DEFAULT 0;
ALTER TABLE assets ADD COLUMN IF NOT EXISTS content_changed BOOLEAN DEFAULT FALSE;
