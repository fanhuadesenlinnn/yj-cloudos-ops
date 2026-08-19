package main

import (
	"os"
	"strings"
	"testing"
)

// 端到端测试：连接真实 sshd 验证登录 + 服务状态采集 + 脚本执行
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
	cfg.SSH.Services = []string{"sshd", "docker", "nonexistent-svc"}
	cfg.SSH.Script = "echo script-ok; hostname"
	cfg.SSH.ScriptTimeout = "5s"

	status, services, uploads, script, err := trySSH(cfg, "127.0.0.1", "Test@12345")
	if err != nil {
		t.Fatalf("trySSH 失败: %v", err)
	}
	_ = uploads
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
	t.Logf("script: %+v", script)
	if script == nil || !script.OK || script.ExitCode != 0 || script.State != "success" {
		t.Errorf("脚本应执行成功: %+v", script)
	}
	if !strings.Contains(script.Output, "script-ok") {
		t.Errorf("脚本输出缺失: %q", script.Output)
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
	cfg.SSH.Script = "echo before-fail; exit 7"
	cfg.SSH.ScriptTimeout = "5s"

	_, _, _, script, err := trySSH(cfg, "127.0.0.1", "Test@12345")
	if err != nil {
		t.Fatalf("trySSH 失败: %v", err)
	}
	if script == nil || script.OK || script.ExitCode != 7 || script.State != "fail" {
		t.Errorf("脚本应标记失败且退出码为7: %+v", script)
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
	cfg.SSH.Script = "sleep 30"
	cfg.SSH.ScriptTimeout = "1s"

	_, _, _, script, err := trySSH(cfg, "127.0.0.1", "Test@12345")
	if err != nil {
		t.Fatalf("trySSH 失败: %v", err)
	}
	if script == nil || script.OK || script.State != "timeout" {
		t.Errorf("脚本应标记超时失败: %+v", script)
	}
	if !strings.Contains(script.Error, "超时") {
		t.Errorf("错误信息应包含超时: %q", script.Error)
	}
}
