//go:build !windows

package main

import "fmt"

func installService(settingsPath string) error {
	return fmt.Errorf("Windows 服务模式仅支持 Windows 操作系统")
}

func uninstallService() error {
	return fmt.Errorf("Windows 服务模式仅支持 Windows 操作系统")
}

func runWebAsService(settingsPath string) error {
	return fmt.Errorf("Windows 服务模式仅支持 Windows 操作系统")
}