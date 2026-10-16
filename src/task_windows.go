//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// quoteArgs 给含空格/制表符的参数加双引号(用于拼进 schtasks /tr)。
func quoteArgs(args []string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		if strings.ContainsAny(a, " \t") {
			out[i] = `"` + a + `"`
		} else {
			out[i] = a
		}
	}
	return out
}

// handleTaskCommand 用 schtasks.exe 管理 Windows 计划任务。
// install 时把 -i / -d 参数写进任务命令行,任务拉起后仍按该间隔与目标目录运行。
//
// 说明: /sc onlogon 需要管理员权限;/sc minute 个人用户即可。
// 这里用 onlogon: 登录后启动,exe 自身按 interval 分钟循环。
func handleTaskCommand(action, exePath string, cliInterval int, cliTarget string, logger *Logger) {
	switch action {
	case "install":
		args := []string{"run"}
		if cliInterval > 0 {
			args = append(args, "-i", strconv.Itoa(cliInterval))
		}
		if strings.TrimSpace(cliTarget) != "" {
			args = append(args, "-d", cliTarget)
		}
		// /tr 的命令行: "exe路径" run [-i N] [-d DIRS]
		tr := fmt.Sprintf(`"%s" %s`, exePath, strings.Join(quoteArgs(args), " "))
		// /sc onlogon 登录即启动; exe 内部按 interval 循环,故任务本身只触发一次
		err := runSchtasks("/create", "/tn", taskName, "/tr", tr, "/sc", "onlogon", "/f")
		if err != nil {
			reportTaskErr(logger, "install", err)
			return
		}
		logger.Printf("计划任务 %s 已注册(登录时启动)", taskName)
		fmt.Printf("install 成功: 计划任务 %q 已注册(登录时自动启动)\n", taskName)
		fmt.Printf("  任务命令: %s\n", tr)
	case "uninstall":
		if err := runSchtasks("/delete", "/tn", taskName, "/f"); err != nil {
			reportTaskErr(logger, "uninstall", err)
			return
		}
		logger.Printf("计划任务 %s 已删除", taskName)
		fmt.Printf("uninstall 成功: 计划任务 %q 已删除\n", taskName)
	case "status":
		out, err := schtasksOutput("/query", "/tn", taskName, "/fo", "LIST", "/v")
		if err != nil {
			logger.Printf("[error] 查询计划任务失败: %v", err)
			fmt.Fprintf(os.Stderr, "计划任务 %q 未注册或查询失败\n", taskName)
			fmt.Fprintln(os.Stderr, "(首次请以管理员运行: backup.exe install)")
			os.Exit(1)
		}
		fmt.Printf("计划任务 %q 已注册。\n\n%s", taskName, taskBrief(out))
	}
}

func runSchtasks(args ...string) error {
	cmd := exec.Command("schtasks", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func schtasksOutput(args ...string) (string, error) {
	out, err := exec.Command("schtasks", args...).CombinedOutput()
	return string(out), err
}

// taskBrief 从 schtasks /v 输出里挑几行关键信息(任务名/状态/运行方式)。
func taskBrief(out string) string {
	var sb strings.Builder
	for _, line := range strings.Split(out, "\n") {
		t := strings.TrimSpace(line)
		if t == "" {
			continue
		}
		if strings.HasPrefix(t, "任务名") || strings.HasPrefix(t, "状态") ||
			strings.HasPrefix(t, "下次运行时间") || strings.HasPrefix(t, "要运行的任务") ||
			strings.HasPrefix(t, "TaskName") || strings.HasPrefix(t, "Status") ||
			strings.HasPrefix(t, "Next Run Time") || strings.HasPrefix(t, "Task To Run") {
			sb.WriteString("  " + t + "\n")
		}
	}
	return sb.String()
}

func reportTaskErr(logger *Logger, action string, err error) {
	logger.Printf("[error] %s 失败: %v", action, err)
	fmt.Fprintf(os.Stderr, "%s 失败: %v\n", action, err)
	fmt.Fprintln(os.Stderr, "提示: install/uninstall 需以管理员身份运行本 exe。")
	os.Exit(1)
}
