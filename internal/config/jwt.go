package config

import (
	"crypto/rand"
	"encoding/hex"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

var (
	jwtOnce   sync.Once
	jwtSecret string
)

// JWTSecret JWT 签名密钥
// 优先级：环境变量 JWT_SECRET > /app/data/jwt.secret（首次启动随机生成）> 临时随机
// 绝不回退到硬编码字符串——否则任何人可以伪造 admin token
func JWTSecret() string {
	jwtOnce.Do(func() {
		if v := strings.TrimSpace(os.Getenv("JWT_SECRET")); v != "" {
			if len(v) < 16 {
				log.Println("[安全警告] JWT_SECRET 长度不足 16 位，建议换成长随机串")
			}
			jwtSecret = v
			return
		}
		path := "/app/data/jwt.secret"
		if os.Getenv("JWT_SECRET_FILE") != "" {
			path = os.Getenv("JWT_SECRET_FILE")
		}
		if b, err := os.ReadFile(path); err == nil {
			if s := strings.TrimSpace(string(b)); len(s) >= 16 {
				jwtSecret = s
				return
			}
		}
		buf := make([]byte, 32)
		if _, err := rand.Read(buf); err != nil {
			log.Fatalf("[安全] 生成 JWT 密钥失败: %v", err)
		}
		jwtSecret = hex.EncodeToString(buf)
		_ = os.MkdirAll(filepath.Dir(path), 0o755)
		if err := os.WriteFile(path, []byte(jwtSecret), 0o600); err != nil {
			log.Printf("[安全警告] JWT 密钥无法持久化（重启后 token 会失效）: %v", err)
		} else {
			log.Printf("[安全] 已生成并保存 JWT 密钥到 %s", path)
		}
	})
	return jwtSecret
}

// StrictAgentAuth 是否强制 agent 携带节点凭据
// 默认强制；仅当显式设置 AGENT_AUTH_STRICT=0 时放行无凭据请求（兼容极旧的 agent）
func StrictAgentAuth() bool {
	return os.Getenv("AGENT_AUTH_STRICT") != "0"
}
