package version

// Version 全局版本号（数字型，如 0.0.1）
// 发布时改这里（或用 -ldflags "-X asset-platform/internal/version.Version=x.y.z" 注入）
var Version = "0.0.6"

// BuildTime 构建时间（可用 -ldflags 注入）
var BuildTime = "dev"

// Commit 构建提交号（可用 -ldflags 注入）
var Commit = "dev"
