package config

import (
	"os"
)

// Config 全局配置，从环境变量读取
type Config struct {
	ServerPort      string // 服务端口
	PGDSN           string // PostgreSQL 连接串
	RedisAddr       string // Redis 地址
	RedisPass       string // Redis 密码
	DataDir         string // 数据目录（上传/快照图片）
	TscanClientPath string // TscanClient 二进制绝对路径（本地扫描用）
}

func Load() *Config {
	return &Config{
		ServerPort:      getEnv("SERVER_PORT", "18888"),
		PGDSN:           getEnv("PG_DSN", "postgres://asset:asset123@127.0.0.1:5432/asset_platform?sslmode=disable"),
		RedisAddr:       getEnv("REDIS_ADDR", "127.0.0.1:6379"),
		RedisPass:       getEnv("REDIS_PASSWORD", ""),
		DataDir:         getEnv("DATA_DIR", "./data"),
		TscanClientPath: getEnv("TSCANCLIENT_PATH", ""),
	}
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
