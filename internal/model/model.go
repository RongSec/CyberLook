package model

import "time"

// Asset 资产主表（台账 + 扫描发现，IP 为关联键）
type Asset struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	IP         string    `gorm:"column:ip;uniqueIndex" json:"ip"`
	SystemName string    `gorm:"column:system_name" json:"systemName"`
	Owner      string    `gorm:"column:owner" json:"owner"`
	Department string    `gorm:"column:department" json:"department"`
	Source     string    `gorm:"column:source;default:scan" json:"source"`
	Status         string    `gorm:"column:status;default:pending" json:"status"`
	ConfirmedTitle string    `gorm:"column:confirmed_title" json:"confirmedTitle"`
	// ConfirmedSimhash 确认时的页面内容指纹（SimHash 64 位），用于判断后续是否改版
	ConfirmedSimhash uint64 `gorm:"column:confirmed_simhash;default:0" json:"-"`
	// ContentChanged 后续扫描发现页面内容实质变化
	ContentChanged bool `gorm:"column:content_changed;default:false" json:"contentChanged"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

func (Asset) TableName() string { return "assets" }

// ScanTask 扫描任务
type ScanTask struct {
	ID         uint       `gorm:"primaryKey" json:"id"`
	Name       string     `gorm:"column:name" json:"name"`
	IPRanges   string     `gorm:"column:ip_ranges" json:"ipRanges"` // 换行/逗号分隔的 IP/CIDR/范围
	PortMode   string     `gorm:"column:port_mode;default:top100" json:"portMode"`
	Ports      string     `gorm:"column:ports" json:"ports"`
	Cron       string     `gorm:"column:cron" json:"cron"`
	Status     string     `gorm:"column:status;default:pending" json:"status"`
	NodeID     *uint      `gorm:"column:node_id" json:"nodeId"`
	TemplateID *uint      `gorm:"column:template_id" json:"templateId"`
	Modules    string     `gorm:"column:modules;default:port" json:"modules"`
	Finger     string     `gorm:"column:finger;default:tiny" json:"finger"`
	Thread     int        `gorm:"column:thread;default:600" json:"thread"`
	Timeout    int        `gorm:"column:timeout;default:3" json:"timeout"`
	NoPing     bool       `gorm:"column:no_ping" json:"noPing"`
	NoSmart    bool       `gorm:"column:no_smart" json:"noSmart"`
	ExtraArgs  string     `gorm:"column:extra_args" json:"extraArgs"`
	ReportPath string     `gorm:"column:report_path" json:"reportPath"`
	BatchSize  int        `gorm:"column:batch_size;default:100" json:"batchSize"` // 每批下发的 IP 数
	NodeIDs    string     `gorm:"column:node_ids" json:"nodeIds"` // ""=本机, "all"=全部节点, "1,2"=指定
	CreatedAt  time.Time  `json:"createdAt"`
	StartedAt  *time.Time `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt"`
}

func (ScanTask) TableName() string { return "scan_tasks" }

// ScanSnapshot 扫描快照（每次任务 × 每个 IP 一条）
type ScanSnapshot struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	TaskID    uint      `gorm:"column:task_id;index" json:"taskId"`
	IP        string    `gorm:"column:ip" json:"ip"`
	IsAlive   bool      `gorm:"column:is_alive" json:"isAlive"`
	OpenPorts string    `gorm:"column:open_ports;type:jsonb" json:"openPorts"` // JSON 数组字符串
	ScannedAt time.Time `gorm:"column:scanned_at" json:"scannedAt"`
}

func (ScanSnapshot) TableName() string { return "scan_snapshots" }

// Service 应用服务（跨快照聚合）
type Service struct {
	ID          uint       `gorm:"primaryKey" json:"id"`
	AssetID     *uint      `gorm:"column:asset_id" json:"assetId"`
	IP          string     `gorm:"column:ip" json:"ip"`
	Port        int        `gorm:"column:port" json:"port"`
	Protocol    string     `gorm:"column:protocol" json:"protocol"`
	Service     string     `gorm:"column:service" json:"service"`
	Banner      string     `gorm:"column:banner" json:"banner"`
	Title       string     `gorm:"column:title" json:"title"`
	Fingerprint string     `gorm:"column:fingerprint" json:"fingerprint"`
	StatusCode  string     `gorm:"column:status_code" json:"statusCode"`
	ContentLength int      `gorm:"column:content_length;default:0" json:"contentLength"`
	// PageSimhash 当前页面内容指纹（SimHash 64 位）
	PageSimhash uint64 `gorm:"column:page_simhash;default:0" json:"-"`
	MissCount   int        `gorm:"column:miss_count;default:0" json:"missCount"`
	Confirmed   bool       `gorm:"column:confirmed;default:false" json:"confirmed"`
	FirstSeen   *time.Time `gorm:"column:first_seen" json:"firstSeen"`
	LastSeen    *time.Time `gorm:"column:last_seen" json:"lastSeen"`
}

