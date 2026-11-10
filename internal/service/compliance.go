package service

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"asset-platform/internal/config"
	"asset-platform/internal/model"

	"gorm.io/gorm"
)

// ruleKeyword 规则对应的指纹关键词
func ruleKeyword(rule model.ComplianceRule) string {
	name := strings.ToLower(rule.Name)
	keywords := []struct{ kw, match string }{
		{"ssh", "ssh"}, {"mysql", "mysql"}, {"redis", "redis"},
		{"postgres", "postgres"}, {"sql server", "sqlserver"}, {"rdp", "rdp"},
		{"mongodb", "mongo"}, {"elasticsearch", "elastic"},
		{"memcached", "memcache"}, {"zookeeper", "zookeeper"}, {"kafka", "kafka"},
	}
	for _, k := range keywords {
		if strings.Contains(name, k.kw) {
			return k.match
		}
	}
	return ""
}

// MatchRule 端口 或 指纹 任一命中即判违规
func MatchRule(rule model.ComplianceRule, svc model.Service) bool {
	// 端口命中即违规
	if svc.Port == rule.Port {
		return true
	}
	// 指纹命中即违规（端口不同但服务类型匹配，如 2222 端口的 ssh）
	kw := ruleKeyword(rule)
	if kw == "" {
		return false
	}
	hay := strings.ToLower(svc.Service + " " + svc.Banner + " " + svc.Fingerprint + " " + svc.Protocol)
	return strings.Contains(hay, kw)
}

// 合规状态三态
//   suspected 疑似违规：扫描阶段按端口/指纹命中规则就落库，未经实连确认
//   violation 违规    ：手动一键检测实连确认端口仍然开着
//   fixed     已整改  ：手动一键检测实连被拒（或原先的违规已确认关闭）
const (
	StatusSuspected = "suspected"
	StatusViolation = "violation"
	StatusFixed     = "fixed"
)

// 扫描阶段能给出的结论只有一档：**一律疑似**。
// 确认违规只能来自手动「一键检测」——那是对所有疑似目标做一次实连确认的扫描。
// （白名单不参与扫描阶段判定，只在手动检测时若不勾「忽略白名单」才生效。）
func scanStageStatus(ip string) string {
	return StatusSuspected
}

// CheckComplianceForService 扫描入库时判定：只用扫描已经拿到的数据，不发起任何网络探测。
// 注意：这里**不会**把 suspected 降级，也不会把已确认的 violation 降级；
// 只有 fixed 的记录重新被扫到，才回到 suspected（重新发现、待人工确认）。
func CheckComplianceForService(svc model.Service, taskID uint) {
	var rules []model.ComplianceRule
	config.DB.Where("enabled = true").Find(&rules)
	now := time.Now()
	want := scanStageStatus(svc.IP)
	for _, rule := range rules {
		if !MatchRule(rule, svc) {
			continue
		}
		var cr model.ComplianceResult
		err := config.DB.Where("ip = ? AND port = ? AND rule_id = ?", svc.IP, svc.Port, rule.ID).First(&cr).Error
		if err == gorm.ErrRecordNotFound {
			config.DB.Create(&model.ComplianceResult{
				AssetID: svc.AssetID, IP: svc.IP, Port: svc.Port, RuleID: &rule.ID,
				Status: want, DetectedAt: &now, LastSeen: &now,
				TaskID: taskID, LastTaskID: taskID,
			})
			continue
		}
		if err != nil {
			continue
		}
		updates := map[string]interface{}{"last_seen": now, "last_task_id": taskID}
		switch cr.Status {
		case StatusFixed:
			// 之前确认关闭过，这次扫描又出现 → 回到待确认
			updates["status"] = StatusSuspected
		case StatusSuspected:
			if want == StatusViolation {
				updates["status"] = StatusViolation // 非白名单的端口，扫描即可确认
			}
		}
		config.DB.Model(&cr).Updates(updates)
	}
}

// ListRules 合规规则列表
func ListRules() []model.ComplianceRule {
	var rules []model.ComplianceRule
	config.DB.Order("port").Find(&rules)
	return rules
}

