-- 任务日志持久化：之前 tasklog 只放内存，容器一重启日志全丢（实测重启后任务日志空白）
CREATE TABLE IF NOT EXISTS task_logs (
    id         bigserial PRIMARY KEY,
    task_id    bigint NOT NULL,
    line       text NOT NULL,
    created_at timestamptz DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_task_logs_task ON task_logs(task_id, id);
