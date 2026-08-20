package main

import (
	"os"
	"strings"
	"testing"
)

// 端到端测试：连接真实 sshd 验证登录 + 流水线（status/services/script 步骤）
// 运行: E2E_SSH=1 go test -run TestSSHE2E -v .
func TestSSHE2E(t *testing.T) {
	if os.Getenv("E2E_SSH") == "" {
		t.Skip("set E2E_SSH=1 to run")
	}
	cfg := &Config{}
	cfg.SSH.Port = 2222
	cfg.SSH.Username = "root"
	cfg.SSH.Timeout = "5s"
	cfg.SSH.VerifyCommand = "echo ok"
	cfg.ExecList = []ExecStep{
		{Name: "状态", Type: "status", OnError: "continue"},
		{Name: "服务", Type: "services", Services: []string{"sshd", "docker", "nonexistent-svc"}, OnError: "continue"},
		{Name: "命令", Type: "command", Target: "remote", Script: "echo script-ok; hostname", Timeout: "5s"},
	}

	status, services, steps, err := trySSH(cfg, "127.0.0.1", "Test@12345", nil, false)
	if err != nil {
		t.Fatalf("trySSH 失败: %v", err)
	}
	t.Logf("status: %+v", status)
	if status == nil || status.Kernel == "" {
		t.Errorf("运行状态未采集到")
	}
	t.Logf("services: %+v", services)
	if len(services) != 3 {
		t.Errorf("服务数量错误: %d", len(services))
	}
	for _, s := range services {
		if s.Name == "" || s.State == "" {
			t.Errorf("服务字段为空: %+v", s)
		}
	}
	t.Logf("steps: %+v", steps)
	if len(steps) != 3 {
		t.Fatalf("应有3个步骤结果: %+v", steps)
	}
	if steps[0].State != "success" || !strings.Contains(steps[0].Output, "OS") {
		t.Errorf("status 步骤应成功: %+v", steps[0])
	}
	if steps[1].State != "success" || !strings.Contains(steps[1].Output, "sshd=") {
		t.Errorf("services 步骤应成功: %+v", steps[1])
	}
	if steps[2].State != "success" || steps[2].ExitCode != 0 {
		t.Errorf("脚本步骤应执行成功: %+v", steps[2])
	}
	if !strings.Contains(steps[2].Output, "script-ok") {
		t.Errorf("脚本输出缺失: %q", steps[2].Output)
	}
}

// 脚本失败（非零退出码）时不应影响 SSH 登录结果
func TestSSHE2EScriptFail(t *testing.T) {
	if os.Getenv("E2E_SSH") == "" {
		t.Skip("set E2E_SSH=1 to run")
	}
	cfg := &Config{}
	cfg.SSH.Port = 2222
	cfg.SSH.Username = "root"
	cfg.SSH.Timeout = "5s"
	cfg.SSH.VerifyCommand = "echo ok"
	cfg.ExecList = []ExecStep{
		{Name: "命令", Type: "command", Target: "remote", Script: "echo before-fail; exit 7", Timeout: "5s"},
	}

	_, _, steps, err := trySSH(cfg, "127.0.0.1", "Test@12345", nil, false)
	if err != nil {
		t.Fatalf("trySSH 失败: %v", err)
	}
	if steps[0].State != "fail" || steps[0].ExitCode != 7 {
		t.Errorf("脚本应标记失败且退出码为7: %+v", steps[0])
	}
}

// 脚本超时：应被强制中断并标记超时
func TestSSHE2EScriptTimeout(t *testing.T) {
	if os.Getenv("E2E_SSH") == "" {
		t.Skip("set E2E_SSH=1 to run")
	}
	cfg := &Config{}
	cfg.SSH.Port = 2222
	cfg.SSH.Username = "root"
	cfg.SSH.Timeout = "5s"
	cfg.SSH.VerifyCommand = "echo ok"
	cfg.ExecList = []ExecStep{
		{Name: "命令", Type: "command", Target: "remote", Script: "sleep 30", Timeout: "1s"},
	}

	_, _, steps, err := trySSH(cfg, "127.0.0.1", "Test@12345", nil, false)
	if err != nil {
		t.Fatalf("trySSH 失败: %v", err)
	}
	if steps[0].State != "timeout" {
		t.Errorf("脚本应标记超时失败: %+v", steps[0])
	}
	if !strings.Contains(steps[0].Error, "超时") {
		t.Errorf("错误信息应包含超时: %q", steps[0].Error)
	}
}
