package model

import "time"

// TaskUnit IP 租约队列单元：一个任务的一个目标 IP。
// 生命周期：pending --claim--> leased --done--> done；
// 租约过期（节点崩/断网）→ reaper 重置 pending（attempts+1），≥3 次判 failed_unit。
// 唯一键 (task_id, ip)：任务内 IP 天然去重；跨任务各自独立。
// 列与 migrations/041_task_units.sql 一一对应（表结构以迁移 SQL 为准）。
type TaskUnit struct {
	ID     uint   `gorm:"primaryKey" json:"id"`
	TaskID uint   `gorm:"column:task_id;uniqueIndex:idx_task_units_task_ip,priority:1" json:"taskId"`
	IP     string `gorm:"column:ip;size:64;uniqueIndex:idx_task_units_task_ip,priority:2" json:"ip"`
	Status string `gorm:"column:status;size:16;default:pending" json:"status"` // pending|leased|done|failed_unit
	NodeID uint   `gorm:"column:node_id;default:0" json:"nodeId"`              // 0=未领取/本机
	// SessionID 领取时的执行槽位（agent_tasks.id；队列模式下是该节点的队列会话分片）
	SessionID   uint       `gorm:"column:session_id;default:0" json:"sessionId"`
	LeaseExpire *time.Time `gorm:"column:lease_expire" json:"leaseExpire"`
	Attempts    int        `gorm:"column:attempts;default:0" json:"attempts"`
	ProbesDone  int64      `gorm:"column:probes_done;default:0" json:"probesDone"`
	// ProbesTotal 该单元总探针数（1 IP × 端口数，创建即真值——进度分母从不完备的元凶在此根治）
	ProbesTotal int64   `gorm:"column:probes_total;default:0" json:"probesTotal"`
	Pps         float64 `gorm:"column:pps;default:0" json:"pps"`
	// OpenFound/identified 单元实时发现数（进度展示用）
	OpenFound  int64     `gorm:"column:open_found;default:0" json:"openFound"`
	Identified int64     `gorm:"column:identified;default:0" json:"identified"`
	UpdatedAt  time.Time `gorm:"column:updated_at;autoUpdateTime" json:"updatedAt"`
	CreatedAt  time.Time `gorm:"column:created_at" json:"createdAt"`
}

func (TaskUnit) TableName() string { return "task_units" }

// 单元状态常量（与 041 迁移注释口径一致）
const (
	UnitPending    = "pending"
	UnitLeased     = "leased"
	UnitDone       = "done"
	UnitFailedUnit = "failed_unit"
)