// UpsertRule 新增/更新规则
func UpsertRule(r *model.ComplianceRule) error {
	if r.ID == 0 {
		return config.DB.Create(r).Error
	}
	return config.DB.Model(&model.ComplianceRule{}).Where("id = ?", r.ID).
		Updates(map[string]interface{}{"name": r.Name, "port": r.Port, "protocol": r.Protocol, "enabled": r.Enabled}).Error
}

// DeleteRule 删除规则
func DeleteRule(id uint) error {
	config.DB.Where("rule_id = ?", id).Delete(&model.ComplianceResult{})
	return config.DB.Delete(&model.ComplianceRule{}, id).Error
}

// ListWhitelist 堡垒机白名单
func ListWhitelist() []model.BastionWhitelist {
	var wl []model.BastionWhitelist
	config.DB.Order("ip").Find(&wl)
	return wl
}

// AddWhitelist 添加白名单
func AddWhitelist(ip, note string) error {
	return config.DB.Create(&model.BastionWhitelist{IP: ip, Note: note}).Error
}

// DeleteWhitelist 删除白名单
func DeleteWhitelist(id uint) error {
	return config.DB.Delete(&model.BastionWhitelist{}, id).Error
}

// ListComplianceResults 合规结果列表
func ListComplianceResults(status, keyword string, page, size int) ([]model.ComplianceResult, int64) {
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 200 {
		size = 20
	}
	q := config.DB.Model(&model.ComplianceResult{})
	if status != "" {
		q = q.Where("status = ?", status)
	}
	if keyword != "" {
		like := "%" + keyword + "%"
		// 只查表里真实存在的列。compliance_results 没有 rule_name/protocol/detail
		// （之前写了这三列 → SQL 报错被忽略 → 搜索永远返回空）
		// 规则名通过 rule_id 子查询匹配
		q = q.Where(`ip::text LIKE ? OR port::text LIKE ?
			OR rule_id IN (SELECT id FROM compliance_rules WHERE name ILIKE ?)`,
			like, like, like)
	}
	var total int64
	q.Count(&total)
	var results []model.ComplianceResult
	q.Order("id desc").Offset((page - 1) * size).Limit(size).Find(&results)
	return results, total
}

// ===== 堡垒机白名单（IP 或 CIDR，30 秒缓存）=====

var (
	wlMu     sync.RWMutex
	wlNets   []*net.IPNet
	wlIPs    []net.IP
	wlLoaded time.Time
)

func loadWhitelist() ([]*net.IPNet, []net.IP) {
	wlMu.RLock()
	if time.Since(wlLoaded) < 30*time.Second && (wlNets != nil || wlIPs != nil) {
		n, i := wlNets, wlIPs
		wlMu.RUnlock()
		return n, i
	}
	wlMu.RUnlock()

	wlMu.Lock()
	defer wlMu.Unlock()
	var list []model.BastionWhitelist
	config.DB.Find(&list)
	nets := make([]*net.IPNet, 0, len(list))
	ips := make([]net.IP, 0, len(list))
	for _, w := range list {
		s := strings.TrimSpace(w.IP)
		if s == "" {
			continue
		}
		if strings.Contains(s, "/") {
			if _, n, err := net.ParseCIDR(s); err == nil {
				nets = append(nets, n)
			}
			continue
		}
		if ip := net.ParseIP(s); ip != nil {
			ips = append(ips, ip)
		}
	}
	wlNets, wlIPs, wlLoaded = nets, ips, time.Now()
	return nets, ips
}

// InWhitelist 判断 IP 是否命中堡垒机白名单（支持单 IP 与 CIDR）
func InWhitelist(ipStr string) bool {
	ip := net.ParseIP(strings.TrimSpace(ipStr))
	if ip == nil {
		return false
	}
	nets, ips := loadWhitelist()
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	for _, x := range ips {
		if x.Equal(ip) {
			return true
		}
	}
	return false
}

// ===== 端口实连探测 =====

type probeOutcome int

const (
	probeOpen probeOutcome = iota
	probeClosed
	probeUnknown
)

