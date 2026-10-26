package scheduler

import (
	"log"

	"asset-platform/internal/config"
	"asset-platform/internal/model"
	"asset-platform/internal/service"

	"github.com/robfig/cron/v3"
)

var c *cron.Cron

// Start 启动定时任务调度器
func Start() {
	Reload()
	log.Printf("[scheduler] 定时任务调度器启动")
}

// Reload 重新加载所有 cron 任务（任务增删后调用）
func Reload() {
	if c != nil {
		c.Stop()
	}
	c = cron.New()
	var tasks []model.ScanTask
	config.DB.Where("cron IS NOT NULL AND cron != ''").Find(&tasks)
	for _, t := range tasks {
		taskID := t.ID
		cronExpr := t.Cron
		if _, err := cron.ParseStandard(cronExpr); err != nil {
			continue
		}
		_, _ = c.AddFunc(cronExpr, func() {
			log.Printf("[scheduler] 触发定时任务 #%d (%s)", taskID, cronExpr)
			go func() {
				var t model.ScanTask
				if config.DB.First(&t, taskID).Error != nil {
					return
				}
				// 防重入：上一轮还没跑完就不要触发，否则会删掉正在执行的分片
				if t.Status == "running" {
					log.Printf("[scheduler] 任务 #%d 仍在运行，跳过本次触发", taskID)
					return
				}
				// 重跑视为全新开始：清掉上一轮分片
				config.DB.Where("task_id = ?", taskID).Delete(&model.AgentTask{})
				service.StartTaskRun(taskID)
				// nodeIds 为空时 DispatchTask 会自动指派给本机节点，之后再看是否真的分片了
				if err := service.DispatchTask(&t); err == nil && t.NodeIDs != "" {
					config.DB.Model(&t).Update("status", "running")
					log.Printf("[scheduler] 任务 #%d 已分发给节点执行", taskID)
					return
				} else if err != nil {
					log.Printf("[scheduler] 任务 #%d 分发失败，回退本机执行: %v", taskID, err)
				}
				_ = service.ExecuteTask(taskID)
			}()
		})
	}
	c.Start()
}
