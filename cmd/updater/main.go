// updater 边车服务：接收主应用指令，在宿主机执行镜像更新
// 只暴露内网端口，通过 docker.sock 操作 compose
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

type updateReq struct {
	Version    string `json:"version"`    // 目标版本，如 v0.0.4
	OldVersion string `json:"oldVersion"` // 当前版本，回滚用
}

type updateResp struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
	Output  string `json:"output,omitempty"`
}

var mu sync.Mutex

func run(name string, args ...string) (string, error) {
	return runTimeout(3*time.Minute, name, args...)
}

func runTimeout(d time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = "/project"
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return string(out), fmt.Errorf("命令超时（%v）: %s %s", d, name, strings.Join(args, " "))
	}
	return string(out), err
}

func healthCheck(stopAt time.Time) bool {
	for time.Now().Before(stopAt) {
		resp, err := http.Get("http://server:18888/api/health")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return true
			}
		}
		time.Sleep(3 * time.Second)
	}
	return false
}

// emit 流式输出一行日志（每行立即 flush，前端实时可见）
func emit(w http.ResponseWriter, line string) {
	fmt.Fprintf(w, "%s\n", line)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

func handleUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	mu.Lock()
	defer mu.Unlock()

	var req updateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Version == "" {
		http.Error(w, "参数错误：缺少 version", http.StatusBadRequest)
		return
	}

	// 流式响应：text/plain + 立即 flush
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Stream", "true")
	w.WriteHeader(http.StatusOK)

	log.Printf("[update] 开始更新 -> %s（当前 %s）", req.Version, req.OldVersion)
	emit(w, "START "+req.Version)

	// 1) 备份数据库（失败不阻断，但记录）
	emit(w, "== 备份数据库 ==")
	ts := time.Now().Format("20060102-150405")
	dumpPath := fmt.Sprintf("/project/data/backup/pre-update-%s.sql", ts)
	if out, err := run("docker", "exec", "asset-postgres", "pg_dump", "-U", "asset", "asset_platform"); err == nil {
		if err := os.WriteFile(dumpPath, []byte(out), 0o644); err != nil {
			emit(w, "备份写入失败: "+err.Error())
		} else {
			emit(w, "备份完成: "+dumpPath)
		}
	} else {
		emit(w, "备份失败（继续更新）: "+err.Error())
	}

	// 2) 改 .env 版本号（统一加 v 前缀）
	emit(w, "== 切换版本 ==")
	envPath := "/project/.env"
	content, _ := os.ReadFile(envPath)
	ver := req.Version
	if ver != "" && !strings.HasPrefix(ver, "v") {
		ver = "v" + ver
	}
	lines := strings.Split(string(content), "\n")
	found := false
	for i, ln := range lines {
		if strings.HasPrefix(ln, "APP_VERSION=") {
			lines[i] = "APP_VERSION=" + ver
			found = true
		}
	}
	if !found {
		lines = append(lines, "APP_VERSION="+ver)
	}
	os.WriteFile(envPath, []byte(strings.Join(lines, "\n")), 0o644)
	emit(w, "APP_VERSION="+req.Version)

	// 3) 拉镜像（本地已有则跳过）
	emit(w, "== 拉取镜像 ==")
	img := "ghcr.io/rongsec/cyberlook:" + req.Version
	if q, err := run("docker", "images", "-q", img); err == nil && strings.TrimSpace(q) != "" {
		emit(w, "本地已有镜像 "+img+"，跳过拉取")
	} else if _, err := runTimeout(8*time.Minute, "docker", "compose", "pull", "server"); err != nil {
		emit(w, "拉取失败: "+err.Error())
		rollbackEnv(req.OldVersion)
		emit(w, "ERROR 拉取镜像失败，已回滚 .env")
		return
	} else {
		emit(w, "拉取完成")
	}

	// 4) 重启 server（不动 pg/redis）
	emit(w, "== 重启服务 ==")
	if _, err := runTimeout(4*time.Minute, "docker", "compose", "up", "-d", "--no-deps", "--pull", "never", "--force-recreate", "server"); err != nil {
		emit(w, "重启失败: "+err.Error())
		rollbackAll(req.OldVersion)
		emit(w, "ERROR 重启失败，已回滚")
		return
	}
	emit(w, "服务已重启")

	// 5) 健康检查（最多 90 秒）
	emit(w, "== 健康检查 ==")
	if !healthCheck(time.Now().Add(90 * time.Second)) {
		rollbackAll(req.OldVersion)
		emit(w, "ERROR 健康检查失败，已回滚到 "+req.OldVersion)
		return
	}
	emit(w, "健康检查通过")

	log.Printf("[update] 更新完成 -> %s", req.Version)
	emit(w, "SUCCESS 已更新到 "+req.Version)
}

func rollbackEnv(v string) {
	// GHCR 的 tag 都带 v 前缀，version.Version 不带，统一补上
	if v != "" && !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	content, _ := os.ReadFile("/project/.env")
	lines := strings.Split(string(content), "\n")
	for i, ln := range lines {
		if strings.HasPrefix(ln, "APP_VERSION=") {
			lines[i] = "APP_VERSION=" + v
		}
	}
	os.WriteFile("/project/.env", []byte(strings.Join(lines, "\n")), 0o644)
}

func rollbackAll(v string) {
	rollbackEnv(v)
	runTimeout(4*time.Minute, "docker", "compose", "up", "-d", "--no-deps", "--pull", "never", "--force-recreate", "server")
}

func writeJSON(w http.ResponseWriter, code int, v updateResp) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func main() {
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok":true}`))
	})
	http.HandleFunc("/update", handleUpdate)
	log.Println("[updater] listening :9999")
	log.Fatal(http.ListenAndServe(":9999", nil))
}
