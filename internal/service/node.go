package service

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"errors"
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
func DeleteNode(id uint) error {
	return config.DB.Delete(&model.Node{}, id).Error
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
