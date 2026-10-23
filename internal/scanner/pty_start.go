package scanner

import (
	"io"
	"os/exec"

	"github.com/creack/pty"
)

// startWithPTY 用伪终端启动子进程，让它的 stdout 看起来是 TTY。
// TscanClient 只有在 TTY 下才绘制进度条，管道模式下会静默关闭进度输出。
// 返回的 wait 负责等待进程结束并清理 PTY。
func startWithPTY(cmd *exec.Cmd, sink io.Writer) (func() error, error) {
	f, err := pty.Start(cmd)
	if err != nil {
		return nil, err
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 8192)
		for {
			n, rerr := f.Read(buf)
			if n > 0 {
				_, _ = sink.Write(buf[:n])
			}
			if rerr != nil {
				return
			}
		}
	}()
	return func() error {
		werr := cmd.Wait()
		_ = f.Close()
		<-done
		return werr
	}, nil
}
