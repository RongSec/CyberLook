-- 1) 节点终端日志最后变化时间（扫描监控用它判断"日志是否还在刷新"）
ALTER TABLE nodes ADD COLUMN IF NOT EXISTS terminal_log_at timestamptz;
-- 2) 分片 AI 诊断结论：'' 未诊断 / done 已完成但没结束 / stalled 真卡死
ALTER TABLE agent_tasks ADD COLUMN IF NOT EXISTS ai_verdict text DEFAULT '';
ALTER TABLE agent_tasks ADD COLUMN IF NOT EXISTS ai_verdict_at timestamptz;
ALTER TABLE agent_tasks ADD COLUMN IF NOT EXISTS ai_verdict_note text DEFAULT '';
-- 3) 通知模板（可编辑，支持 {{变量}}）
ALTER TABLE notify_settings ADD COLUMN IF NOT EXISTS tpl_task_done text DEFAULT '';
ALTER TABLE notify_settings ADD COLUMN IF NOT EXISTS tpl_new_asset text DEFAULT '';
ALTER TABLE notify_settings ADD COLUMN IF NOT EXISTS tpl_violation text DEFAULT '';
ALTER TABLE notify_settings ADD COLUMN IF NOT EXISTS tpl_fault text DEFAULT '';
-- 4) AI 功能开关
CREATE TABLE IF NOT EXISTS ai_settings (
    id             bigint PRIMARY KEY DEFAULT 1,
    service_identify boolean DEFAULT true,
    scan_monitor     boolean DEFAULT true,
    updated_at     timestamptz DEFAULT now()
);
INSERT INTO ai_settings (id) VALUES (1) ON CONFLICT (id) DO NOTHING;
-- 5) 备份设置
CREATE TABLE IF NOT EXISTS backup_settings (
    id         bigint PRIMARY KEY DEFAULT 1,
    enabled    boolean DEFAULT true,
    hour       integer DEFAULT 2,
    keep_days  integer DEFAULT 7,
    updated_at timestamptz DEFAULT now()
);
INSERT INTO backup_settings (id) VALUES (1) ON CONFLICT (id) DO NOTHING;
