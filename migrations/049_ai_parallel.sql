-- v0.0.56：识别并发数改为可配置（默认 3）。
--
-- 之前并发=核数/2（上限 3），换机器就变，用户无法按"网关能扛多少"调整。
-- 这个值只影响识别 worker 的并行度，不影响扫描。
ALTER TABLE ai_settings ADD COLUMN IF NOT EXISTS parallel int DEFAULT 3;
UPDATE ai_settings SET parallel = 3 WHERE parallel IS NULL;
