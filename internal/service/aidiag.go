package service

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"asset-platform/internal/config"
	"asset-platform/internal/llm"
	"asset-platform/internal/model"
	"asset-platform/internal/tasklog"

	"gorm.io/gorm"
)

// 分片 AI 诊断结论
const (
	ShardAIDone    = "done"    // 日志显示扫描已完成，只是没收到结束信号
	ShardAIStalled = "stalled" // 真卡死（日志不再输出，也没有完成迹象）
)

// AIScanMonitorEnabled 扫描监控开关（LLM 设置页可关）
func AIScanMonitorEnabled() bool {
	var s model.AISettings
	if err := config.DB.First(&s, 1).Error; err != nil {
		return true
	}
	return s.ScanMonitor
}

// AIServiceIdentifyEnabled 服务识别开关
func AIServiceIdentifyEnabled() bool {
	var s model.AISettings
	if err := config.DB.First(&s, 1).Error; err != nil {
		return true
	}
	return s.ServiceIdentify
}

// DiagnoseShard 看该分片所属节点的终端日志，判断「已完成但没结束」还是「真卡死」，
// 并据此收尾（done_ai）或回收重派（stalled_ai）。
// trigger 仅用于日志标注（watchdog / 手动）。
func DiagnoseShard(at model.AgentTask, trigger string) (verdict, note string, err error) {
	var node model.Node
	_ = config.DB.First(&node, at.NodeID).Error
	// 优先用「本分片」的终端回显（节点日志是累积的，混着几百个分片）
	srcLog := node.TerminalLog
	if strings.TrimSpace(at.TerminalLog) != "" {
		srcLog = at.TerminalLog
	}
	logText := tailRunes(srcLog, 4000)

	// 1) 规则先判，能确定就不花 token。
	//    优先用「本分片」日志：节点日志是累积的，里面残留着别的分片的「扫描结束」，
	//    曾导致 66% 进度的分片被直接判成"已完成"（分片 #632）。
	verdict, note = heuristicVerdict(at, srcLog)

	// 2) 规则判不了再让模型看日志
	if verdict == "" && len(llm.AvailableProfiles()) > 0 && strings.TrimSpace(logText) != "" {
		if v, why, e := askAIDiagnose(at, node, logText); e == nil && v != "" {
			verdict, note = v, "AI："+why
		}
	}
	// 模型也不可用就别瞎判「完成」，按中断处理（IP 会被重派，最多多扫一遍）
	if verdict == "" {
		verdict, note = ShardAIStalled, "无完成迹象，模型不可用"
	}

	now := time.Now()
	config.DB.Model(&model.AgentTask{}).Where("id = ?", at.ID).Updates(map[string]interface{}{
		"ai_verdict": verdict, "ai_verdict_note": note, "ai_verdict_at": now,
	})

	if verdict == ShardAIDone {
		res := config.DB.Model(&model.AgentTask{}).Where("id = ? AND status = ?", at.ID, "running").
			Updates(map[string]interface{}{
				"status": "done_ai", "progress": 100, "scanned": gorm.Expr("total"),
				"finished_at": now, "message": "AI完成",
			})
		// 状态已被别人改过（重复诊断/已由 agent 正常收尾）：不要再写日志、不要再回收 IP，
		// 否则同一批 IP 会被重复放回待扫池 → 重复扫、日志刷屏（曾见 #624 一秒内写 7 条）
		if res.RowsAffected == 0 {
			return verdict, note, nil
		}
		tasklog.AppendTS(at.TaskID, "[AI诊断] 分片 #%d（%s）判定为「已完成但未结束」→ 标记 AI完成（%s）",
			at.ID, at.NodeName, padNote(note))
		shardActDrop(at.ID)
		return verdict, note, nil
	}

	res := config.DB.Model(&model.AgentTask{}).Where("id = ? AND status = ?", at.ID, "running").
		Updates(map[string]interface{}{
			"status": "stalled_ai", "finished_at": now, "message": "AI中断",
			"retry_count": gorm.Expr("retry_count + 1"),
		})
	if res.RowsAffected == 0 {
		return verdict, note, nil
	}
	// 回收次数到上限：不再往池子里塞回去，否则同一批 IP 会被无限循环重派
	// （任务 #115 就是这样连着被判 19 次、进度一直卡在 26%）
	if at.RetryCount+1 >= shardMaxRetry {
		status := "failed"
		msg := "多次回收仍停滞，放弃重派"
		if at.Total > 0 && at.Scanned*100/at.Total >= shardStallAcceptRatio {
			status = "done_ai"
			msg = "多次回收仍停滞，按已完成收尾"
		}
		config.DB.Model(&model.AgentTask{}).Where("id = ?", at.ID).Updates(map[string]interface{}{
			"status": status, "message": msg,
		})
		tasklog.AppendTS(at.TaskID, "[AI诊断] 分片 #%d（%s）已回收 %d 次仍停滞 → %s，不再重派（%s）",
			at.ID, at.NodeName, at.RetryCount+1, msg, padNote(note))
		shardActDrop(at.ID)
		return verdict, note, nil
	}
	reclaimShardIPs(at.TaskID, at.IPRanges)
	tasklog.AppendTS(at.TaskID, "[AI诊断] 分片 #%d（%s）判定为「卡死中断」→ 标记 AI中断，IP 收回重派（%s）",
		at.ID, at.NodeName, padNote(note))
	shardActDrop(at.ID)
	return verdict, note, nil
}

