//go:build windows

package main

import "os/exec"

// setProcessGroup Windows 无进程组概念，空实现
func setProcessGroup(cmd *exec.Cmd) {}

// killProcessGroup Windows 直接杀子进程（cmd /C 通常直接 exec 目标命令，可接受）
func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
