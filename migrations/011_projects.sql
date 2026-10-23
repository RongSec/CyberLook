-- 节点的 config.db 里的 project 列表（agent 心跳上报，JSON 数组）
ALTER TABLE nodes ADD COLUMN IF NOT EXISTS projects TEXT DEFAULT '[]';
