package config

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log"
	"os"
	"sync"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// DB 全局数据库连接
var DB *gorm.DB

// LatestAgentVersion 当前服务端内置的 agent 控制文件版本（二进制内容 SHA256 前 8 位）
var LatestAgentVersion = "unknown"

// AgentBinaryPath agent 二进制在容器内的路径（amd64，兼容历史命名）
const AgentBinaryPath = "/app/agent/scope-agent-linux"

// agentVerCache 按架构缓存指纹，避免每次心跳都读 11MB 文件算哈希
var agentVerCache sync.Map // arch -> string

// ComputeAgentVersion 默认架构（amd64）的控制文件指纹。
func ComputeAgentVersion() string { return AgentVersionFor("amd64") }

// AgentVersionFor 按架构返回控制文件指纹（二进制内容 SHA256 前 8 位）。
//
// 为什么用内容指纹而不是版本号：版本号是平台版本，任何人改一行 server 代码、
// 发一个补丁版本，重新构建出来的 agent 二进制里都带着新版本串，内容一变大，
// 全集群节点就被判定"需要升级"，白下几十 MB 再重启一轮。而 agent 代码根本
// 没动。只有内容指纹能做到"agent 真变了才升级"。
func AgentVersionFor(arch string) string {
	if arch == "" {
		arch = "amd64"
	}
	if v, ok := agentVerCache.Load(arch); ok {
		return v.(string)
	}
	v := binaryFingerprint(agentBinaryPathFor(arch))
	agentVerCache.Store(arch, v)
	return v
}

// agentBinaryPathFor 与 api.agentBinaryName 保持同一套命名，不反向依赖 api 包。
func agentBinaryPathFor(arch string) string {
	if arch == "windows" {
		return "/app/agent/scope-agent-windows.exe"
	}
	if arch == "amd64" {
		// 兼容历史命名（老镜像里没有架构后缀）
		if fi, err := os.Stat("/app/agent/scope-agent-linux-amd64"); err == nil && !fi.IsDir() {
			return "/app/agent/scope-agent-linux-amd64"
		}
		return AgentBinaryPath
	}
	return "/app/agent/scope-agent-linux-" + arch
}

// binaryFingerprint 读文件算 SHA256 前 8 位；文件不存在时返回 unknown。
func binaryFingerprint(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return "unknown"
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "unknown"
	}
	return hex.EncodeToString(h.Sum(nil))[:8]
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
	// 连接池配置（防止 database is closed）
	sqlDB, err := DB.DB()
	if err == nil {
		sqlDB.SetMaxIdleConns(10)
		sqlDB.SetMaxOpenConns(100)
		sqlDB.SetConnMaxLifetime(time.Hour)
	}
	log.Printf("[db] PostgreSQL 连接成功")
	return nil
}
