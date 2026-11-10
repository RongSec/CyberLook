-- 检测结果的自定义标签：非空时覆盖显示的规则名（只影响展示，不影响检测匹配逻辑）
ALTER TABLE compliance_results ADD COLUMN IF NOT EXISTS label TEXT DEFAULT '';
