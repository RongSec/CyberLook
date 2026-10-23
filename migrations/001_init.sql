-- 001_init.sql — 初始建表
-- 资产主表（台账 + 扫描发现，IP 为关联键）
CREATE TABLE IF NOT EXISTS assets (
  id          BIGSERIAL PRIMARY KEY,
  ip          INET UNIQUE NOT NULL,
  system_name TEXT,
  owner       TEXT,
  department  TEXT,
  source      TEXT NOT NULL DEFAULT 'scan',
  status      TEXT NOT NULL DEFAULT 'pending',
  created_at  TIMESTAMPTZ DEFAULT now(),
  updated_at  TIMESTAMPTZ DEFAULT now()
);

-- 扫描任务
CREATE TABLE IF NOT EXISTS scan_tasks (
  id          BIGSERIAL PRIMARY KEY,
  name        TEXT NOT NULL,
  ip_ranges   TEXT NOT NULL,
  port_mode   TEXT DEFAULT 'top100',
  ports       TEXT,
  cron        TEXT,
  status      TEXT DEFAULT 'pending',
  node_id     BIGINT,
  template_id BIGINT,
  modules     TEXT DEFAULT 'port',
  finger      TEXT DEFAULT 'tiny',
  thread      INT DEFAULT 600,
  timeout     INT DEFAULT 3,
  no_ping     BOOLEAN DEFAULT false,
  no_smart    BOOLEAN DEFAULT false,
  extra_args  TEXT,
  created_at  TIMESTAMPTZ DEFAULT now(),
  started_at  TIMESTAMPTZ,
  finished_at TIMESTAMPTZ
);

-- 扫描快照（态势核心）
CREATE TABLE IF NOT EXISTS scan_snapshots (
  id          BIGSERIAL PRIMARY KEY,
  task_id     BIGINT NOT NULL REFERENCES scan_tasks(id) ON DELETE CASCADE,
  ip          INET NOT NULL,
  is_alive    BOOLEAN NOT NULL,
  open_ports  JSONB,
  scanned_at  TIMESTAMPTZ DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_snapshots_ip_time ON scan_snapshots(ip, scanned_at DESC);
CREATE INDEX IF NOT EXISTS idx_snapshots_task ON scan_snapshots(task_id);

-- 服务（应用服务，跨快照聚合）
CREATE TABLE IF NOT EXISTS services (
  id          BIGSERIAL PRIMARY KEY,
  asset_id    BIGINT REFERENCES assets(id) ON DELETE SET NULL,
  ip          INET NOT NULL,
  port        INT NOT NULL,
  protocol    TEXT,
  service     TEXT,
  banner      TEXT,
  fingerprint JSONB,
  first_seen  TIMESTAMPTZ,
  last_seen   TIMESTAMPTZ,
  UNIQUE(ip, port)
);

-- 合规规则（敏感端口清单）
CREATE TABLE IF NOT EXISTS compliance_rules (
  id       BIGSERIAL PRIMARY KEY,
  name     TEXT NOT NULL,
  port     INT NOT NULL,
  protocol TEXT DEFAULT 'tcp',
  enabled  BOOLEAN DEFAULT true,
  UNIQUE (name, port, protocol)
);

-- 预置敏感端口
INSERT INTO compliance_rules (name, port) VALUES
  ('SSH 直连', 22),
  ('MySQL 直连', 3306),
  ('Redis 直连', 6379),
  ('PostgreSQL 直连', 5432),
  ('SQL Server 直连', 1433),
  ('RDP 直连', 3389),
  ('MongoDB 直连', 27017),
  ('Elasticsearch 直连', 9200),
  ('Memcached 直连', 11211),
  ('ZooKeeper 直连', 2181),
  ('Kafka 直连', 9092)
ON CONFLICT DO NOTHING;

-- 堡垒机白名单
CREATE TABLE IF NOT EXISTS bastion_whitelist (
  id   BIGSERIAL PRIMARY KEY,
  ip   INET NOT NULL,
  note TEXT
);

-- 合规结果
CREATE TABLE IF NOT EXISTS compliance_results (
  id          BIGSERIAL PRIMARY KEY,
  asset_id    BIGINT REFERENCES assets(id) ON DELETE CASCADE,
  ip          INET NOT NULL,
  port        INT NOT NULL,
  rule_id     BIGINT REFERENCES compliance_rules(id) ON DELETE CASCADE,
  status      TEXT DEFAULT 'violation',
  detected_at TIMESTAMPTZ,
  last_seen   TIMESTAMPTZ,
  fixed_at    TIMESTAMPTZ
);

-- 页面快照
CREATE TABLE IF NOT EXISTS page_snapshots (
  id          BIGSERIAL PRIMARY KEY,
  asset_id    BIGINT REFERENCES assets(id) ON DELETE CASCADE,
  url         TEXT NOT NULL,
  title       TEXT,
  image_path  TEXT,
  captured_at TIMESTAMPTZ DEFAULT now()
);

-- 节点（分布式）
CREATE TABLE IF NOT EXISTS nodes (
  id             BIGSERIAL PRIMARY KEY,
  name           TEXT UNIQUE NOT NULL,
  ip             INET,
  token          TEXT NOT NULL,
  status         TEXT DEFAULT 'offline',
  last_heartbeat TIMESTAMPTZ,
  created_at     TIMESTAMPTZ DEFAULT now()
);

-- 用户
CREATE TABLE IF NOT EXISTS users (
  id            BIGSERIAL PRIMARY KEY,
  username      TEXT UNIQUE NOT NULL,
  password_hash TEXT NOT NULL,
  role          TEXT DEFAULT 'admin',
  created_at    TIMESTAMPTZ DEFAULT now()
);

-- 网段（机房 → 网段映射）
CREATE TABLE IF NOT EXISTS ip_segments (
  id          BIGSERIAL PRIMARY KEY,
  name        TEXT NOT NULL,
  cidr        TEXT NOT NULL,
  description TEXT,
  created_at  TIMESTAMPTZ DEFAULT now()
);

-- 免扫描清单（IP 范围 + 原因）
CREATE TABLE IF NOT EXISTS exclude_ranges (
  id         BIGSERIAL PRIMARY KEY,
  ip_range   TEXT NOT NULL,
  reason     TEXT,
  created_at TIMESTAMPTZ DEFAULT now()
);

-- 扫描模板（端口 + 工具参数，拼接成命令）
CREATE TABLE IF NOT EXISTS scan_templates (
  id         BIGSERIAL PRIMARY KEY,
  name       TEXT NOT NULL,
  modules    TEXT DEFAULT 'port',
  port_mode  TEXT DEFAULT 'top100',
  ports      TEXT,
  thread     INT DEFAULT 600,
  timeout    INT DEFAULT 3,
  no_ping    BOOLEAN DEFAULT false,
  no_smart   BOOLEAN DEFAULT false,
  finger     TEXT DEFAULT 'tiny',
  extra_args TEXT,
  created_at TIMESTAMPTZ DEFAULT now()
);

-- 组织结构（部门，支持层级）
CREATE TABLE IF NOT EXISTS departments (
  id        BIGSERIAL PRIMARY KEY,
  name      TEXT NOT NULL,
  parent_id BIGINT,
  sort      INT DEFAULT 0
);
