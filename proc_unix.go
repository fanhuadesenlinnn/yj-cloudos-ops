//go:build !windows

package main

import (
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
