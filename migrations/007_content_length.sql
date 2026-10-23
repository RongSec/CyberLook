-- 响应体长度（来自 TscanClient ipscan/urlscan 的 Lenbody）
ALTER TABLE services ADD COLUMN IF NOT EXISTS content_length INTEGER DEFAULT 0;
