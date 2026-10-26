package service

import (
	"encoding/json"
	"log"
	"strconv"
	"strings"

	"asset-platform/internal/config"
	"asset-platform/internal/model"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// pipelineState 流水线断点（落盘用）
// 落盘内容 = 探测进度 + 待下发池，服务中断后可恢复：
//   - offset 之后的 IP 继续探测
//   - pool 里的 IP 继续下发
type pipelineState struct {
	TaskID    uint      `json:"taskId"`
	Offset    int       `json:"offset"`
	Pool      []string  `json:"pool"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func pipelineDir() string {
	return filepath.Join("data", "pipeline")
}

func pipelineFilePath(taskID uint) string {
	return filepath.Join(pipelineDir(), fmt.Sprintf("%d.json", taskID))
}

// savePipelineFile 原子写入断点文件（tmp + rename，避免崩溃产生半截文件）
func savePipelineFile(taskID uint, offset int, pool []string) error {
	if err := os.MkdirAll(pipelineDir(), 0755); err != nil {
		return err
	}
	if pool == nil {
		pool = []string{}
	}
	st := pipelineState{TaskID: taskID, Offset: offset, Pool: pool, UpdatedAt: time.Now()}
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	tmp := pipelineFilePath(taskID) + ".tmp"
	if err := os.WriteFile(tmp, b, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, pipelineFilePath(taskID))
}

// loadPipelineFile 读取断点；ok=false 表示没有或损坏
func loadPipelineFile(taskID uint) (offset int, pool []string, ok bool) {
	b, err := os.ReadFile(pipelineFilePath(taskID))
	if err != nil {
		return 0, nil, false
	}
	var st pipelineState
	if err := json.Unmarshal(b, &st); err != nil {
		return 0, nil, false
	}
	if st.Pool == nil {
		st.Pool = []string{}
	}
	return st.Offset, st.Pool, true
}

// removePipelineFile 删除断点（任务完成/删除/重跑时调用）
func removePipelineFile(taskID uint) {
	_ = os.Remove(pipelineFilePath(taskID))
	_ = os.Remove(pipelineFilePath(taskID) + ".tmp")
}

// CleanupOrphanPipelines 清理孤儿断点文件（服务启动时调用）：
// 任务已不存在/已结束，但断点文件还留着的，属于崩溃残留。
func CleanupOrphanPipelines() int {
	dir := pipelineDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	removed := 0
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		idStr := strings.TrimSuffix(name, ".json")
		id, err := strconv.ParseUint(idStr, 10, 64)
		if err != nil {
			continue
		}
		var task model.ScanTask
		err = config.DB.First(&task, uint(id)).Error
		if err != nil || task.Status != "running" {
			_ = os.Remove(filepath.Join(dir, name))
			removed++
		}
	}
	if removed > 0 {
		log.Printf("[pipeline] 清理 %d 个孤儿断点文件", removed)
	}
	return removed
}
