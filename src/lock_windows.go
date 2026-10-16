//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// acquireSingleInstance 用「共享模式为 0 的独占文件句柄」保证同一时刻只有一个实例
// 在循环备份。句柄随进程退出(含崩溃)由系统自动关闭,不会留下陈旧锁。
func acquireSingleInstance(exeDir string) (release func(), err error) {
	lockPath := filepath.Join(exeDir, ".backup-c.lock")
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		return nil, fmt.Errorf("创建锁文件目录失败: %w", err)
	}
	namePtr, err := windows.UTF16PtrFromString(lockPath)
	if err != nil {
		return nil, err
	}
	// 共享模式 0:任何其它进程再打开都会失败(ERROR_SHARING_VIOLATION)
	h, err := windows.CreateFile(
		namePtr,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		0,
		nil,
		windows.OPEN_ALWAYS,
		windows.FILE_ATTRIBUTE_HIDDEN,
		0,
	)
	if err != nil {
		if errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
			return nil, fmt.Errorf("已有 backup-c 实例在运行")
		}
		return nil, fmt.Errorf("创建锁文件失败: %w", err)
	}
	return func() { windows.CloseHandle(h) }, nil
}
