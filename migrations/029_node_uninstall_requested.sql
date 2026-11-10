-- 远程卸载标记：控制端删除节点时置 true，agent 心跳取到后自清理
ALTER TABLE nodes ADD COLUMN IF NOT EXISTS uninstall_requested boolean DEFAULT false;
