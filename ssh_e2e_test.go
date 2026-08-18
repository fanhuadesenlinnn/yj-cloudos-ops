package main

import (
	"os"
	"testing"
)

// 端到端测试：连接真实 sshd 验证登录 + 服务状态采集
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

	status, services, err := trySSH(cfg, "127.0.0.1", "Test@12345")
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
}
