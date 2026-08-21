//go:build windows

package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// procShellExecuteW shell32.ShellExecuteW（UAC 提权用）
var procShellExecuteW = windows.NewLazySystemDLL("shell32.dll").NewProc("ShellExecuteW")

// ensureAdmin 检测当前进程是否管理员（UAC 提权视角）。
// 非管理员时用 ShellExecute(verb=runas) 以管理员身份重新启动自身，返回 (true, nil)，调用方应退出；
// 已是管理员返回 (false, nil) 继续执行。用于 -service install/uninstall 自动弹 UAC 确认框。
func ensureAdmin() (bool, error) {
	if windows.GetCurrentProcessToken().IsElevated() {
		return false, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return false, err
	}
	// 重新拼接原参数（含引号转义，settings 路径等可能含空格）
	args := ""
	for i, a := range os.Args[1:] {
		if i > 0 {
			args += " "
		}
		args += windows.EscapeArg(a)
	}
	dir, _ := os.Getwd()
	r1, _, err := procShellExecuteW.Call(
		0, // hwnd：无父窗口
		uintptr(unsafe.Pointer(windows.StringToUTF16Ptr("runas"))), // verb：提权
		uintptr(unsafe.Pointer(windows.StringToUTF16Ptr(exe))),
		uintptr(unsafe.Pointer(windows.StringToUTF16Ptr(args))),
		uintptr(unsafe.Pointer(windows.StringToUTF16Ptr(dir))),
		1, // SW_SHOWNORMAL
	)
	// ShellExecute 返回值 <= 32 表示失败（含用户取消 UAC 弹窗）
	if r1 <= 32 {
		return false, fmt.Errorf("请求管理员权限失败（可能取消了 UAC 弹窗）: %v", err)
	}
	return true, nil
}

// serviceName Windows 服务名
const serviceName = "yj-cloudos-ops"

// installService 把当前程序注册为 Windows 服务：
// 开机自启（StartAutomatic）+ 崩溃自动重启（SetRecoveryActions）。
// 服务由服务控制管理器（SCM）托管，独立于用户会话 —— 用户注销/退出后服务照常运行。
func installService(settingsPath string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	abs, err := filepath.Abs(settingsPath)
	if err != nil {
		return err
	}
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()

	s, err := m.CreateService(serviceName, exe, mgr.Config{
		DisplayName: "yj-cloudos-ops Web 服务",
		Description: "CloudOS 云主机检查工具 Web 服务（浏览器管理配置/运行/导出）。作为 Windows 服务运行，独立于用户会话，用户注销/退出后仍保持运行。",
		StartType:   mgr.StartAutomatic,
	}, "-service", "run", "-web-settings", abs)
	if err != nil {
		return err
	}
	defer s.Close()

	// 崩溃自动重启：首次失败 3 秒后重启，再次失败 60 秒后重启（系统版本较旧不支持时仅提示）
	if err := s.SetRecoveryActions([]mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 3 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 60 * time.Second},
	}, 0); err != nil {
		fmt.Fprintf(os.Stderr, "提示: 设置崩溃自动重启失败（不影响安装）: %v\n", err)
	}

	fmt.Fprintf(os.Stderr, "服务已安装: %s\n", serviceName)
	fmt.Fprintf(os.Stderr, "  可执行文件: %s\n", exe)
	fmt.Fprintf(os.Stderr, "  设置文件: %s\n", abs)
	fmt.Fprintf(os.Stderr, "启动服务: net start %s（或服务管理器）\n", serviceName)
	fmt.Fprintf(os.Stderr, "停止服务: net stop %s（或服务管理器）\n", serviceName)
	fmt.Fprintf(os.Stderr, "卸载服务: %s -service uninstall\n", filepath.Base(exe))
	return nil
}

// uninstallService 停止并删除服务
func uninstallService() error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(serviceName)
	if err != nil {
		return fmt.Errorf("服务未安装（%s）: %v", serviceName, err)
	}
	defer s.Close()

	// 先尝试停止（未运行时忽略）
	if _, err := s.Control(svc.Stop); err == nil {
		fmt.Fprintf(os.Stderr, "正在停止服务...\n")
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			st, qerr := s.Query()
			if qerr == nil && st.State == svc.Stopped {
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
	}
	if err := s.Delete(); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "服务已卸载: %s\n", serviceName)
	return nil
}

// runWebAsService 以 Windows 服务方式运行 Web 服务（-service run，由 SCM 拉起）
func runWebAsService(settingsPath string) error {
	return svc.Run(serviceName, &webServiceHandler{settingsPath: settingsPath})
}

// webServiceHandler 服务主体：启动 Web 服务，收到停止/关机请求时优雅退出
type webServiceHandler struct {
	settingsPath string
}

func (h *webServiceHandler) Execute(args []string, req <-chan svc.ChangeRequest, changes chan<- svc.Status) (bool, uint32) {
	changes <- svc.Status{State: svc.StartPending}

	// SCM 启动服务时工作目录是 C:\Windows\System32，
	// 先切到设置文件所在目录，保证 configs/files/web.log 等相对路径解析正确
	if dir := filepath.Dir(h.settingsPath); dir != "" {
		if err := os.Chdir(dir); err != nil {
			fmt.Fprintf(os.Stderr, "切换工作目录失败: %v\n", err)
			return false, 1
		}
	}

	s, err := newWebServer(h.settingsPath, "")
	if err != nil {
		fmt.Fprintf(os.Stderr, "服务启动失败: %v\n", err)
		return false, 1
	}
	// 服务进程无控制台，重定向日志到 web.log
	if lf, err := openLogFile(filepath.Join(filepath.Dir(h.settingsPath), "web.log")); err == nil {
		os.Stdout = lf
		os.Stderr = lf
		defer lf.Close()
	}

	addr := s.settings.Addr
	if addr == "" {
		addr = "0.0.0.0:8080"
	}
	fmt.Fprintf(os.Stderr, "yj-cloudos-ops 服务启动\n")
	fmt.Fprintf(os.Stderr, "  监听地址: http://%s\n", addr)
	fmt.Fprintf(os.Stderr, "  设置文件: %s\n", h.settingsPath)
	fmt.Fprintf(os.Stderr, "  配置目录: %s\n", s.settings.ConfigsDir)
	fmt.Fprintf(os.Stderr, "  文件目录: %s\n", s.settings.FilesDir)

	srv := &http.Server{Addr: addr, Handler: s.routes()}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.ListenAndServe() }()

	changes <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for {
		select {
		case c := <-req:
			switch c.Cmd {
			case svc.Interrogate:
				changes <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				// 优雅退出：停当前任务 -> 关 HTTP -> 返回（SCM 标记停止完成）
				changes <- svc.Status{State: svc.StopPending}
				s.mu.Lock()
				if s.currentJob != nil && s.currentJob.Status == "running" {
					s.currentJob.Status = "failed"
					s.currentJob.Error = "服务停止，任务已终止"
				}
				s.mu.Unlock()
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				_ = srv.Shutdown(ctx)
				return false, 0
			}
		case err := <-serveErr:
			if err != nil && err != http.ErrServerClosed {
				fmt.Fprintf(os.Stderr, "HTTP 服务启动失败: %v\n", err)
				return false, 1
			}
			return false, 0
		}
	}
}
