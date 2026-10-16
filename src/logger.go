// 文件日志:写到 exe 旁 logs/backup-YYYY-MM-DD.log,按天切分。
// 服务模式无控制台,日志是唯一留痕;同时也输出到 stdout(前台运行时可看)。
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Logger 简单的按天切分日志器,并发安全。
type Logger struct {
	dir     string
	mu      sync.Mutex
	curDate string
	file    *os.File
	echo    bool // 是否同时输出到 stdout
}

// NewLogger 在 dir 下创建日志器。
func NewLogger(dir string, echo bool) (*Logger, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Logger{dir: dir, echo: echo}, nil
}

func (l *Logger) rotate(date string) error {
	if l.file != nil {
		l.file.Close()
		l.file = nil
	}
	name := filepath.Join(l.dir, "backup-"+date+".log")
	f, err := os.OpenFile(name, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	l.file = f
	l.curDate = date
	return nil
}

// Printf 记录一行带时间戳的日志。
func (l *Logger) Printf(format string, a ...any) {
	now := time.Now()
	date := now.Format("2006-01-02")
	line := now.Format("2006-01-02 15:04:05") + " " + fmt.Sprintf(format, a...)

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.curDate != date || l.file == nil {
		if err := l.rotate(date); err != nil {
			// 日志写不了也不能让备份崩
			if l.echo {
				fmt.Fprintln(os.Stderr, "日志写入失败:", err)
			}
		}
	}
	if l.file != nil {
		fmt.Fprintln(l.file, line)
	}
	if l.echo {
		fmt.Println(line)
	}
}

// Close 关闭底层文件。
func (l *Logger) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file != nil {
		l.file.Close()
		l.file = nil
	}
}
