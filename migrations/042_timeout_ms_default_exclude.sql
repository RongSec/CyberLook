-- 042: 超时统一按毫秒存 + 免扫描清单默认排除网段网关
--
-- 一、超时单位：scan_tasks.timeout / scan_templates.timeout 一律毫秒。
--     历史数据是"秒"（1~60），按 <100 判据一次性换算成毫秒。
--     判据与代码里的兼容逻辑一致（internal/scanner/builtin.go 的 <100 → ×1000），
--     所以换算只做一次也不会重复放大。
UPDATE scan_tasks SET timeout = timeout * 1000
 WHERE timeout > 0 AND timeout < 100;
UPDATE scan_templates SET timeout = timeout * 1000
 WHERE timeout > 0 AND timeout < 100;

ALTER TABLE scan_tasks ALTER COLUMN timeout SET DEFAULT 1500;
ALTER TABLE scan_templates ALTER COLUMN timeout SET DEFAULT 1500;

-- 二、免扫描清单缺省项：每个网段的 .1 基本都是网关/交换机/防火墙管理口，
--     扫它既没有资产价值，还会在存活探测阶段被当成"存活主机"入队，
--     白白消耗一整个 65535 端口的扫描单元（实测 198.18.2.1 就是被这样带进队列的）。
--     通配写法 *.*.*.1 由 internal/target.Excluder 支持；用户可在「网络管理」里删掉。
INSERT INTO exclude_ranges (ip_range, reason, created_at)
SELECT '*.*.*.1', '默认：网段网关/交换机管理口（不缺省扫描）', now()
 WHERE NOT EXISTS (SELECT 1 FROM exclude_ranges WHERE ip_range = '*.*.*.1');
