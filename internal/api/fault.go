package api

import (
	"net/http"
	"sort"
	"strconv"

	"asset-platform/internal/config"
	"asset-platform/internal/model"
	"asset-platform/internal/service"

	"github.com/gin-gonic/gin"
)

// ListFaults 故障列表（kind 可选：server_restart / node_offline / node_fused / shard_failed）
func ListFaults(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, _ := strconv.Atoi(c.DefaultQuery("size", "20"))
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 500 {
		size = 20
	}
	rows, total := service.ListFaults(c.Query("kind"), page, size)
	if rows == nil {
		rows = []model.Fault{}
	}
	c.JSON(http.StatusOK, gin.H{"items": rows, "total": total, "page": page, "size": size})
}

// SystemLogs 系统日志：故障 + 任务执行记录，按时间倒序混合
func SystemLogs(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, _ := strconv.Atoi(c.DefaultQuery("size", "50"))
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 500 {
		size = 50
	}

	type logItem struct {
		Time    string `json:"time"`
		Level   string `json:"level"` // info / warn / error
		Source  string `json:"source"`
		Message string `json:"message"`
	}
	items := []logItem{}

	var faults []model.Fault
	config.DB.Order("created_at DESC").Limit(200).Find(&faults)
	for _, f := range faults {
		level := "warn"
		if f.Kind == "server_restart" || f.Kind == "shard_failed" {
			level = "error"
		}
		t := ""
		if f.CreatedAt != nil {
			t = f.CreatedAt.Format("2006-01-02 15:04:05")
		}
		src := f.Kind
		if f.NodeName != "" {
			src = f.Kind + " / " + f.NodeName
		}
		items = append(items, logItem{Time: t, Level: level, Source: src, Message: f.Message})
	}

	var runs []model.TaskRun
	config.DB.Order("started_at DESC").Limit(100).Find(&runs)
	for _, r := range runs {
		t := ""
		if r.StartedAt != nil {
			t = r.StartedAt.Format("2006-01-02 15:04:05")
		}
		level := "info"
		if r.Status == "failed" {
			level = "error"
		} else if r.Status == "interrupted" {
			level = "warn"
		}
		items = append(items, logItem{
			Time: t, Level: level, Source: "task_run",
			Message: "任务 #" + strconv.Itoa(int(r.TaskID)) + " 执行 " + r.Status,
		})
	}

	// 按时间倒序（sort.Slice 是 O(n log n)）
	sort.Slice(items, func(i, j int) bool { return items[i].Time > items[j].Time })

	total := len(items)
	start := (page - 1) * size
	if start > total {
		start = total
	}
	end := start + size
	if end > total {
		end = total
	}
	c.JSON(http.StatusOK, gin.H{"items": items[start:end], "total": total, "page": page, "size": size})
}

// GetFaultsRetention 读取故障保留天数
func GetFaultsRetention(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"days": service.GetFaultsRetentionDays()})
}

// SaveFaultsRetention 保存故障保留天数
func SaveFaultsRetention(c *gin.Context) {
	var req struct {
		Days int `json:"days"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	if req.Days < 1 || req.Days > 365 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "保留天数需在 1-365 之间"})
		return
	}
	config.DB.Exec(`INSERT INTO system_settings (id, faults_retention_days) VALUES (1, ?)
		ON CONFLICT (id) DO UPDATE SET faults_retention_days = EXCLUDED.faults_retention_days, updated_at = now()`, req.Days)
	c.JSON(http.StatusOK, gin.H{"message": "已保存"})
}
