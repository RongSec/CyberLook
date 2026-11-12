package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"asset-platform/internal/config"
	"asset-platform/internal/model"
	"asset-platform/internal/scanner"
	"asset-platform/internal/similarity"
	"asset-platform/internal/target"
	"asset-platform/internal/tasklog"

	"gorm.io/gorm"
)

// TaskInput 创建任务输入
type TaskInput struct {
	Name       string
	IPRanges   string
	PortMode   string
	Ports      string
	Cron       string
	TemplateID *uint
	Modules    string
	Engine     string // builtin / tscan
	Finger     string
	Thread     int
	Timeout    int
	NoPing     bool
	AliveProbe bool // 是否先探测存活（默认 true；false=全量直接下发）
	NoSmart    bool
	ExtraArgs  string
	NodeIDs    string
	BatchSize  int // 每批下发的 IP 数（0=默认 100）
}

// CreateTask 创建扫描任务
func CreateTask(in TaskInput) (*model.ScanTask, error) {
	task := model.ScanTask{
		Name: in.Name, IPRanges: in.IPRanges, PortMode: in.PortMode, Ports: in.Ports,
		Cron: in.Cron, Status: "pending", TemplateID: in.TemplateID,
		Modules: in.Modules, Engine: in.Engine, Finger: in.Finger,
		Thread: in.Thread, Timeout: in.Timeout, NoPing: in.NoPing, NoSmart: in.NoSmart,
		AliveProbe: in.AliveProbe,
		ExtraArgs:  in.ExtraArgs, NodeIDs: in.NodeIDs, BatchSize: in.BatchSize,
	}
	if task.Modules == "" {
		task.Modules = "port"
	}
	if task.Finger == "" {
		task.Finger = "tiny"
	}
	if task.Thread == 0 {
		task.Thread = 600
	}
	if task.Timeout == 0 {
		task.Timeout = 3
	}
	// 队列模式创建即固化（重启/翻 USE_IP_QUEUE 不影响已建任务）
	task.QueueMode = UseIPQueue()
	if err := config.DB.Create(&task).Error; err != nil {
		return nil, err
	}
	return &task, nil
}

// ListTasks 任务列表（倒序）
func ListTasks(page, size int) ([]model.ScanTask, int64) {
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 5000 {
		size = 20
	}
	var total int64
	config.DB.Model(&model.ScanTask{}).Count(&total)
	var tasks []model.ScanTask
	config.DB.Order("id desc").Offset((page - 1) * size).Limit(size).Find(&tasks)
	return tasks, total
}

// GetTask 获取单个任务
func GetTask(id uint) (*model.ScanTask, error) {
	var task model.ScanTask
	if err := config.DB.First(&task, id).Error; err != nil {
		return nil, err
	}
	return &task, nil
}

// DeleteTask 删除任务及其快照、报告文件
func DeleteTask(id uint) error {
	var task model.ScanTask
	if err := config.DB.First(&task, id).Error; err == nil {
		removeTaskReports(task.ReportPath, taskProjectID(task))
	}
	config.DB.Where("task_id = ?", id).Delete(&model.ScanSnapshot{})
	config.DB.Where("task_id = ?", id).Delete(&model.TaskUnit{}) // 队列单元随任务销毁
	CleanPipeline(id)                                            // 清掉内存里的流水线状态
	ClearTaskCtrl(id)
	DropETAState(id)
	queueNotifyOnce.Delete(id)
	DeleteTaskLogs(id)
	return config.DB.Delete(&model.ScanTask{}, id).Error
}

// reportsDir 扫描报告 HTML 的输出目录（原为 TscanClient 工作目录；
// 自研引擎不再写 HTML 报告，这里仅作历史报告的清理锚点，恒为空目录/不存在）
func reportsDir() string {
	if d := os.Getenv("DATA_DIR"); d != "" {
		return filepath.Join(d, "reports")
	}
	return "data/reports"
}

// removeTaskReports 删除任务关联的报告文件（含同名前缀历史文件）
func removeTaskReports(reportPath, projectID string) {
	if reportPath != "" {
		_ = os.Remove(reportPath)
	}
	workDir := reportsDir()
	if projectID != "" {
		matches, _ := filepath.Glob(filepath.Join(workDir, projectID+"-*.html"))
		for _, m := range matches {
			_ = os.Remove(m)
		}
	}
}

