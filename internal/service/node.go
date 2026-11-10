package service

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"errors"
	"log"
	"time"

	"asset-platform/internal/config"
	"asset-platform/internal/model"
	"asset-platform/internal/scanner"

	"gorm.io/gorm"
)

// RegisterNode 节点注册（自动加入）
func RegisterNode(name, ip, joinToken, version string) (*model.Node, string, error) {
	// 每次注册都生成新的节点密钥，随响应下发给 agent，后续请求用它认证
	nodeToken := randomHex(32)

	var node model.Node
	err := config.DB.Where("name = ?", name).First(&node).Error
	now := time.Now()
	if err == gorm.ErrRecordNotFound {
		node = model.Node{Name: name, IP: ip, Token: nodeToken, Status: "online", LastHeartbeat: &now}
		if err := config.DB.Create(&node).Error; err != nil {
			return nil, "", err
		}
		return &node, nodeToken, nil
	}
	if err != nil {
		return nil, "", err
	}
	updates := map[string]interface{}{
		"ip": ip, "status": "online", "last_heartbeat": now, "token": nodeToken,
		// 重新注册 = 全新安装，清掉卸载标记（否则新 agent 心跳一读就被自己卸载，
		// 也会让"延迟删除"的任务误删刚装好的节点）
		"uninstall_requested": false,
	}
	// agent 重启后上报的版本与中心一致，说明升级已完成，清除标记避免反复下发
	if version != "" && version == config.LatestAgentVersion {
		updates["upgrade_requested"] = false
	}
	config.DB.Model(&node).Updates(updates)
	node.Token = nodeToken
	return &node, nodeToken, nil
}

// randomHex 生成 n 字节随机数的十六进制串
func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// Heartbeat 节点心跳
func Heartbeat(id uint) error {
	now := time.Now()
	return config.DB.Model(&model.Node{}).Where("id = ?", id).
		Updates(map[string]interface{}{"status": "online", "last_heartbeat": now}).Error
}

// ListNodes 节点列表
func ListNodes() []model.Node {
	var nodes []model.Node
	config.DB.Order("id").Find(&nodes)
	// 超时未心跳的标记离线
	offline := time.Now().Add(-2 * time.Minute)
	for i := range nodes {
		if nodes[i].LastHeartbeat == nil || nodes[i].LastHeartbeat.Before(offline) {
			nodes[i].Status = "offline"
		}
	}
	return nodes
}

// DeleteNode 删除节点
// 顺序很重要：先下发卸载指令（uninstall_requested=true），等 agent 心跳取到后自清理退出，
// 再删数据库行。直接删行会变成：agent 心跳 404 → 清掉本地凭据退出 → 计划任务把进程拉起来 →
// 用已消费的一次性令牌重新注册失败 → 节点上永久残留进程和计划任务。
// 返回 uninstallFirst=true 表示"在线节点，已下发卸载指令，稍后自动删除"。
func DeleteNode(id uint) (uninstallFirst bool, err error) {
	var node model.Node
	if err := config.DB.First(&node, id).Error; err != nil {
		return false, err
	}
	// 已离线（2 分钟无心跳）：agent 大概率不在了，直接删行
	if node.LastHeartbeat == nil || node.LastHeartbeat.Before(time.Now().Add(-2*time.Minute)) {
		return false, config.DB.Delete(&model.Node{}, id).Error
	}
	// 在线：先置卸载标记，agent 下次心跳读到后会自杀（停进程 + 删任务 + 删自己的文件）
	if err := config.DB.Model(&model.Node{}).Where("id = ?", id).
		Update("uninstall_requested", true).Error; err != nil {
		return true, err
	}
	go removeAfterUninstall(id)
	return true, nil
}

// removeAfterUninstall 等 agent 自清理退出后再删节点行。
// agent 每 30 秒心跳一次，正常 1 分钟内就会读到标记并退出；
// 心跳停 40 秒以上视为已退出。最多等 3 分钟，超时也删（避免残留死节点）。
func removeAfterUninstall(id uint) {
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		time.Sleep(10 * time.Second)
		var node model.Node
		if err := config.DB.First(&node, id).Error; err != nil {
			return // 行已不存在
		}
		if !node.UninstallReq {
			log.Printf("[node] 节点 %d 重新注册，取消延迟删除", id)
			return // 期间被重新注册（RegisterNode 会清掉标记），不删
		}
		if node.LastHeartbeat == nil || node.LastHeartbeat.Before(time.Now().Add(-40*time.Second)) {
			break // agent 已退出
		}
	}
	var node model.Node
	if err := config.DB.First(&node, id).Error; err != nil {
		return
	}
	if !node.UninstallReq {
		return
	}
	config.DB.Delete(&model.Node{}, id)
	log.Printf("[node] 节点 %d 卸载完成，已从中心移除", id)
}

// GetNode 获取节点
func GetNode(id uint) (*model.Node, error) {
	var node model.Node
	if err := config.DB.First(&node, id).Error; err != nil {
		return nil, err
	}
	return &node, nil
}

// VerifyNodeToken 校验节点 token（agent 上报结果时鉴权）
func VerifyNodeToken(id uint, token string) error {
	var node model.Node
	if err := config.DB.First(&node, id).Error; err != nil {
		return errors.New("节点不存在")
	}
	if node.Token != token {
		return errors.New("token 无效")
	}
	return nil
}

// GetNodeTasks 获取分配给节点的待执行任务
func GetNodeTasks(nodeID uint) []model.ScanTask {
	var tasks []model.ScanTask
	config.DB.Where("node_id = ? AND status = 'pending'", nodeID).Order("id").Find(&tasks)
	return tasks
}

// ReportNodeResults 节点上报扫描结果并入库
// 注意：这里只入库，不修改任务状态。agent 是分批回传的，若在此标记 done 会导致
// 主任务在第一批结果到达时就"完成"，而此时扫描还在继续。任务完成由 CheckTaskAllDone 判定。
func ReportNodeResults(taskID uint, results []scanner.ScanResult) error {
	return ingestResults(taskID, results)
}
