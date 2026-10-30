package service

import (
	"asset-platform/internal/version"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"asset-platform/internal/config"
	"asset-platform/internal/model"
)

// UpdateInfo GitHub Release 信息
type UpdateInfo struct {
	Version     string `json:"version"`     // tag_name
	DownloadURL string `json:"downloadUrl"` // 服务端二进制的下载地址
	Notes       string `json:"notes"`       // release body
	PublishedAt string `json:"publishedAt"`
	Current     string `json:"current"`     // 当前版本号（如 0.0.6）
	HasUpdate   bool   `json:"hasUpdate"`
	// 发布方式：image=只有容器镜像（走 updater 边车）/ binary=带二进制附件（下载替换）
	ReleaseKind string `json:"releaseKind"`
}

// getHTTPClient 带代理的 HTTP 客户端（代理仅用于获取更新）
func getHTTPClient() *http.Client {
	var s model.SystemSettings
	config.DB.First(&s, 1)
	transport := &http.Transport{}
	if s.Proxy != "" {
		if u, err := url.Parse(s.Proxy); err == nil {
			transport.Proxy = http.ProxyURL(u)
		}
	}
	return &http.Client{Timeout: 60 * time.Second, Transport: transport}
}

// CurrentServerHash 当前服务端二进制的哈希
func CurrentServerHash() string {
	// 版本号用数字型（0.0.1）
	return version.Version
}

// ServerBinaryPath 服务端二进制路径
const ServerBinaryPath = "/app/asset-server"

// BackupDir 备份目录
const BackupDir = "/app/backup"

// CheckUpdate 检查 GitHub 最新 Release
func CheckUpdate() (*UpdateInfo, error) {
	var s model.SystemSettings
	config.DB.First(&s, 1)
	if s.GithubRepo == "" {
		return nil, fmt.Errorf("未配置 GitHub 仓库（形如 owner/repo）")
	}
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", s.GithubRepo)
	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "weijian-asset-platform-updater")
	if s.GithubToken != "" {
		req.Header.Set("Authorization", "Bearer "+s.GithubToken)
	}
	resp, err := getHTTPClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("访问 GitHub 失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		msg := strings.TrimSpace(string(b))
		if msg == "" {
			msg = resp.Status
		}
		if resp.StatusCode == 403 {
			return nil, fmt.Errorf("GitHub 拒绝访问(403)：%s\n提示：未登录调用限 60 次/小时，建议在下方填写 GitHub Token（仅需 public_repo 读权限）", msg)
		}
		if resp.StatusCode == 404 {
			return nil, fmt.Errorf("仓库或 Release 不存在(404)：请确认仓库为 %s 且已发布 Release", s.GithubRepo)
		}
		return nil, fmt.Errorf("GitHub 返回 %d：%s", resp.StatusCode, msg)
	}

	var rel struct {
		TagName string `json:"tag_name"`
		Body    string `json:"body"`
		HTMLURL string `json:"html_url"`
		Assets  []struct {
			Name        string `json:"name"`
			BrowserURL  string `json:"browser_download_url"`
			DownloadURL string `json:"url"` // API 下载地址（带 token）
		} `json:"assets"`
		PublishedAt string `json:"published_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, err
	}

	// 找服务端二进制（名字含 server 或 asset-server）
	downloadURL := ""
	for _, a := range rel.Assets {
		n := strings.ToLower(a.Name)
		if strings.Contains(n, "server") && (strings.Contains(n, "linux") || !strings.Contains(n, "windows")) {
			downloadURL = a.BrowserURL
			if downloadURL == "" {
				downloadURL = a.DownloadURL
			}
			break
		}
	}

	info := &UpdateInfo{
		Version:     rel.TagName,
		DownloadURL: downloadURL,
		Notes:       rel.Body,
		PublishedAt: rel.PublishedAt,
		Current:     version.Version,
	}
	// 有二进制附件 → 老式二进制更新；否则走 updater 边车拉镜像
	if downloadURL != "" {
		info.ReleaseKind = "binary"
	} else {
		info.ReleaseKind = "image"
	}
	// 是否可更新：按版本号比较（镜像发布没有附件，不能再用 downloadURL 判断）
	// 开发版（dev）不触发自动更新
	if isDevVersion(version.Version) || isDevVersion(rel.TagName) {
		info.HasUpdate = false
	} else {
		info.HasUpdate = CompareVersion(strings.TrimPrefix(rel.TagName, "v"), version.Version) > 0
	}
	return info, nil
}

// ApplyUpdate 下载新二进制 → 备份旧的 → 替换 → 触发重启
func ApplyUpdate(downloadURL string) error {
	if downloadURL == "" {
		return fmt.Errorf("下载地址为空")
	}
	var s model.SystemSettings
	config.DB.First(&s, 1)

	req, err := http.NewRequest("GET", downloadURL, nil)
	if err != nil {
		return err
	}
	if s.GithubToken != "" {
		req.Header.Set("Authorization", "Bearer "+s.GithubToken)
	}
	resp, err := getHTTPClient().Do(req)
	if err != nil {
		return fmt.Errorf("下载失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("下载返回 %d", resp.StatusCode)
	}

	// 下载到临时文件
	tmp := ServerBinaryPath + ".new"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	f.Close()

	// 备份旧二进制
	os.MkdirAll(BackupDir, 0755)
	backup := filepath.Join(BackupDir, fmt.Sprintf("asset-server-%s-%d", CurrentServerHash(), time.Now().Unix()))
	if data, err := os.ReadFile(ServerBinaryPath); err == nil {
		os.WriteFile(backup, data, 0755)
	}

	// 替换
	os.Chmod(tmp, 0755)
	if err := os.Rename(tmp, ServerBinaryPath); err != nil {
		return err
	}
	return nil
}

// Rollback 恢复最近的备份
func Rollback() (string, error) {
	entries, err := os.ReadDir(BackupDir)
	if err != nil || len(entries) == 0 {
		return "", fmt.Errorf("没有可用的备份")
	}
	// 找最新的备份
	var latest string
	var latestTime time.Time
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, _ := e.Info()
		if info.ModTime().After(latestTime) {
			latestTime = info.ModTime()
			latest = filepath.Join(BackupDir, e.Name())
		}
	}
	if latest == "" {
		return "", fmt.Errorf("没有可用的备份")
	}
	data, err := os.ReadFile(latest)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(ServerBinaryPath, data, 0755); err != nil {
		return "", err
	}
	return filepath.Base(latest), nil
}

// CompareVersion 比较语义化版本（忽略 v 前缀与预发布后缀）
// 返回 1 表示 a 比 b 新，0 相同，-1 比 b 旧
func CompareVersion(a, b string) int {
	norm := func(v string) []int {
		v = strings.TrimPrefix(strings.TrimSpace(v), "v")
		// 去掉 -rc1 / +build 之类后缀
		if i := strings.IndexAny(v, "-+"); i >= 0 {
			v = v[:i]
		}
		parts := strings.Split(v, ".")
		out := make([]int, 0, len(parts))
		for _, p := range parts {
			n := 0
			fmt.Sscanf(p, "%d", &n)
			out = append(out, n)
		}
		return out
	}
	x, y := norm(a), norm(b)
	for i := 0; i < len(x) || i < len(y); i++ {
		var xi, yi int
		if i < len(x) {
			xi = x[i]
		}
		if i < len(y) {
			yi = y[i]
		}
		if xi > yi {
			return 1
		}
		if xi < yi {
			return -1
		}
	}
	return 0
}

// isDevVersion 判断是否为开发版
func isDevVersion(v string) bool {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	return v == "" || v == "dev" || v == "latest"
}
