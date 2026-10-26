package model

import "time"

// Fault 故障记录（服务端中断 / 节点故障 / 分片失败 / 熔断）
type Fault struct {
	ID         uint       `gorm:"primaryKey" json:"id"`
	Kind       string     `gorm:"column:kind;index" json:"kind"`
	NodeID     uint       `gorm:"column:node_id;default:0" json:"nodeId"`
	NodeName   string     `gorm:"column:node_name;default:" json:"nodeName"`
	TaskID     uint       `gorm:"column:task_id;default:0" json:"taskId"`
	Message    string     `gorm:"column:message;default:" json:"message"`
	CreatedAt  *time.Time `gorm:"column:created_at" json:"createdAt"`
	ResolvedAt *time.Time `gorm:"column:resolved_at" json:"resolvedAt"`
}

func (Fault) TableName() string { return "faults" }
