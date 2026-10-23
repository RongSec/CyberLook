package tasklog

import (
	"fmt"
	"sync"
	"time"
)

// TaskLog 任务日志：普通行 + 当前进度条（进度条单独维护，避免刷屏）
type TaskLog struct {
	Lines    []string
	Progress string
}

var (
	mu   sync.Mutex
	logs = map[uint]*TaskLog{}
)

func get(taskID uint) *TaskLog {
	l, ok := logs[taskID]
	if !ok {
		l = &TaskLog{Lines: []string{}}
		logs[taskID] = l
	}
	return l
}

// Append 追加一行普通日志（限制 5000 行）
func Append(taskID uint, line string) {
	mu.Lock()
	defer mu.Unlock()
	l := get(taskID)
	if len(l.Lines) >= 5000 {
		l.Lines = append(l.Lines[4000:], line)
	} else {
		l.Lines = append(l.Lines, line)
	}
}

// AppendTS 追加带时间戳的日志（平台自身事件用，扫描原始输出不要用）
func AppendTS(taskID uint, format string, args ...interface{}) {
	line := fmt.Sprintf("[%s] ", time.Now().Format("15:04:05")) + fmt.Sprintf(format, args...)
	Append(taskID, line)
}

// SetProgress 更新当前进度条（覆盖上一帧）
func SetProgress(taskID uint, p string) {
	mu.Lock()
	defer mu.Unlock()
	get(taskID).Progress = p
}

// Get 获取任务日志（行 + 进度）
func Get(taskID uint) ([]string, string) {
	mu.Lock()
	defer mu.Unlock()
	l := get(taskID)
	out := make([]string, len(l.Lines))
	copy(out, l.Lines)
	return out, l.Progress
}

// Clear 清除任务日志
func Clear(taskID uint) {
	mu.Lock()
	defer mu.Unlock()
	delete(logs, taskID)
}
