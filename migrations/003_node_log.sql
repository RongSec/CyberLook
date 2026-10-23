-- 节点终端日志缓存（agent 上报的真实日志）
ALTER TABLE nodes ADD COLUMN IF NOT EXISTS terminal_log TEXT;
