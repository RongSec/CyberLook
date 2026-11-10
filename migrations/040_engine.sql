-- 040: 扫描任务支持引擎选择（自研引擎 / TscanPlus）
-- builtin = 自研端口扫描 + 全量指纹（默认）；tscan = TscanPlus（回退兼容）
ALTER TABLE scan_tasks ADD COLUMN IF NOT EXISTS engine VARCHAR(16) DEFAULT 'builtin';
UPDATE scan_tasks SET engine = 'builtin' WHERE engine IS NULL OR engine = '';
