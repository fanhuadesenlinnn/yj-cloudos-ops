package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ---------- 后台化（-daemon）与退出（-stop / /api/shutdown） ----------

// pidFileInfo web.pid 内容：后台实例的进程ID、shutdown token、监听地址
type pidFileInfo struct {
	PID      int    `json:"pid"`
	Token    string `json:"token"`
	Addr     string `json:"addr"`
	LogFile  string `json:"logFile"`
}

// pidFilePath PID 文件路径（默认与设置文件同目录 web.pid）
func pidFilePath(settingsPath string) string {
	return filepath.Join(filepath.Dir(settingsPath), "web.pid")
}

// writePIDFile 写入 PID 文件（后台模式）
func writePIDFile(path string, pid int, token, addr, logFile string) error {
	info := pidFileInfo{PID: pid, Token: token, Addr: addr, LogFile: logFile}
	data, err := json.Marshal(info)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// readPIDFile 读取 PID 文件；不存在返回 os.ErrNotExist
func readPIDFile(path string) (*pidFileInfo, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var info pidFileInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, fmt.Errorf("解析 %s 失败: %w", path, err)
	}
	return &info, nil
}

// removePIDFile 删除 PID 文件
func removePIDFile(path string) {
	_ = os.Remove(path)
}

// processAlive 进程是否存活（PID 文件指向的进程）
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// Unix: signal 0 探测；Windows: FindProcess 总是成功，用 OpenProcess 判活（见 proc_windows.go）
	return processSignal0(p)
}

// newShutdownToken 生成随机 shutdown token（-stop / /api/shutdown 校验用）
func newShutdownToken() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// openLogFile 打开（创建）日志文件追加写，返回文件句柄
func openLogFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
}

// logTail 读取日志文件最后 n 行（设置页查看用）
func logTail(path string, n int) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n"), nil
}

// parseAddrPort 从监听地址 "host:port" 提取端口（供 -stop 用）
func parseAddrPort(addr string) string {
	_, port, err := splitHostPort(addr)
	if err != nil {
		return ""
	}
	return port
}

// splitHostPort 兼容 net.SplitHostPort（避免 import net 造成循环依赖担忧，直接内联）
func splitHostPort(addr string) (host, port string, err error) {
	i := strings.LastIndexByte(addr, ':')
	if i < 0 {
		return "", "", fmt.Errorf("地址缺少端口: %s", addr)
	}
	return addr[:i], addr[i+1:], nil
}

// strToInt 便捷转换
func strToInt(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}
