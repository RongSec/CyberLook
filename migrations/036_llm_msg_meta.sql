-- AI 会话消息的结构化附加数据（如 http_trace 的请求/响应包，前端折叠成按钮 + 四栏弹窗）
ALTER TABLE llm_messages ADD COLUMN IF NOT EXISTS meta text DEFAULT '';
