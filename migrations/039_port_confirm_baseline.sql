-- 端口级确认基线：一个 IP 可能有多个 web 端口，各自标题不同。
-- 原来基线只有 assets.confirmed_title 一个值（IP 级），导致多端口 IP 里除了一个端口外，
-- 其余端口永远对不上基线 → 永久显示"新变更"，按台账确认完立刻又变回去。
ALTER TABLE services ADD COLUMN IF NOT EXISTS confirmed_title text DEFAULT '';

-- 存量回填：只回填「多端口 IP 上已确认的 web 端口」，以当前标题作为基线。
--   * 单端口 IP 保持用 assets.confirmed_title 作基线，不动（避免把真实变更洗掉）
--   * confirmed_title = '' 保证幂等：迁移每次启动都会跑，回填过的不再覆盖
UPDATE services s
SET confirmed_title = COALESCE(s.title, '')
WHERE s.confirmed = true
  AND COALESCE(s.confirmed_title, '') = ''
  AND COALESCE(s.title, '') <> ''
  AND (s.protocol ILIKE 'http%' OR s.service ILIKE '%http%')
  AND EXISTS (
    SELECT 1 FROM services s2
    WHERE s2.ip = s.ip
      AND s2.confirmed = true
      AND s2.port <> s.port
      AND (s2.protocol ILIKE 'http%' OR s2.service ILIKE '%http%')
      AND (COALESCE(s2.status_code,'') <> '' OR COALESCE(s2.title,'') <> '' OR COALESCE(s2.content_length,0) > 0)
  );
