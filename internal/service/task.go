package service

import (
	"log"
	"encoding/json"
	"fmt"
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
	Finger     string
	Thread     int
	Timeout    int
	NoPing     bool
	NoSmart    bool
	ExtraArgs  string
	NodeIDs    string
	BatchSize  int    // 每批下发的 IP 数（0=默认 100）
}

// CreateTask 创建扫描任务
func CreateTask(in TaskInput) (*model.ScanTask, error) {
	task := model.ScanTask{
		Name: in.Name, IPRanges: in.IPRanges, PortMode: in.PortMode, Ports: in.Ports,
		Cron: in.Cron, Status: "pending", TemplateID: in.TemplateID,
		Modules: in.Modules, Finger: in.Finger,
		Thread: in.Thread, Timeout: in.Timeout, NoPing: in.NoPing, NoSmart: in.NoSmart,
		ExtraArgs: in.ExtraArgs, NodeIDs: in.NodeIDs, BatchSize: in.BatchSize,
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
	return config.DB.Delete(&model.ScanTask{}, id).Error
}

// removeTaskReports 删除任务关联的报告文件（含同名前缀历史文件）
func removeTaskReports(reportPath, projectID string) {
	if reportPath != "" {
		_ = os.Remove(reportPath)
	}
	workDir := filepath.Dir(scanner.TscanClientPath)
	if projectID != "" {
		matches, _ := filepath.Glob(filepath.Join(workDir, projectID+"-*.html"))
		for _, m := range matches {
			_ = os.Remove(m)
		}
	}
}

// findTaskReport 找任务本次生成的 HTML 报告（取最新）
func findTaskReport(projectID string) string {
	workDir := filepath.Dir(scanner.TscanClientPath)
	matches, _ := filepath.Glob(filepath.Join(workDir, projectID+"-*.html"))
	if len(matches) == 0 {
		return ""
	}
	sort.Strings(matches)
	return matches[len(matches)-1]
}

// CleanOldReports 清理 N 天前的扫描报告文件，返回删除数量
func CleanOldReports(days int) int {
	workDir := filepath.Dir(scanner.TscanClientPath)
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

// ExecuteTask 执行扫描任务：调 TscanClient → 结果入库（快照/服务/资产）
// StopTask 停止任务：本机执行则杀进程，节点分片则标记取消
func StopTask(taskID uint) error {
	var task model.ScanTask
	if err := config.DB.First(&task, taskID).Error; err != nil {
		return err
	}
	scanner.StopScan(taskProjectID(task))
	config.DB.Model(&model.AgentTask{}).
		Where("task_id = ? AND status IN ?", taskID, []string{"pending", "running"}).
		Updates(map[string]interface{}{"status": "cancelled", "message": "已手动停止"})
	now := time.Now()
	return config.DB.Model(&task).Updates(map[string]interface{}{
		"status": "stopped", "finished_at": now,
	}).Error
}

func ExecuteTask(taskID uint) error {
	var task model.ScanTask
	if err := config.DB.First(&task, taskID).Error; err != nil {
		return err
	}
	now := time.Now()
	config.DB.Model(&task).Updates(map[string]interface{}{"status": "running", "started_at": now})
	tasklog.Clear(task.ID)
	targets := parseTargets(task.IPRanges)
	tasklog.AppendTS(task.ID, "[本机] 收到任务「%s」，共 %d 个目标段，开始执行", task.Name, len(targets))
	ports := task.Ports
	switch task.PortMode {
	case "top100":
		ports = "" // TscanClient 默认 Top100
	case "all":
		ports = "1-65535"
	}

	opts := scanner.ScanOptions{
		ProjectID: taskProjectID(task),
		Modules:   task.Modules, Finger: task.Finger,
		Ports:     ports, Thread: task.Thread, Timeout: task.Timeout,
		NoPing: task.NoPing, NoSmart: task.NoSmart,
		ExtraArgs: task.ExtraArgs,
	}
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
		config.DB.Model(&task).Updates(map[string]interface{}{"status": "failed", "finished_at": time.Now()})
		return err
	}
	tasklog.SetProgress(task.ID, "") // 扫描结束清空进度条
	if err := ingestResults(task.ID, results); err != nil {
		config.DB.Model(&task).Updates(map[string]interface{}{"status": "failed", "finished_at": time.Now()})
		return err
	}
	// 连续 3 次未发现才剔除 + 自动合规检测
	expanded := target.Resolve(targets, ExcludeRangeStrings())
	markMissingServices(expanded, results)
	if nv, fixed, err := RunComplianceCheck(); err == nil {
		tasklog.Append(task.ID, fmt.Sprintf("[合规] 检测完成，新增违规 %d 条，已整改 %d 条", nv, fixed))
	}
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
func ingestService(ip string, p scanner.ScanResult) {
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

	if err == gorm.ErrRecordNotFound {
		config.DB.Create(&model.Service{
			AssetID: &asset.ID, IP: ip, Port: p.Port, Protocol: p.Protocol,
			Service: p.Service, Banner: p.Banner, Title: p.Title, Fingerprint: fpStr,
			StatusCode: p.StatusCode, ContentLength: p.ContentLength, PageSimhash: fp,
			FirstSeen: &now, LastSeen: &now,
		})
	} else {
		config.DB.Model(&svc).Updates(map[string]interface{}{
			"protocol": p.Protocol, "service": p.Service, "banner": p.Banner,
			"title": p.Title, "fingerprint": fpStr, "status_code": p.StatusCode,
			"content_length": p.ContentLength, "page_simhash": fp,
			"last_seen": now, "asset_id": asset.ID, "miss_count": 0,
		})
	}

	// 内容变更检测：与「确认时的指纹」比对，而不是和台账标题比
	// 台账标题只用于人工识别，允许不准确；真正在意的是页面本身有没有变
	if fp != 0 && asset.ConfirmedSimhash != 0 && similarity.Changed(asset.ConfirmedSimhash, fp, 0) {
		config.DB.Model(&model.Asset{}).Where("id = ?", asset.ID).
			Update("content_changed", true)
		log.Printf("[变更] %s 页面内容发生变化（汉明距离 > %d）", ip, similarity.DefaultThreshold)
	}

	// 实时合规检测（端口 + 指纹双重判定），发现即入库
	config.DB.Where("ip = ? AND port = ?", ip, p.Port).First(&svc)
	CheckComplianceForService(svc)
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
		ingestService(r.IP, r)
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
			ingestService(ip, p)
		}
	}
	return nil
}
