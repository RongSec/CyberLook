-- 合规检测的现场凭证：一键检测时实际连接该端口抓到的响应（banner），
-- 用于证明"点击那一刻端口确实还开着"，也可作为报告证据。
ALTER TABLE compliance_results ADD COLUMN IF NOT EXISTS evidence TEXT DEFAULT '';