// probePort 实连一次目标端口，返回结论 + 现场凭证。
// 准确性处理：
//   - 连接成功 = 端口确实开着（open）
//   - 连接被拒 = 重试一次再下结论，避免瞬时 RST / 半开连接导致误判"已整改"
//   - 超时/不可达 = 重试一次仍不行就记 uncertain，**不改状态**（防火墙 DROP、路由不通都属这类，
//     不能当成"端口已关"）
func probePort(ip string, port int, timeout time.Duration) (probeOutcome, string) {
	addr := net.JoinHostPort(ip, strconv.Itoa(port))
	var lastErr string
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			time.Sleep(400 * time.Millisecond)
		}
		conn, err := net.DialTimeout("tcp", addr, timeout)
		if err == nil {
			_ = conn.SetReadDeadline(time.Now().Add(1 * time.Second))
			buf := make([]byte, 256)
			n, _ := conn.Read(buf)
			ev := sanitizeBanner(buf[:n])
			conn.Close()
			if ev == "" {
				// RDP / MongoDB 这类协议不会主动吐 banner，握手成功本身就是证据
				ev = "TCP 连接成功（无 banner）"
			}
			return probeOpen, ev
		}
		lastErr = strings.ToLower(err.Error())
	}
	if strings.Contains(lastErr, "refused") {
		return probeClosed, "连接被拒绝（两次）"
	}
	return probeUnknown, "不可达：" + lastErr
}

// sanitizeBanner 只留可打印字符，压成一行，限制长度
func sanitizeBanner(b []byte) string {
	var sb strings.Builder
	for _, c := range b {
		if c >= 32 && c < 127 {
			sb.WriteByte(c)
		} else if c == '\r' || c == '\n' || c == '\t' {
			sb.WriteByte(' ')
		} else {
			sb.WriteByte('.')
		}
		if sb.Len() >= 160 {
			break
		}
	}
	return strings.TrimSpace(sb.String())
}

// ComplianceSummary 一次检测的统计
type ComplianceSummary struct {
	Checked      int `json:"checked"`      // 实际探测的端口数
	NewViolation int `json:"newViolation"` // 新发现的违规
	Confirmed    int `json:"confirmed"`    // 疑似 → 确认违规
	Reopened     int `json:"reopened"`     // 已整改又复现
	Fixed        int `json:"fixed"`        // 本次确认已关闭
	Uncertain    int `json:"uncertain"`    // 网络不可达，无法判定
}

type checkCandidate struct {
	IP      string
	Port    int
	RuleID  uint
	AssetID *uint
}

