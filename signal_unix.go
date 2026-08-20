//go:build !windows

package main

import (
	"os"
	"os/signal"
	"syscall"
)

// handleSignals 后台实例监听 SIGTERM/SIGINT，触发优雅关闭（-stop 发 SIGTERM 走这里）
func handleSignals() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGTERM, syscall.SIGINT)
	<-ch
	shutdownCh <- struct{}{}
}