// findTaskReport 找任务本次生成的 HTML 报告（取最新）
func findTaskReport(projectID string) string {
	workDir := reportsDir()
	matches, _ := filepath.Glob(filepath.Join(workDir, projectID+"-*.html"))
	if len(matches) == 0 {
		return ""
	}
	sort.Strings(matches)
	return matches[len(matches)-1]
}

// CleanOldReports 清理 N 天前的扫描报告文件，返回删除数量
func CleanOldReports(days int) int {
	workDir := reportsDir()
	matches, _ := filepath.Glob(filepath.Join(workDir, "*.html"))
	cutoff := time.Now().AddDate(0, 0, -days)
	removed := 0
	for _, m := range matches {
		if info, err := os.Stat(m); err == nil && info.ModTime().Before(cutoff) {
			_ = os.Remove(m)
			removed++
		}
	}
	return removed
}

// BatchDeleteTasks 批量删除任务
func BatchDeleteTasks(ids []uint) error {
	for _, id := range ids {
		if err := DeleteTask(id); err != nil {
			return err
		}
	}
	return nil
}

// BatchDeleteAssets 批量删除资产（级联服务/违规/快照）
func BatchDeleteAssets(ids []uint) error {
	for _, id := range ids {
		config.DB.Where("asset_id = ?", id).Delete(&model.Service{})
		config.DB.Where("asset_id = ?", id).Delete(&model.ComplianceResult{})
		config.DB.Where("asset_id = ?", id).Delete(&model.PageSnapshot{})
		if err := config.DB.Delete(&model.Asset{}, id).Error; err != nil {
			return err
		}
	}
	return nil
}

// ExecuteTask 执行扫描任务：调自研引擎（流式）→ 结果入库（快照/服务/资产）
// StopTask 停止任务：取消本机扫描，节点分片标记取消（agent 心跳收到 cancelled 后取消本机扫描）
// v0.0.27 队列任务取消口径（用户拍板）：status=failed + error=「已取消（部分结果已入库）」，
// 在扫单元由 agent 保留部分结果后停；未领单元留在 pending（重跑可续）。
func StopTask(taskID uint) error {
	var task model.ScanTask
	if err := config.DB.First(&task, taskID).Error; err != nil {
		return err
	}
	now := time.Now()
	if task.QueueMode {
		// 本机队列循环：命中当前在扫单元句柄（ProjectID=任务名，本机串行单句柄）
		scanner.StopScan(taskProjectID(task))
		// 节点会话行全部置 cancelled：agent 心跳收到 cancelled → 停当前单元、退出领取循环
		config.DB.Model(&model.AgentTask{}).
			Where("task_id = ? AND batch_no = 0 AND status IN ?", taskID, []string{"pending", "running"}).
			Updates(map[string]interface{}{"status": "cancelled", "message": "已取消（部分结果已入库）", "finished_at": now})
		// 正在 leased 的单元打回 pending（执行方已停或即将停，不必等 reaper；
		// 若 agent 慢半拍仍在上报，ReportUnit 条件更新命中不到 leased 行会静默忽略）
		config.DB.Model(&model.TaskUnit{}).
			Where("task_id = ? AND status = ?", taskID, model.UnitLeased).
			Updates(map[string]interface{}{
				"status": model.UnitPending, "node_id": 0, "session_id": 0,
				"lease_expire": nil, "updated_at": now,
			})
		ClearTaskCtrl(taskID)
		DropETAState(taskID)
		FinishTaskRun(taskID, "failed")
		tasklog.AppendTS(taskID, "[队列] 任务已取消：部分结果已入库，未完成单元保留在队列（重跑续扫）")
		return config.DB.Model(&model.ScanTask{}).Where("id = ?", taskID).Updates(map[string]interface{}{
			"status": "failed", "error": "已取消（部分结果已入库）", "finished_at": now,
		}).Error
	}
	scanner.StopScan(taskProjectID(task))
	// 停止时把未结束的分片统一置为 cancelled 并补结束时间，
	// 否则它们会一直挂在 running，主任务永远不收敛
	config.DB.Model(&model.AgentTask{}).
		Where("task_id = ? AND status IN ?", taskID, []string{"pending", "running"}).
		Updates(map[string]interface{}{"status": "cancelled", "message": "已手动停止", "finished_at": now})
	// 取消终态统一口径（与队列路径一致）：status=failed + error="已取消（部分结果已入库）"。
	// 不能是 done/成功，也不能只写 "stopped" 让迟到事件把它翻回 done。
	ClearTaskCtrl(taskID)
	FinishTaskRun(taskID, "failed")
	tasklog.AppendTS(taskID, "[调度] 任务已取消：部分结果已入库")
	return config.DB.Model(&task).Updates(map[string]interface{}{
		"status": "failed", "error": "已取消（部分结果已入库）", "finished_at": now,
	}).Error
}