// heuristicVerdict 规则判定，只返回**能确定**的结论；判不了返回空串交给模型。
// 关键：进度不够时绝不认「完成标识」——日志里可能残留别的分片的结束标记。
func heuristicVerdict(at model.AgentTask, logText string) (string, string) {
	pct := 0
	if at.Total > 0 {
		pct = at.Scanned * 100 / at.Total
	}
	if pct < 95 {
		return "", ""
	}
	low := strings.ToLower(logText)
	// TscanClient 收尾时会有这些行
	for _, k := range []string{"扫描完成", "扫描结束", "scan finished", "all done"} {
		if strings.Contains(low, strings.ToLower(k)) {
			return ShardAIDone, fmt.Sprintf("进度 %d%%，日志出现完成标识：%s", pct, k)
		}
	}
	// 每个 IP 都打完「Ip扫描结束」也算结束
	if at.IPRanges != "" {
		ips := len(strings.Split(at.IPRanges, ","))
		if ips > 0 && strings.Count(logText, "Ip扫描结束") >= ips {
			return ShardAIDone, fmt.Sprintf("进度 %d%%，全部 %d 个 IP 均有结束标识", pct, ips)
		}
	}
	return ShardAIDone, fmt.Sprintf("端口已扫 %d%%", pct)
}

func askAIDiagnose(at model.AgentTask, node model.Node, logText string) (string, string, error) {
	prompt := fmt.Sprintf(`下面是一个端口扫描分片在节点上的终端日志。请判断它属于哪种情况，只输出 JSON：
{"verdict":"done 或 stalled","reason":"一句话理由"}
判定标准：
- done：日志显示扫描已经跑完（出现完成提示、所有 IP 都有结束标识、进度到 100%%），只是控制端没收到结束信号；
- stalled：扫描卡住/中断（日志长时间没有任何新输出，也没有完成迹象）。
分片信息：IP 数 %d，已扫端口 %d / %d。
最近日志：
%s`, countIPs(at.IPRanges), at.Scanned, at.Total, logText)

	req := llm.ChatRequest{
		Messages: []llm.Message{
			{Role: "system", Content: "你是扫描任务诊断助手，只输出 JSON。"},
			{Role: "user", Content: prompt},
		},
		Temperature: 0.1,
		MaxTokens:   200,
	}
	res, err := llm.ChatWithFailover(req, 0)
	if err != nil || res == nil || res.Resp == nil || len(res.Resp.Choices) == 0 {
		return "", "", err
	}
	raw := strings.TrimSpace(res.Resp.Choices[0].Message.Content)
	var out struct {
		Verdict string `json:"verdict"`
		Reason  string `json:"reason"`
	}
	if e := json.Unmarshal([]byte(extractJSON(raw)), &out); e != nil {
		return "", "", e
	}
	v := strings.ToLower(strings.TrimSpace(out.Verdict))
	if v != ShardAIDone && v != ShardAIStalled {
		return "", "", fmt.Errorf("模型返回无法识别的结论: %s", raw)
	}
	return v, out.Reason, nil
}

func countIPs(ranges string) int {
	if strings.TrimSpace(ranges) == "" {
		return 0
	}
	return len(strings.Split(ranges, ","))
}

func tailRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return "…" + string(r[len(r)-n:])
}

func padNote(s string) string {
	if len([]rune(s)) > 60 {
		return string([]rune(s)[:60]) + "…"
	}
	return s
}