func (Service) TableName() string { return "services" }

// ComplianceRule 合规规则（敏感端口清单）
type ComplianceRule struct {
	ID       uint   `gorm:"primaryKey" json:"id"`
	Name     string `gorm:"column:name" json:"name"`
	Port     int    `gorm:"column:port" json:"port"`
	Protocol string `gorm:"column:protocol;default:tcp" json:"protocol"`
	Enabled  bool   `gorm:"column:enabled;default:true" json:"enabled"`
}

func (ComplianceRule) TableName() string { return "compliance_rules" }

// BastionWhitelist 堡垒机白名单
type BastionWhitelist struct {
	ID   uint   `gorm:"primaryKey" json:"id"`
	IP   string `gorm:"column:ip" json:"ip"`
	Note string `gorm:"column:note" json:"note"`
}

func (BastionWhitelist) TableName() string { return "bastion_whitelist" }

// ComplianceResult 合规结果
type ComplianceResult struct {
	ID         uint       `gorm:"primaryKey" json:"id"`
	AssetID    *uint      `gorm:"column:asset_id" json:"assetId"`
	IP         string     `gorm:"column:ip" json:"ip"`
	Port       int        `gorm:"column:port" json:"port"`
	RuleID     *uint      `gorm:"column:rule_id" json:"ruleId"`
	Status     string     `gorm:"column:status;default:violation" json:"status"`
	DetectedAt *time.Time `gorm:"column:detected_at" json:"detectedAt"`
	LastSeen   *time.Time `gorm:"column:last_seen" json:"lastSeen"`
	FixedAt    *time.Time `gorm:"column:fixed_at" json:"fixedAt"`
}

func (ComplianceResult) TableName() string { return "compliance_results" }

// PageSnapshot 页面快照
type PageSnapshot struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	AssetID    *uint     `gorm:"column:asset_id" json:"assetId"`
	URL        string    `gorm:"column:url" json:"url"`
	Title      string    `gorm:"column:title" json:"title"`
	ImagePath  string    `gorm:"column:image_path" json:"imagePath"`
	CapturedAt time.Time `gorm:"column:captured_at" json:"capturedAt"`
}

func (PageSnapshot) TableName() string { return "page_snapshots" }

// Node 扫描节点
type Node struct {
	ID            uint       `gorm:"primaryKey" json:"id"`
	Name          string     `gorm:"column:name;uniqueIndex" json:"name"`
	IP            string     `gorm:"column:ip" json:"ip"`
	Token         string     `gorm:"column:token" json:"-"`
	Status        string     `gorm:"column:status;default:offline" json:"status"`
	Timeout       int        `gorm:"column:timeout;default:3000" json:"timeout"`
	Concurrency   int        `gorm:"column:concurrency;default:1000" json:"concurrency"`
	LastHeartbeat *time.Time `gorm:"column:last_heartbeat" json:"lastHeartbeat"`
	TerminalLog   string     `gorm:"column:terminal_log;type:text" json:"-"`
	AgentVersion  string     `gorm:"column:agent_version" json:"agentVersion"`
	Projects      string     `gorm:"column:projects;type:text" json:"projects"` // config.db 里的 project 列表（JSON 数组）
	UpgradeReq    bool       `gorm:"column:upgrade_requested;default:false" json:"upgradeRequested"`
	CreatedAt     time.Time  `json:"createdAt"`
}

func (Node) TableName() string { return "nodes" }

