-- 故障记录保留天数（默认 30 天，可调）
ALTER TABLE system_settings ADD COLUMN IF NOT EXISTS faults_retention_days integer DEFAULT 30;
