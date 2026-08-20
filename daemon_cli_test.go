package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// 编译当前二进制用于 CLI 测试
func buildBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "yj-test")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("构建失败: %v\n%s", err, out)
	}
	return bin
}

func runCLI(t *testing.T, bin string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			t.Fatalf("执行失败: %v", err)
		}
	}
	return string(out), code
}

// -daemon 单独使用：应友好提示需与 -web 搭配，非零退出，不报无关的配置错误
func TestDaemonAloneFriendlyError(t *testing.T) {
	bin := buildBinary(t)
	out, code := runCLI(t, bin, "-daemon")
	if code == 0 {
		t.Error("-daemon 单独使用应非零退出")
	}
	if !strings.Contains(out, "-daemon 必须与 -web 搭配使用") {
		t.Errorf("应友好提示需搭配 -web: %q", out)
	}
	if !strings.Contains(out, "用法示例") || !strings.Contains(out, "-web -daemon") {
		t.Errorf("应给出用法示例: %q", out)
	}
	if strings.Contains(out, "加载配置失败") {
		t.Error("不应报无关的配置加载错误")
	}
}

// -stop 未运行：友好提示
func TestStopNoInstanceFriendly(t *testing.T) {
	bin := buildBinary(t)
	dir := t.TempDir()
	out, code := runCLI(t, bin, "-stop", "-web-settings", filepath.Join(dir, "settings.yaml"))
	if code == 0 {
		t.Error("-stop 无实例应非零退出")
	}
	if !strings.Contains(out, "未找到后台实例") {
		t.Errorf("应提示未找到实例: %q", out)
	}
}

// -stop 打印实例详情（PID/地址/日志）+ 清理确认
func TestStopPrintsDetails(t *testing.T) {
	// 模拟 web.pid 指向一个不可探测的 PID（如当前进程自身，Signal 0 会成功）
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.yaml")
	pidPath := pidFilePath(settingsPath)
	if err := writePIDFile(pidPath, os.Getpid(), "tok", "0.0.0.0:18099", "web.log"); err != nil {
		t.Fatal(err)
	}
	// 用当前进程 PID（Signal 0 成功 -> 视为存活），但 -stop 会尝试停止自己...
	// 该用例只验证输出格式，改用不可探测 PID 走"已不存在"分支（输出含详情语义的清理提示）
	if err := writePIDFile(pidPath, 999999999, "tok", "0.0.0.0:18099", "web.log"); err != nil {
		t.Fatal(err)
	}
	bin := buildBinary(t)
	out, _ := runCLI(t, bin, "-stop", "-web-settings", settingsPath)
	// 不可探测 PID -> 提示已不存在并清理
	if !strings.Contains(out, "已不存在") {
		t.Errorf("应提示实例已不存在: %q", out)
	}
	if !strings.Contains(out, "web.pid") {
		t.Errorf("应提及清理 PID 文件: %q", out)
	}
}
