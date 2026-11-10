-- 旧响应码/响应长度：待处置里「旧→新」对比用
ALTER TABLE services ADD COLUMN IF NOT EXISTS prev_status_code text DEFAULT '';
ALTER TABLE services ADD COLUMN IF NOT EXISTS prev_content_length integer DEFAULT 0;
