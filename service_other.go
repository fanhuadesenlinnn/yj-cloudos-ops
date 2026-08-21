//go:build !windows

package main

import "fmt"

// ensureAdmin 非 Windows 平台无 UAC 概念，无需提权
func ensureAdmin() (bool, error) { return false, nil }

func installService(settingsPath string) error {
	return fmt.Errorf("Windows 服务模式仅支持 Windows 操作系统")
}

func uninstallService() error {
	return fmt.Errorf("Windows 服务模式仅支持 Windows 操作系统")
}

func runWebAsService(settingsPath string) error {
	return fmt.Errorf("Windows 服务模式仅支持 Windows 操作系统")
}