func ExecuteTask(taskID uint) error {
	var task model.ScanTask
	if err := config.DB.First(&task, taskID).Error; err != nil {
		return err
	}
	// v0.0.27 本机队列路径：queue_mode 已固化或全局开关开启 → 进程内消费租约队列
	// （claim→单 IP 扫描→回报→再 claim），与 agent 路径同构；旧单机整段扫描退役。
	if task.QueueMode || UseIPQueue() {
		return ExecuteTaskQueue(taskID)
	}
	now := time.Now()
	config.DB.Model(&task).Updates(map[string]interface{}{"status": "running", "started_at": now})
	tasklog.Clear(task.ID)
	targets := parseTargets(task.IPRanges)
	tasklog.AppendTS(task.ID, "[本机] 收到任务「%s」，共 %d 个目标段，开始执行", task.Name, len(targets))
	ports := task.Ports
	switch task.PortMode {
	case "top100":
		ports = "" // 默认 Top100（引擎空串即 Top100）
	case "all":
		ports = "1-65535"
	}

	opts := scanner.ScanOptions{
		ProjectID: taskProjectID(task),
		Modules:   task.Modules, Finger: task.Finger,
		Ports: ports, Thread: task.Thread, Timeout: task.Timeout * 1000, // 秒→毫秒（与 agent 侧一致）
		NoPing: task.NoPing, NoSmart: task.NoSmart,
		ExtraArgs: task.ExtraArgs,
	}
	// 引擎选择：字段保留兼容老任务数据；v0.0.27 起引擎唯一（自研），路由已收敛
	if task.Engine != "" {
		opts.Engine = task.Engine
	}
	// 本地执行路径：取消由 StopTask → scanner.StopScan(taskProjectID) 直接命中
	// （同 ProjectID）；暂停/调速由下面的轮询器 1.5s 投影到同一 Controls 句柄。
	stopPoll := make(chan struct{})
	defer close(stopPoll)
	go func() {
		t := time.NewTicker(1500 * time.Millisecond)
		defer t.Stop()
		appliedPaused := false
		for {
			select {
			case <-stopPoll:
				return
			case <-t.C:
				cmd := TaskCtrlSnapshot(task.ID)
				switch {
				case cmd.Paused && !appliedPaused:
					if scanner.ControlScan(opts.ProjectID, scanner.ScanCtl{Action: "pause"}) {
						tasklog.AppendTS(task.ID, "[调控] 本机扫描已挂起（许可清零，在途探针自然结束）")
						appliedPaused = true
					}
				case !cmd.Paused && appliedPaused:
					if scanner.ControlScan(opts.ProjectID, scanner.ScanCtl{Action: "resume"}) {
						tasklog.AppendTS(task.ID, "[调控] 本机扫描已恢复")
						appliedPaused = false
					}
				}
				if !cmd.Paused && cmd.Action == "limit" && (cmd.Concurrency > 0 || cmd.TimeoutMs > 0) {
					scanner.ControlScan(opts.ProjectID, scanner.ScanCtl{
						Action: "limit", Concurrency: cmd.Concurrency, TimeoutMs: cmd.TimeoutMs})
				}
			}
		}
	}()
	results, err := scanner.RunPortScan(targets, ExcludeRangeStrings(), opts, func(line string, isProgress bool) {
		if isProgress {
			tasklog.SetProgress(task.ID, line)
		} else {
			tasklog.Append(task.ID, line)
		}
	}, ingestServicesLive)
	if err != nil {
		tasklog.Append(task.ID, "[错误] "+err.Error())
		tasklog.SetProgress(task.ID, "")
		status := "failed"
		// 取消（StopTask/调控取消）导致的提前结束按 stopped 收敛，部分结果保留
		if errors.Is(err, context.Canceled) {
			status = "stopped"
			tasklog.AppendTS(task.ID, "[本机] 扫描已取消，保留部分结果 %d 条", len(results))
		}
		config.DB.Model(&task).Updates(map[string]interface{}{"status": status, "finished_at": time.Now()})
		return err
	}
	tasklog.SetProgress(task.ID, "") // 扫描结束清空进度条
	ClearTaskCtrl(task.ID)           // 执行收敛：内存调控态归零（rerun 用干净状态）
	if err := ingestResults(task.ID, results); err != nil {
		config.DB.Model(&task).Updates(map[string]interface{}{"status": "failed", "finished_at": time.Now()})
		return err
	}
	// 连续 3 次未发现才剔除
	expanded := target.Resolve(targets, ExcludeRangeStrings())
	markMissingServices(expanded, results)
	// 注意：扫描结束后不再自动跑合规检测。合规结果只由「合规检测」页手动触发产生
	//（手动检测会对端口做实连确认），避免用陈旧扫描数据刷出一堆假违规。
	// 记录本次报告文件
	reportPath := findTaskReport(taskProjectID(task))
	tasklog.AppendTS(task.ID, "[本机] 扫描完成，共 %d 条结果", len(results))
	config.DB.Model(&task).Updates(map[string]interface{}{
		"status": "done", "finished_at": time.Now(), "report_path": reportPath,
	})
	return nil
}

