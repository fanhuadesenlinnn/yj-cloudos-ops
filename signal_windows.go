//go:build windows

package main

// handleSignals Windows 无 POSIX 信号；-stop 走 /api/shutdown（本机+token），无需处理
func handleSignals() {}
