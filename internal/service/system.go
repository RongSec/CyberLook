package service

import (
	"time"

	"asset-platform/internal/config"
	"asset-platform/internal/model"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/mem"
)

// SystemStats 系统运行状态
type SystemStats struct {
	CPUPercent   float64 `json:"cpuPercent"`
	MemUsedMB    uint64  `json:"memUsedMB"`
	MemTotalMB   uint64  `json:"memTotalMB"`
	MemPercent   float64 `json:"memPercent"`
	RunningTasks int64   `json:"runningTasks"`
}

// GetSystemStats 读取 CPU/内存占用 + 运行中任务数
func GetSystemStats() SystemStats {
	var s SystemStats
	if pcts, err := cpu.Percent(500*time.Millisecond, false); err == nil && len(pcts) > 0 {
		s.CPUPercent = pcts[0]
	}
	if vm, err := mem.VirtualMemory(); err == nil {
		s.MemUsedMB = vm.Used / 1024 / 1024
		s.MemTotalMB = vm.Total / 1024 / 1024
		s.MemPercent = vm.UsedPercent
	}
	config.DB.Model(&model.ScanTask{}).Where("status = 'running'").Count(&s.RunningTasks)
	return s
}