// taskProjectID 生成 -pr 项目名（用任务名，空则回退任务 ID）
func taskProjectID(task model.ScanTask) string {
	name := strings.TrimSpace(task.Name)
	if name == "" {
		return strconv.Itoa(int(task.ID))
	}
	return name
}

// parseTargets 解析 IP 范围输入（逗号/换行分隔）
func parseTargets(input string) []string {
	fields := strings.FieldsFunc(input, func(r rune) bool {
		return r == ',' || r == '，' || r == ';' || r == '；' ||
			r == '\n' || r == '\r' || r == ' ' || r == '\t' || r == '、'
	})
	var out []string
	for _, f := range fields {
		f = strings.TrimSpace(f)
		if f != "" {
			out = append(out, f)
		}
	}
	return out
}

// ingestService 单个服务 upsert + 确保资产存在（不写快照）
func ingestService(ip string, p scanner.ScanResult, taskID uint) {
	now := time.Now()
	isWeb := strings.HasPrefix(strings.ToUpper(p.Protocol), "HTTP")

	var asset model.Asset
	if err := config.DB.Where("ip = ?", ip).First(&asset).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			// 主机资产不需要确认（默认已登记）；仅发现 web 服务才进入待处置
			status := "confirmed"
			if isWeb {
				status = "pending"
			}
			asset = model.Asset{IP: ip, Source: "scan", Status: status}
			config.DB.Create(&asset)
			config.DB.Where("ip = ?", ip).First(&asset)
		} else {
			return
		}
	} else if isWeb && asset.Owner == "" && asset.SystemName == "" &&
		asset.Status == "confirmed" && asset.ConfirmedTitle == "" {
		// 已有主机记录，后发现的 web 服务且尚未标注 → 转入待处置
		config.DB.Model(&asset).Update("status", "pending")
		asset.Status = "pending"
	}
	var svc model.Service
	err := config.DB.Where("ip = ? AND port = ?", ip, p.Port).First(&svc).Error

	// 指纹：优先 urlscan 的指纹，回退 banner
	fpStr := p.Fingerprint
	if fpStr == "" {
		fpStr = p.Banner
	}

	// 页面内容指纹：仅当有响应体时计算
	fp := similarity.Fingerprint(p.Response)

	// ===== 新旧识别合并 =====
	// 旧值：上次扫描的 title/fingerprint
	prevTitle := ""
	prevFP := ""
	if err != gorm.ErrRecordNotFound {
		prevTitle = svc.Title
		prevFP = svc.Fingerprint
	}

	// 合并 title（语义一致用旧，避免无意义"新变更"）
	mergedTitle, titleSrc, titleChanged := scanner.MergeIdentification(prevTitle, p.Title)
	if titleChanged && prevTitle != "" {
		// 有变更，保存旧值用于对比
		svc.PrevTitle = prevTitle
	}

	// 响应码/长度变化时留旧值（待处置里展示"旧→新"）
	prevStatus := ""
	prevLen := 0
	if err != gorm.ErrRecordNotFound {
		prevStatus = svc.StatusCode
		prevLen = svc.ContentLength
	}

	// 合并 fingerprint
	mergedFP, _, fpChanged := scanner.MergeFingerprint(prevFP, fpStr)
	if fpChanged && prevFP != "" {
		svc.PrevFingerprint = prevFP
	}

	// 更新后的值
	p.Title = mergedTitle
	fpStr = mergedFP

	if err == gorm.ErrRecordNotFound {
		config.DB.Create(&model.Service{
			AssetID: &asset.ID, IP: ip, Port: p.Port, Protocol: p.Protocol,
			Service: p.Service, Banner: p.Banner, Title: p.Title, Fingerprint: fpStr,
			StatusCode: p.StatusCode, ContentLength: p.ContentLength, PageSimhash: fp,
			PrevTitle: svc.PrevTitle, PrevFingerprint: svc.PrevFingerprint, TitleSource: titleSrc,
			FirstSeen: &now, LastSeen: &now,
			TaskID: taskID, LastTaskID: taskID, // 首次发现归本任务
		})
	} else {
		if prevStatus != p.StatusCode && prevStatus != "" {
			svc.PrevStatusCode = prevStatus
		}
		if prevLen != p.ContentLength && prevLen != 0 {
			svc.PrevContentLength = prevLen
		}
		config.DB.Model(&svc).Updates(map[string]interface{}{
			"protocol": p.Protocol, "service": p.Service, "banner": p.Banner,
			"prev_status_code": svc.PrevStatusCode, "prev_content_length": svc.PrevContentLength,
			"title": p.Title, "fingerprint": fpStr, "status_code": p.StatusCode,
			"prev_title": svc.PrevTitle, "prev_fingerprint": svc.PrevFingerprint, "title_source": titleSrc,
			"content_length": p.ContentLength, "page_simhash": fp,
			"last_seen": now, "asset_id": asset.ID, "miss_count": 0,
			"last_task_id": taskID, // 只更新「最近扫到」，不动首次归属
		})
	}

	// 内容变更检测：与「确认时的指纹」比对，而不是和台账标题比
	// 台账标题只用于人工识别，允许不准确；真正在意的是页面本身有没有变
	if fp != 0 && asset.ConfirmedSimhash != 0 && similarity.Changed(asset.ConfirmedSimhash, fp, 0) {
		config.DB.Model(&model.Asset{}).Where("id = ?", asset.ID).
			Update("content_changed", true)
		log.Printf("[变更] %s 页面内容发生变化（汉明距离 > %d）", ip, similarity.DefaultThreshold)
	}

	// 合规判定：扫描阶段只做「疑似/违规」标记（纯数据比对，不起连接、不扫指纹），
	// 真正的「确认违规」要等人打开合规页手动一键检测（实连确认）。
	CheckComplianceForService(svc, taskID)

	// AI 识别：web 服务扫到但「标题不清晰」（空 / nginx 默认页 / 欢迎页 / 纯 IP）时，
	// 异步走「规则→AI→浏览器渲染→AI」分级识别，结果写回 title/fingerprint（title_source=ai），
	// 同一任务的识别链路都归到一个会话里，方便在 AI 助手页回溯卡在哪一级。
	if strings.HasPrefix(strings.ToLower(p.Protocol), "http") && !TitleClear(p.Title) {
		EnqueueAIIdentify(taskID, ip, p.Port)
	}
}

