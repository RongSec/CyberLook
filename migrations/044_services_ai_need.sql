-- 044_services_ai_need.sql
--
-- 引擎按"证据来源"标记哪些识别结果需要 AI 复核（产品名缺失 / 只来自协议名、证书、HTTP 头、页面标题）。
-- 平台据此决定是否把这条服务送进 AI 识别链路——不用正则去猜名字真假。
ALTER TABLE services ADD COLUMN IF NOT EXISTS ai_need boolean NOT NULL DEFAULT false;
ALTER TABLE services ADD COLUMN IF NOT EXISTS ai_checked_at timestamp;
CREATE INDEX IF NOT EXISTS idx_services_ai_need ON services (ai_need) WHERE ai_need;
