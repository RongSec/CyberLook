package service

import (
	"log"
	"time"

	"asset-platform/internal/config"
	"asset-platform/internal/model"
	"asset-platform/internal/scanner"
	"asset-platform/internal/target"
)

// 活跃 / 睡眠 结算
//
// 口径（与用户确认过的规则一致）：
//   * 每次「扫描完毕」结算一次，不是每个包结算一次；同一任务重复收尾不会双计
//     （host_states.last_task_id / services.last_miss_task 做幂等）。
//   * 主机存活（本次扫到至少一个开放端口）：
//       - 扫到的端口 → active，缺席计数清零
//       - 没扫到的端口 → 缺席 +1，连续 3 次 → sleeping
//   * 主机不存活（本次一个端口都没扫到）：
//       - 主机缺席 +1，连续 3 次 → 该 IP 旗下所有服务 sleeping
//       - 期间任何一次重新扫到 → 立即回 active
//   * 任何情况下都不删除服务记录：只改 activity，避免"删了行，下轮重扫又变成新发现"。
const activitySleepThreshold = 3

// SettleActivity 本机扫描完成后结算活跃/睡眠
func SettleActivity(taskID uint, targetIPs []string, results []scanner.ScanResult) {
	alivePorts := map[string]map[int]bool{}
	for _, r := range results {
		if alivePorts[r.IP] == nil {
			alivePorts[r.IP] = map[int]bool{}
		}
		alivePorts[r.IP][r.Port] = true
	}
	settle(taskID, targetIPs, alivePorts)
}

// SettleActivityForTask 队列/节点模式：按任务已入库的结果结算。
// 判定依据是 services.last_task_id = 本任务（该端口本次被扫到过）。
func SettleActivityForTask(taskID uint) {
	if config.DB == nil || taskID == 0 {
		return
	}
	var ips []string
	config.DB.Model(&model.TaskUnit{}).Where("task_id = ?", taskID).Distinct("ip").Pluck("ip", &ips)
	if len(ips) == 0 {
		var task model.ScanTask
		if err := config.DB.First(&task, taskID).Error; err == nil {
			ips = target.Resolve(parseTargets(task.IPRanges), ExcludeRangeStrings())
		}
	}
	alivePorts := map[string]map[int]bool{}
	type row struct {
		IP   string
		Port int
	}
	var rows []row
	config.DB.Model(&model.Service{}).Select("ip::text AS ip, port").
		Where("last_task_id = ?", taskID).Scan(&rows)
	for _, r := range rows {
		if alivePorts[r.IP] == nil {
			alivePorts[r.IP] = map[int]bool{}
		}
		alivePorts[r.IP][r.Port] = true
	}
	settle(taskID, ips, alivePorts)
}

// settle 结算主体：alivePorts = 本次扫到的 ip → 端口集合
func settle(taskID uint, targetIPs []string, alivePorts map[string]map[int]bool) {
	if config.DB == nil || len(targetIPs) == 0 {
		return
	}

	woke, slept, hostSlept := 0, 0, 0
	for _, ip := range targetIPs {
		var hs model.HostState
		hasHost := config.DB.Where("ip = ?", ip).First(&hs).Error == nil
		if hasHost && hs.LastTaskID >= int64(taskID) {
			continue // 本任务已结算过
		}
		ports := alivePorts[ip]

		if len(ports) > 0 {
			upsertHostState(ip, true, 0, taskID)
			var svcs []model.Service
			config.DB.Where("ip = ?", ip).Find(&svcs)
			for _, s := range svcs {
				if ports[s.Port] {
					if s.Activity != model.ActivityActive || s.MissCount != 0 {
						config.DB.Model(&model.Service{}).Where("id = ?", s.ID).
							Updates(map[string]interface{}{"activity": model.ActivityActive, "miss_count": 0})
						woke++
					}
					continue
				}
				if s.LastMissTask >= int64(taskID) {
					continue
				}
				miss := s.MissCount + 1
				act := s.Activity
				if miss >= activitySleepThreshold {
					act = model.ActivitySleeping
				}
				config.DB.Model(&model.Service{}).Where("id = ?", s.ID).Updates(map[string]interface{}{
					"miss_count": miss, "activity": act, "last_miss_task": taskID,
				})
				if act == model.ActivitySleeping && s.Activity != model.ActivitySleeping {
					slept++
				}
			}
			continue
		}

		// 主机不存活：缺席累积，达到阈值把旗下全部服务标睡眠
		miss := 1
		if hasHost {
			miss = hs.MissCount + 1
		}
		upsertHostState(ip, false, miss, taskID)
		if miss >= activitySleepThreshold {
			r := config.DB.Model(&model.Service{}).Where("ip = ? AND activity <> ?", ip, model.ActivitySleeping).
				Update("activity", model.ActivitySleeping)
			if r.Error == nil && r.RowsAffected > 0 {
				hostSlept++
				slept += int(r.RowsAffected)
			}
		}
	}

	if woke+slept > 0 {
		log.Printf("[活跃度] 任务 #%d 结算：唤醒 %d 条，睡眠 %d 条（主机整体睡眠 %d 台）", taskID, woke, slept, hostSlept)
	}
}

// upsertHostState 写入主机状态（同一 IP 一行）
func upsertHostState(ip string, alive bool, miss int, taskID uint) {
	config.DB.Exec(`
		INSERT INTO host_states (ip, alive, miss_count, last_task_id, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (ip) DO UPDATE SET
			alive = EXCLUDED.alive,
			miss_count = EXCLUDED.miss_count,
			last_task_id = EXCLUDED.last_task_id,
			updated_at = EXCLUDED.updated_at`,
		ip, alive, miss, taskID, time.Now())
}

// WakeServices 外部让某 IP 的服务立即回到活跃（人工确认处置时用）
func WakeServices(ip string) {
	if config.DB == nil {
		return
	}
	config.DB.Model(&model.Service{}).Where("ip = ?", ip).
		Updates(map[string]interface{}{"activity": model.ActivityActive, "miss_count": 0})
	upsertHostState(ip, true, 0, 0)
}
