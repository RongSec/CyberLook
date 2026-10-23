package config

import (
	"asset-platform/internal/version"
	"log"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// DB 全局数据库连接
var DB *gorm.DB

// LatestAgentVersion 当前服务端内置的 agent 控制文件版本（二进制 SHA256 前 8 位）
var LatestAgentVersion = "unknown"

// AgentBinaryPath agent 二进制在容器内的路径
const AgentBinaryPath = "/app/agent/scope-agent-linux"

// ComputeAgentVersion 计算磁盘上 agent 二进制的版本号
func ComputeAgentVersion() string {
	// 版本号用数字型（0.0.1），与 agent 的 version.Version 保持一致
	return version.Version
}

// InitDB 初始化 PostgreSQL 连接
func InitDB(dsn string) error {
	var err error
	DB, err = gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Warn),
	})
	if err != nil {
		return err
	}
	log.Printf("[db] PostgreSQL 连接成功")
	return nil
}
