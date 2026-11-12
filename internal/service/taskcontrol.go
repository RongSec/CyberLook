package service

import (
	"errors"

	"asset-platform/internal/tasklog"
	"time"

	"asset-platform/internal/config"
	"asset-platform/internal/model"
)

// PauseTask 暂停任务：
//  - 状态置 paused
//  - 未完成的分片取消，IP 放回存活池
//  - 停止继续探测（needProbe 会跳过非 running 状态）
func PauseTask(id uint) error {
	var t model.ScanTask
	if err := config.DB.First(&t, id).Error; err != nil {
		return errors.New("任务不存在")
	}
	if t.Status != "running" {
		return errors.New("只有运行中的任务可以暂停")
	}
	// 队列任务：执行层暂停（引擎冻结 + 停领新单元），**不杀会话、不打回 leased 单元**——
	// 心跳仍在续租，恢复后当前单元无缝继续（这正是单 IP 队列相对分片制的暂停优势）。
	if t.QueueMode {
		SetTaskPause(id, true)
		config.DB.Model(&model.ScanTask{}).Where("id = ?", id).Update("status", "paused")
		tasklog.AppendTS(id, "[队列] 任务已暂停：当前单元在途探针结束后冻结，暂停期间不再领取")
		return nil
	}

	// 取消未完成分片，IP 收回池子
	var shards []model.AgentTask
	config.DB.Where("task_id = ? AND status IN ?", id, []string{"pending", "running"}).Find(&shards)
	for _, s := range shards {
		ips := splitIPs(s.IPRanges)
		if v, ok := pipelines.Load(id); ok {
			p := v.(*taskPipeline)
			p.mu.Lock()
			p.alivePool = append(p.alivePool, ips...)
			snapOffset := p.offset
			snapPool := append([]string(nil), p.alivePool...)
			p.mu.Unlock()
			_ = savePipelineFile(id, snapOffset, snapPool)
		}
		config.DB.Model(&model.AgentTask{}).Where("id = ?", s.ID).
			Updates(map[string]interface{}{
				"status":      "cancelled",
				"message":     "任务暂停，IP 已收回待扫池",
				"finished_at": time.Now(),
			})
	}

	config.DB.Model(&model.ScanTask{}).Where("id = ?", id).
		Update("status", "paused")
	tasklog.AppendTS(id, "[调度] 任务已暂停，%d 个分片的 IP 已收回待扫池", len(shards))
	return nil
}

// ResumeTask 继续任务：状态置 running，从池子重新派发
func ResumeTask(id uint) error {
	var t model.ScanTask
	if err := config.DB.First(&t, id).Error; err != nil {
		return errors.New("任务不存在")
	}
	if t.Status != "paused" && t.Status != "stopped" {
		return errors.New("只有暂停/停止的任务可以继续")
	}
	// 队列任务：解冻执行层 + 状态回 running + 唤醒节点领取循环
	if t.QueueMode {
		SetTaskPause(id, false)
		config.DB.Model(&model.ScanTask{}).Where("id = ?", id).Update("status", "running")
		tasklog.AppendTS(id, "[队列] 任务继续：恢复领取与扫描")
		wakeQueueConsumers(&t)
		return nil
	}
	// 注意：不要在这里 StartTaskRun
	// 概览按「最近一次执行」时间窗口统计，新建 run 会把窗口重置 → 概览清零。
	// 暂停/继续属于同一次执行，必须保持原窗口；只有「停止后重跑」「完成后重跑」才开新 run。
	config.DB.Model(&model.ScanTask{}).Where("id = ?", id).Update("status", "running")
	tasklog.AppendTS(id, "[调度] 任务继续执行，重新派发待扫池中的 IP")
	go DispatchNextBatch(id)
	return nil
}

// RestartTask 重跑任务：清空断点/池子/分片，从头开始
func RestartTask(id uint) error {
	var t model.ScanTask
	if err := config.DB.First(&t, id).Error; err != nil {
		return errors.New("任务不存在")
	}
	if t.Status == "running" {
		return errors.New("请先暂停/停止再重跑")
	}
	// 队列任务重跑：单元全部回 pending（attempts 清零），清会话，重走队列下发
	if t.QueueMode {
		config.DB.Where("task_id = ? AND batch_no = 0", id).Delete(&model.AgentTask{})
		ResetUnits(id)
		ClearTaskCtrl(id)
		DropETAState(id)
		queueNotifyOnce.Delete(id)
		config.DB.Model(&model.ScanTask{}).Where("id = ?", id).
			Updates(map[string]interface{}{"status": "pending", "error": "", "probe_offset": 0, "alive_count": 0})
		StartTaskRun(id)
		tasklog.AppendTS(id, "[队列] 任务已重跑：全部单元回炉（attempts/进度清零）")
		go func() {
			var fresh model.ScanTask
			if config.DB.First(&fresh, id).Error == nil {
				local, err := DispatchQueueTask(&fresh)
				if err != nil {
					tasklog.AppendTS(id, "[队列] 重跑下发失败：%v", err)
					config.DB.Model(&model.ScanTask{}).Where("id = ?", id).Updates(map[string]interface{}{"status": "failed", "error": err.Error()})
					return
				}
				if local {
					_ = ExecuteTaskQueue(id)
				}
			}
		}()
		return nil
	}
	// 清空分片与断点
	config.DB.Where("task_id = ?", id).Delete(&model.AgentTask{})
	CleanPipeline(id)
	config.DB.Model(&model.ScanTask{}).Where("id = ?", id).
		Updates(map[string]interface{}{
			"status":       "pending",
			"probe_offset": 0,
			"alive_count":  0,
		})
	StartTaskRun(id)
	tasklog.AppendTS(id, "[调度] 任务已重跑，探测进度清零")
	go DispatchTask(&t)
	return nil
}
