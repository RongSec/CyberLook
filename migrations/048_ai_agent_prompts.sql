-- 048_ai_agent_prompts.sql
--
-- AI Agent 提示词可编辑：两个角色各一列。
--   judge_prompt    ── 研判（页面是否异常、要不要渲染）
--   identify_prompt ── 识别（给出业务系统名）
-- 空值表示使用内置默认（见 internal/service/aiprompt.go）。

ALTER TABLE ai_settings ADD COLUMN IF NOT EXISTS judge_prompt text;
ALTER TABLE ai_settings ADD COLUMN IF NOT EXISTS identify_prompt text;