// AgentTask 子任务：一个扫描任务在某个节点上的分片
type AgentTask struct {
	ID         uint       `gorm:"primaryKey" json:"id"`
	TaskID     uint       `gorm:"column:task_id;index" json:"taskId"`
	NodeID     uint       `gorm:"column:node_id;index" json:"nodeId"`
	NodeName   string     `gorm:"column:node_name" json:"nodeName"`
	IPRanges   string     `gorm:"column:ip_ranges" json:"ipRanges"`
	BatchNo    int        `gorm:"column:batch_no;default:0" json:"batchNo"`
	BatchTotal int        `gorm:"column:batch_total;default:0" json:"batchTotal"`
	Status     string     `gorm:"column:status;default:pending" json:"status"`
	Total      int        `gorm:"column:total;default:0" json:"total"`
	Scanned    int        `gorm:"column:scanned;default:0" json:"scanned"`
	Progress   int        `gorm:"column:progress;default:0" json:"progress"`
	Message    string     `gorm:"column:message" json:"message"`
	CreatedAt  time.Time  `json:"createdAt"`
	StartedAt  *time.Time `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt"`
}

func (AgentTask) TableName() string { return "agent_tasks" }

// User 用户
type User struct {
	ID           uint      `gorm:"primaryKey" json:"id"`
	Username     string    `gorm:"column:username;uniqueIndex" json:"username"`
	PasswordHash string    `gorm:"column:password_hash" json:"-"`
	Role         string    `gorm:"column:role;default:admin" json:"role"`
	CreatedAt    time.Time `json:"createdAt"`
}

func (User) TableName() string { return "users" }

// IPSegment 网段（机房 → 网段映射）
type IPSegment struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	Name        string    `gorm:"column:name" json:"name"`
	CIDR        string    `gorm:"column:cidr" json:"cidr"`
	Description string    `gorm:"column:description" json:"description"`
	CreatedAt   time.Time `json:"createdAt"`
}

func (IPSegment) TableName() string { return "ip_segments" }

// ExcludeRange 免扫描清单（IP 范围 + 原因）
type ExcludeRange struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	IPRange   string    `gorm:"column:ip_range" json:"ipRange"`
	Reason    string    `gorm:"column:reason" json:"reason"`
	CreatedAt time.Time `json:"createdAt"`
}

func (ExcludeRange) TableName() string { return "exclude_ranges" }

// ScanTemplate 扫描模板（端口 + 工具参数，拼接成 TscanClient 命令）
type ScanTemplate struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	Name      string    `gorm:"column:name" json:"name"`
	Modules   string    `gorm:"column:modules;default:port" json:"modules"`
	PortMode  string    `gorm:"column:port_mode;default:top100" json:"portMode"`
	Ports     string    `gorm:"column:ports" json:"ports"`
	Thread    int       `gorm:"column:thread;default:600" json:"thread"` // 并发 -t
	Timeout   int       `gorm:"column:timeout;default:3" json:"timeout"` // 超时 -time
	NoPing    bool      `gorm:"column:no_ping" json:"noPing"`            // -np 禁用存活探测
	NoSmart   bool      `gorm:"column:no_smart" json:"noSmart"`          // -nosmart 禁用启发式
	Finger    string    `gorm:"column:finger;default:tiny" json:"finger"`
	ExtraArgs string    `gorm:"column:extra_args" json:"extraArgs"` // 额外参数（自由拼接）
	CreatedAt time.Time `json:"createdAt"`
}

func (ScanTemplate) TableName() string { return "scan_templates" }

// Department 组织结构（部门，支持层级与预设负责人）
type Department struct {
	ID       uint   `gorm:"primaryKey" json:"id"`
	Name     string `gorm:"column:name" json:"name"`
	ParentID *uint  `gorm:"column:parent_id" json:"parentId"`
	Owners   string `gorm:"column:owners" json:"owners"` // 逗号分隔的负责人
	Sort     int    `gorm:"column:sort;default:0" json:"sort"`
}

func (Department) TableName() string { return "departments" }

// SystemSettings 系统设置（单行）：GitHub 仓库 + 代理（仅用于获取更新）
type SystemSettings struct {
	ID         uint   `gorm:"primaryKey;column:id" json:"-"`
	GithubRepo string `gorm:"column:github_repo" json:"githubRepo"` // 形如 owner/repo
	GithubToken string `gorm:"column:github_token" json:"-"`
	Proxy      string `gorm:"column:proxy" json:"proxy"` // HTTP 代理，仅用于 GitHub 请求
	Port       int    `gorm:"column:port;default:18888" json:"port"` // 服务端监听端口
}

func (SystemSettings) TableName() string { return "system_settings" }