// markMissingServices 本次扫描未发现的服务累计缺席，连续 3 次才剔除
func markMissingServices(targetIPs []string, results []scanner.ScanResult) {
	if len(targetIPs) == 0 {
		return
	}
	found := map[string]bool{}
	for _, r := range results {
		found[fmt.Sprintf("%s:%d", r.IP, r.Port)] = true
	}
	var svcs []model.Service
	config.DB.Where("ip IN ?", targetIPs).Find(&svcs)
	for _, s := range svcs {
		if found[fmt.Sprintf("%s:%d", s.IP, s.Port)] {
			continue
		}
		if s.MissCount+1 >= 3 {
			config.DB.Delete(&model.Service{}, s.ID)
		} else {
			config.DB.Model(&s).Update("miss_count", s.MissCount+1)
		}
	}
}

// ingestServicesLive 实时入库（扫描过程中增量 upsert 服务，不写快照）
func ingestServicesLive(results []scanner.ScanResult) {
	for _, r := range results {
		ingestService(r.IP, r, 0)
	}
}

// ingestResults 扫描结果入库：快照 + 服务 + 资产
func ingestResults(taskID uint, results []scanner.ScanResult) error {
	byIP := map[string][]scanner.ScanResult{}
	for _, r := range results {
		byIP[r.IP] = append(byIP[r.IP], r)
	}

	now := time.Now()
	for ip, ports := range byIP {
		// 快照
		openPorts, _ := json.Marshal(ports)
		snapshot := model.ScanSnapshot{
			TaskID: taskID, IP: ip, IsAlive: true, OpenPorts: string(openPorts), ScannedAt: now,
		}
		if err := config.DB.Create(&snapshot).Error; err != nil {
			return err
		}

		// 服务 upsert
		for _, p := range ports {
			ingestService(ip, p, taskID)
		}
	}
	return nil
}