// RunComplianceCheck 一键检测（只有这个入口会写库）
// ids 非空 = 只检测选中的记录；ignoreWhitelist=true = 临时关闭白名单（手动检测默认如此）。
// 与旧实现的关键差别：不再依赖 services 表的"旧数据"下结论，而是对每个候选端口实连一次，
// 能连上才判违规，连接被拒才判已整改，超时/不可达保持原状态。
func RunComplianceCheck(ids []uint, ignoreWhitelist bool) (ComplianceSummary, error) {
	var sum ComplianceSummary
	var rules []model.ComplianceRule
	config.DB.Where("enabled = true").Find(&rules)
	if len(rules) == 0 {
		return sum, nil
	}
	ruleByID := make(map[uint]model.ComplianceRule, len(rules))
	for _, r := range rules {
		ruleByID[r.ID] = r
	}

	var cands []checkCandidate
	seen := map[string]bool{}
	key := func(ip string, port int, ruleID uint) string {
		return fmt.Sprintf("%s|%d|%d", ip, port, ruleID)
	}

	if len(ids) > 0 {
		// 只检测选中行：候选取自现有记录（含已整改的，重测可复现）
		var rows []model.ComplianceResult
		config.DB.Where("id IN ?", ids).Find(&rows)
		for _, r := range rows {
			if r.RuleID == nil {
				continue
			}
			if _, ok := ruleByID[*r.RuleID]; !ok {
				continue // 规则已停用/删除
			}
			k := key(r.IP, r.Port, *r.RuleID)
			if seen[k] {
				continue
			}
			seen[k] = true
			cands = append(cands, checkCandidate{IP: r.IP, Port: r.Port, RuleID: *r.RuleID, AssetID: r.AssetID})
		}
	} else {
		// 全量：从扫描结果（services）里挑候选，不重新扫描、不看指纹列，只按端口/指纹关键词匹配规则
		var svcs []model.Service
		config.DB.Find(&svcs)
		for _, rule := range rules {
			for _, svc := range svcs {
				if !MatchRule(rule, svc) {
					continue
				}
				if !ignoreWhitelist && InWhitelist(svc.IP) {
					continue
				}
				k := key(svc.IP, svc.Port, rule.ID)
				if seen[k] {
					continue
				}
				seen[k] = true
				cands = append(cands, checkCandidate{IP: svc.IP, Port: svc.Port, RuleID: rule.ID, AssetID: svc.AssetID})
			}
		}
		// 已有的「疑似」记录也要一并实连确认——它们可能是服务表里已经消失的端口，
		// 只从 services 取候选会让它们永远停在疑似。
		var pend []model.ComplianceResult
		config.DB.Where("status = ?", StatusSuspected).Find(&pend)
		for _, r := range pend {
			if r.RuleID == nil {
				continue
			}
			if _, ok := ruleByID[*r.RuleID]; !ok {
				continue
			}
			if !ignoreWhitelist && InWhitelist(r.IP) {
				continue
			}
			k := key(r.IP, r.Port, *r.RuleID)
			if seen[k] {
				continue
			}
			seen[k] = true
			cands = append(cands, checkCandidate{IP: r.IP, Port: r.Port, RuleID: *r.RuleID, AssetID: r.AssetID})
		}
	}
	if len(cands) == 0 {
		return sum, nil
	}

	// 并发实连（64 路），结果统一回主协程写库
	type probeRes struct {
		c        checkCandidate
		outcome  probeOutcome
		evidence string
	}
	sem := make(chan struct{}, 64)
	outCh := make(chan probeRes, len(cands))
	var wg sync.WaitGroup
	for _, c := range cands {
		wg.Add(1)
		go func(c checkCandidate) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			o, ev := probePort(c.IP, c.Port, 3*time.Second)
			outCh <- probeRes{c: c, outcome: o, evidence: ev}
		}(c)
	}
	wg.Wait()
	close(outCh)

	now := time.Now()
	for r := range outCh {
		sum.Checked++
		var cr model.ComplianceResult
		err := config.DB.Where("ip = ? AND port = ? AND rule_id = ?", r.c.IP, r.c.Port, r.c.RuleID).First(&cr).Error
		switch r.outcome {
		case probeOpen:
			// 实连确认端口仍开着 → 一律升为「违规」（疑似被确认/已整改又复现都算）
			if err == gorm.ErrRecordNotFound {
				config.DB.Create(&model.ComplianceResult{
					AssetID: r.c.AssetID, IP: r.c.IP, Port: r.c.Port, RuleID: &r.c.RuleID,
					Status: StatusViolation, DetectedAt: &now, LastSeen: &now, Evidence: r.evidence,
				})
				sum.NewViolation++
			} else if err == nil {
				was := cr.Status
				config.DB.Model(&cr).Updates(map[string]interface{}{
					"status": StatusViolation, "last_seen": now, "evidence": r.evidence, "fixed_at": nil,
				})
				switch was {
				case StatusSuspected:
					sum.Confirmed++
				case StatusFixed:
					sum.Reopened++
				}
			}
		case probeClosed:
			// 两次实连都被拒 → 端口确实关了；疑似和已违规都收敛为「已整改」
			if err == nil && cr.Status != StatusFixed {
				config.DB.Model(&cr).Updates(map[string]interface{}{
					"status": StatusFixed, "fixed_at": now, "evidence": r.evidence,
				})
				sum.Fixed++
			}
		default:
			sum.Uncertain++ // 超时/不可达：保持原状态，不误判
		}
	}
	return sum, nil
}

