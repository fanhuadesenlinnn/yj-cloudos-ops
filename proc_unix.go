//go:build !windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// setProcessGroup 让子进程成为新的进程组组长，便于超时时整组杀掉（连带 sleep 等子进程）
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessGroup 杀掉整个进程组（含孙进程），
// 避免 sleep 等子进程持有 stdout/stderr 管道导致 cmd.Wait() 一直等待拷贝协程结束
func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

// processSignal0 探测进程存活（signal 0：不实际发信号，仅探测可否投递）
func processSignal0(p *os.Process) bool {
	return p.Signal(syscall.Signal(0)) == nil
}

// daemonize 后台化：用当前可执行文件重新拉起一个脱离终端的子进程（Setsid 新会话），
// 父进程立即退出，命令行窗口随之关闭；子进程重定向 stdout/stderr 到日志文件。
// 返回子进程是否成功拉起。Unix（Linux/macOS）实现。
func daemonize(logFile string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	// 去掉 -daemon 参数，避免子进程再次后台化
	args := make([]string, 0, len(os.Args))
	for _, a := range os.Args[1:] {
		if a == "-daemon" {
			continue
		}
		args = append(args, a)
	}
	lf, err := openLogFile(logFile)
	if err != nil {
		return fmt.Errorf("打开日志文件失败: %w", err)
	}
	defer lf.Close()

	cmd := exec.Command(exe, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // 新会话，脱离终端/SIGHUP
	cmd.Env = append(os.Environ(), "YJ_DAEMON=1")     // 标记子进程为后台实例（据此写 PID 文件）
	cmd.Stdout = lf
	cmd.Stderr = lf
	cmd.Stdin = nil
	if err := cmd.Start(); err != nil {
		return err
	}
	// 父进程不等待子进程，直接返回（main 随即退出）
	_ = cmd.Process.Release()
	return nil
}

// requestShutdown 通知后台实例优雅退出：
// Unix 直接给 PID 发 SIGTERM（系统原生方式）。返回错误表示未能送达。
func requestShutdown(info *pidFileInfo) error {
	p, err := os.FindProcess(info.PID)
	if err != nil {
		return err
	}
	return p.Signal(syscall.SIGTERM)
}
