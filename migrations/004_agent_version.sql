-- agent 控制文件版本与升级标记
ALTER TABLE nodes ADD COLUMN IF NOT EXISTS agent_version TEXT;
ALTER TABLE nodes ADD COLUMN IF NOT EXISTS upgrade_requested BOOLEAN DEFAULT FALSE;
