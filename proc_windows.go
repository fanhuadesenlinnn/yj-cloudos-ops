//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// setProcessGroup Windows 无进程组概念，空实现
func setProcessGroup(cmd *exec.Cmd) {}

// killProcessGroup Windows 直接杀子进程（cmd /C 通常直接 exec 目标命令，可接受）
func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

// processSignal0 Windows 无 signal 0 概念，用 OpenProcess 探测进程存活
func processSignal0(p *os.Process) bool {
	h, err := syscall.OpenProcess(syscall.PROCESS_QUERY_INFORMATION, false, uint32(p.Pid))
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(h)
	// 用 WaitForSingleObject 检查是否已退出（超时 0 = 仍存活）
	code, _ := syscall.WaitForSingleObject(h, 0)
	return code == syscall.WAIT_TIMEOUT
}

// daemonize 后台化：Windows 用 CREATE_NEW_PROCESS_GROUP | DETACHED_PROCESS | CREATE_NO_WINDOW
// 创建脱离控制台的子进程，父进程立即退出（命令行窗口随之关闭），子进程重定向到日志。
func daemonize(logFile string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	// 去掉 -daemon，避免子进程再次后台化
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
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS | windows.CREATE_NO_WINDOW,
	}
	cmd.Env = append(os.Environ(), "YJ_DAEMON=1") // 标记子进程为后台实例（据此写 PID 文件）
	cmd.Stdout = lf
	cmd.Stderr = lf
	cmd.Stdin = nil
	if err := cmd.Start(); err != nil {
		return err
	}
	_ = cmd.Process.Release()
	return nil
}

// requestShutdown Windows 无 POSIX 信号：向本机 Web 端口发带 token 的 shutdown 请求
// （仅接受 127.0.0.1 + token 匹配，安全）。返回错误表示未能送达。
func requestShutdown(info *pidFileInfo) error {
	port := parseAddrPort(info.Addr)
	if port == "" {
		return fmt.Errorf("PID 文件中无端口信息")
	}
	return httpShutdownRequest("127.0.0.1:"+port, info.Token)
}
