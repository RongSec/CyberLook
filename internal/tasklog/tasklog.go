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
	// loaded 是否已经从持久化存储回填过（空日志时才回填，避免覆盖新日志）
	loaded bool
}

const maxLines = 5000

var (
	mu   sync.Mutex
	logs = map[uint]*TaskLog{}
)

// Entry 一条待落盘的日志
type Entry struct {
	TaskID uint
	Line   string
}

// 持久化钩子：由 service 层注入，避免 tasklog 反向依赖 db/model
var (
	writeHook func([]Entry)
	loadHook  func(taskID uint) []string
	flushCh   chan Entry
	flushOnce sync.Once
)

// SetStore 注入持久化实现：write 批量落盘，load 按任务回读（返回按时间正序的行）
func SetStore(write func([]Entry), load func(uint) []string) {
	writeHook = write
	loadHook = load
}

func get(taskID uint) *TaskLog {
	l, ok := logs[taskID]
	if !ok {
		l = &TaskLog{Lines: []string{}}
		logs[taskID] = l
	}
	return l
}

// Append 追加一行普通日志（限制 5000 行），并异步落盘
func Append(taskID uint, line string) {
	mu.Lock()
	l := get(taskID)
	if len(l.Lines) >= maxLines {
		l.Lines = append(l.Lines[4000:], line)
	} else {
		l.Lines = append(l.Lines, line)
	}
	mu.Unlock()
	persist(taskID, line)
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

// Get 获取任务日志（行 + 进度）。内存为空时从持久化存储回填（服务重启后仍能看到历史日志）
func Get(taskID uint) ([]string, string) {
	mu.Lock()
	l := get(taskID)
	needLoad := len(l.Lines) == 0 && !l.loaded && loadHook != nil
	if needLoad {
		l.loaded = true
	}
	mu.Unlock()

	if needLoad {
		if lines := loadHook(taskID); len(lines) > 0 {
			mu.Lock()
			cur := get(taskID)
			if len(cur.Lines) == 0 { // 期间可能已有新日志进来，别覆盖
				cur.Lines = lines
			}
			mu.Unlock()
		}
	}

	mu.Lock()
	defer mu.Unlock()
	l = get(taskID)
	out := make([]string, len(l.Lines))
	copy(out, l.Lines)
	return out, l.Progress
}

// Clear 清除任务日志（内存；持久化数据保留，靠保留策略清理）
func Clear(taskID uint) {
	mu.Lock()
	defer mu.Unlock()
	delete(logs, taskID)
}

// ===== 异步批量落盘 =====
// 每行都同步写库会拖慢扫描输出，这里攒批：最多 200 行或 800ms 落一次。

func persist(taskID uint, line string) {
	if writeHook == nil {
		return
	}
	flushOnce.Do(func() {
		flushCh = make(chan Entry, 8192)
		go flushLoop()
	})
	select {
	case flushCh <- Entry{TaskID: taskID, Line: line}:
	default: // 队列满就丢弃落盘（内存日志仍完整），不能因为写日志阻塞扫描
	}
}

func flushLoop() {
	batch := make([]Entry, 0, 200)
	timer := time.NewTimer(800 * time.Millisecond)
	for {
		select {
		case e := <-flushCh:
			batch = append(batch, e)
			if len(batch) >= 200 {
				flush(&batch)
			}
		case <-timer.C:
			flush(&batch)
			timer.Reset(800 * time.Millisecond)
		}
	}
}

func flush(batch *[]Entry) {
	if len(*batch) == 0 {
		return
	}
	if writeHook != nil {
		writeHook(*batch)
	}
	*batch = (*batch)[:0]
}
