// backup-c:把 config.yaml 清单中的文件/目录镜像备份到目标目录。
// 由 Windows 计划任务(或手动)启动,启动后每隔 interval 分钟备份一次。
//
// 用法:
//
//	backup.exe run [-i 分钟] [-d 目标目录]   启动后按 interval 分钟循环备份(默认)
//	backup.exe once                          立即备份一次后退出
//	backup.exe install                       注册 Windows 计划任务(登录自动启动,需管理员)
//	backup.exe uninstall                     删除计划任务(需管理员)
//	backup.exe status                        查看计划任务状态
//	backup.exe version                       显示版本
//
// 优先级: 间隔 -i -> config.interval -> 默认 10 分钟
//
//	目标 -d -> config.targetDir -> 默认 <exe>/dist
package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// version 可在构建时注入: go build -ldflags "-X main.version=v1.2.3"
// 未注入时为下方默认值。
var version = "1.0.0"

const taskName = "backup-c"
const defaultInterval = 10 // 分钟

func main() {
	exePath, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, "无法定位可执行文件:", err)
		os.Exit(1)
	}
	exeDir := filepath.Dir(exePath)
	if _, err := os.Stat(filepath.Join(exeDir, "config.yaml")); err != nil {
		if wd, err := os.Getwd(); err == nil {
			if _, err := os.Stat(filepath.Join(wd, "config.yaml")); err == nil {
				exeDir = wd
			}
		}
	}

	action, cliInterval, cliTarget, err := parseArgs(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "参数错误:", err)
		fmt.Fprintln(os.Stderr)
		printUsage()
		os.Exit(2)
	}

	switch action {
	case "version", "-v", "--version":
		fmt.Printf("backup-c %s\n", version)
		return
	case "help", "-h", "--help":
		printUsage()
		return
	}

	echo := action == "run" || action == "once"
	logger, err := NewLogger(filepath.Join(exeDir, "logs"), echo)
	if err != nil {
		fmt.Fprintln(os.Stderr, "初始化日志失败:", err)
		os.Exit(1)
	}
	defer logger.Close()

	engine, err := newEngine(exeDir, cliTarget, logger)
	if err != nil {
		logger.Printf("[error] %v", err)
		fmt.Fprintln(os.Stderr, "初始化失败:", err)
		os.Exit(1)
	}

	interval, src := resolveInterval(cliInterval, engine.Manifest.Interval)

	switch action {
	case "once":
		logger.Printf("手动备份一次: sourceRoot=%s", engine.Home)
		st, err := engine.RunOnce()
		if err != nil {
			logger.Printf("[error] 备份失败: %v", err)
			fmt.Fprintln(os.Stderr, "备份失败:", err)
			os.Exit(1)
		}
		logger.Printf("备份完成: 新增 %d 更新 %d 未变 %d 删除 %d 空目录复原 %d 清理空目录 %d",
			st.Copied, st.Updated, st.Skipped, st.Deleted, st.DirsMade, st.DirsRemoved)
		return

	case "install":
		handleTaskCommand("install", exePath, cliInterval, cliTarget, logger)
		return
	case "uninstall":
		handleTaskCommand("uninstall", exePath, 0, "", logger)
		return
	case "status":
		handleTaskCommand("status", exePath, 0, "", logger)
		return

	case "run":
		runLoop(engine, logger, exeDir, cliInterval, cliTarget, interval, src)

	default:
		fmt.Fprintf(os.Stderr, "未知子命令: %s\n\n", action)
		printUsage()
		os.Exit(1)
	}
}

// runLoop 启动后立即备份一次,随后每隔 interval 分钟备份一次,直到收到 Ctrl+C。
// 期间监听 config.yaml,一旦变化就重载配置(含 interval/targetDir/include/exclude)
// 并按新的间隔重置定时器,同时立即备份一次。
func runLoop(engine *Engine, logger *Logger, exeDir string, cliInterval int, cliTarget string, interval int, intervalSrc string) {
	release, err := acquireSingleInstance(exeDir)
	if err != nil {
		logger.Printf("[error] %v", err)
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer release()

	engine.backupAndLog("[启动]")
	logger.Printf("进入循环: 每 %d 分钟同步一次 (间隔来源: %s, sourceRoot=%s, destRoot=%s, 版本 %s)",
		interval, intervalSrc, engine.Home, strings.Join(engine.DestRoots, " ; "), version)

	ticker := newTicker(interval)
	defer ticker.Stop()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)

	// 独立于备份间隔的配置监听(每 2 秒探测一次 config.yaml 是否变化)
	watch := time.NewTicker(2 * time.Second)
	defer watch.Stop()
	lastSig := configSignature(exeDir)

	for {
		select {
		case <-ticker.C:
			engine.backupAndLog("[定时]")

		case <-watch.C:
			sig := configSignature(exeDir)
			if sig == lastSig || sig == "" {
				continue
			}
			lastSig = sig
			logger.Printf("[重载] 检测到 config.yaml 变化,重新加载配置并立即备份...")
			ne, err := newEngine(exeDir, cliTarget, logger)
			if err != nil {
				logger.Printf("[error] 重载配置失败,沿用原配置: %v", err)
				continue
			}
			engine = ne
			// 重载后按最新 interval 重置定时器(命令行 -i 仍优先)
			newInterval, newSrc := resolveInterval(cliInterval, engine.Manifest.Interval)
			if newInterval != interval {
				interval, intervalSrc = newInterval, newSrc
				ticker.Stop()
				ticker = newTicker(interval)
				logger.Printf("[重载] 同步间隔已改为每 %d 分钟 (间隔来源: %s)", interval, intervalSrc)
			}
			engine.backupAndLog("[重载]")

		case <-sig:
			logger.Printf("收到退出信号,停止循环。")
			return
		}
	}
}

