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

func handleUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	mu.Lock()
	defer mu.Unlock()

	var req updateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Version == "" {
		writeJSON(w, http.StatusBadRequest, updateResp{Message: "参数错误：缺少 version"})
		return
	}

	log.Printf("[update] 开始更新 -> %s（当前 %s）", req.Version, req.OldVersion)
	var logs []string

	// 1) 备份数据库（失败不阻断，但记录）
	logs = append(logs, "== 备份数据库 ==")
	ts := time.Now().Format("20060102-150405")
	dumpPath := fmt.Sprintf("/project/data/backup/pre-update-%s.sql", ts)
	if out, err := run("docker", "exec", "asset-postgres", "pg_dump", "-U", "asset", "asset_platform"); err == nil {
		if err := os.WriteFile(dumpPath, []byte(out), 0o644); err != nil {
			logs = append(logs, "备份写入失败: "+err.Error())
		} else {
			logs = append(logs, "备份完成: "+dumpPath)
		}
	} else {
		logs = append(logs, "备份失败（继续更新）: "+err.Error())
	}

	// 2) 改 .env 版本号
	logs = append(logs, "== 切换版本 ==")
	envPath := "/project/.env"
	content, _ := os.ReadFile(envPath)
	lines := strings.Split(string(content), "\n")
	found := false
	for i, ln := range lines {
		if strings.HasPrefix(ln, "APP_VERSION=") {
			lines[i] = "APP_VERSION=" + req.Version
			found = true
		}
	}
	if !found {
		lines = append(lines, "APP_VERSION="+req.Version)
	}
	os.WriteFile(envPath, []byte(strings.Join(lines, "\n")), 0o644)
	logs = append(logs, "APP_VERSION="+req.Version)

	// 3) 拉镜像（本地已有则跳过，避免每次更新都卡在 registry 查询上）
	logs = append(logs, "== 拉取镜像 ==")
	img := "ghcr.io/rongsec/cyberlook:" + req.Version
	if q, err := run("docker", "images", "-q", img); err == nil && strings.TrimSpace(q) != "" {
		logs = append(logs, "本地已有镜像 "+img+"，跳过拉取")
	} else if out, err := runTimeout(8*time.Minute, "docker", "compose", "-p", "asset-platform", "pull", "server"); err != nil {
		logs = append(logs, "拉取失败: "+err.Error()+"\n"+out)
		// 回滚 .env
		rollbackEnv(req.OldVersion)
		writeJSON(w, http.StatusInternalServerError, updateResp{Message: "拉取镜像失败", Output: strings.Join(logs, "\n")})
		return
	} else {
		logs = append(logs, "拉取完成")
	}

	// 4) 重启 server（不动 pg/redis）
	logs = append(logs, "== 重启服务 ==")
	if out, err := runTimeout(4*time.Minute, "docker", "compose", "-p", "asset-platform", "up", "-d", "--no-deps", "--pull", "never", "--force-recreate", "server"); err != nil {
		logs = append(logs, "重启失败: "+err.Error()+"\n"+out)
		rollbackAll(req.OldVersion)
		writeJSON(w, http.StatusInternalServerError, updateResp{Message: "重启失败，已回滚", Output: strings.Join(logs, "\n")})
		return
	}

	// 5) 健康检查（最多 90 秒）
	logs = append(logs, "== 健康检查 ==")
	if !healthCheck(time.Now().Add(90 * time.Second)) {
		logs = append(logs, "健康检查失败，回滚到 "+req.OldVersion)
		rollbackAll(req.OldVersion)
		writeJSON(w, http.StatusInternalServerError, updateResp{Message: "健康检查失败，已回滚", Output: strings.Join(logs, "\n")})
		return
	}
	logs = append(logs, "健康检查通过")

	log.Printf("[update] 更新完成 -> %s", req.Version)
	writeJSON(w, http.StatusOK, updateResp{OK: true, Message: "已更新到 " + req.Version, Output: strings.Join(logs, "\n")})
}

func rollbackEnv(v string) {
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
	runTimeout(4*time.Minute, "docker", "compose", "-p", "asset-platform", "up", "-d", "--no-deps", "--pull", "never", "--force-recreate", "server")
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