// newTicker 按分钟数创建备份定时器。
func newTicker(interval int) *time.Ticker {
	return time.NewTicker(time.Duration(interval) * time.Minute)
}

// configSignature 返回 config.yaml 的内容指纹(长度 + sha256);读不到时返回空串。
//
// 不能只用 mtime+size:同步盘(如坚果云)、cp -p、robocopy /copyall 等会保留原修改时间,
// 若改动前后字节数又恰好相同(如 interval: 60 -> 61),mtime+size 就完全不变,
// 于是永远检测不到变化、不会重载。故以文件内容为准。
func configSignature(exeDir string) string {
	raw, err := os.ReadFile(filepath.Join(exeDir, "config.yaml"))
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("%d:%x", len(raw), sum)
}

// resolveInterval 按 -i 参数 -> config.interval -> 默认 10 分钟 给出间隔(分钟)与来源。
func resolveInterval(cliInterval, cfgInterval int) (interval int, src string) {
	switch {
	case cliInterval > 0:
		return cliInterval, "-i 参数"
	case cfgInterval > 0:
		return cfgInterval, "config.interval"
	default:
		return defaultInterval, "默认"
	}
}

// parseArgs 解析命令行,支持子命令、-i 间隔与 -d 目标目录(均位置无关)。
func parseArgs(args []string) (action string, interval int, target string, err error) {
	action = "run"
	for i := 0; i < len(args); i++ {
		a := args[i]
		// 取下一个参数值(用于 -i/-s/-d 的空格写法)
		nextVal := func(flag string) (string, error) {
			if i+1 >= len(args) {
				return "", fmt.Errorf("%s 缺少参数值", flag)
			}
			i++
			return args[i], nil
		}
		switch {
		case a == "-i" || a == "--interval":
			val, e := nextVal(a)
			if e != nil {
				return "", 0, "", e
			}
			v, e := parseInterval(val)
			if e != nil {
				return "", 0, "", e
			}
			interval = v
		case a == "-d" || a == "--target":
			val, e := nextVal(a)
			if e != nil {
				return "", 0, "", e
			}
			target = val
		case hasAnyPrefix(a, "-i=", "--interval="):
			_, val, _ := strings.Cut(a, "=")
			v, e := parseInterval(val)
			if e != nil {
				return "", 0, "", e
			}
			interval = v
		case strings.HasPrefix(a, "-d=") || strings.HasPrefix(a, "--target="):
			_, val, _ := strings.Cut(a, "=")
			target = val
		case strings.HasPrefix(a, "-"):
			if action == "run" {
				action = a
			} else {
				return "", 0, "", fmt.Errorf("未知选项: %s", a)
			}
		default:
			action = a
		}
	}
	return action, interval, target, nil
}

// hasAnyPrefix 判断 s 是否以任一前缀开头。
func hasAnyPrefix(s string, prefixes ...string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

func parseInterval(s string) (int, error) {
	v, err := strconv.Atoi(s)
	if err != nil || v < 1 {
		return 0, fmt.Errorf("非法的间隔分钟数 %q(需为正整数)", s)
	}
	return v, nil
}

func printUsage() {
	fmt.Print(`backup-c - 定时镜像备份

用法:
  backup.exe [子命令] [-i 分钟] [-d 目标目录]

子命令:
  run         启动后按 interval 分钟循环备份(默认)
  once        立即备份一次后退出
  install     注册 Windows 计划任务(登录时自动启动,需管理员)
  uninstall   删除计划任务(需管理员)
  status      查看计划任务状态
  version     显示版本

选项:
  -i, --interval N  备份间隔,单位分钟(如 -i 20 表示每 20 分钟)
  -d, --target DIRS 备份目标目录,多个用逗号分隔(如 -d D:\a,E:\b)

优先级:
  间隔   -i 参数 -> config.yaml 的 interval   -> 默认 10 分钟
  目标   -d 参数 -> config.yaml 的 targetDir  -> 默认 <exe 目录>\dist

配置: 与本 exe 同目录的 config.yaml(sourceRoot / targetDir / interval / include / exclude)
日志: 与本 exe 同目录的 logs/backup-YYYY-MM-DD.log
备份: 默认目标为本 exe 同目录的 dist/,可用 -d 或 config.targetDir 改(支持多个)
`)
}
