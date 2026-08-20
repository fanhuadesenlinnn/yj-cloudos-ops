package main

import (
	"strings"
	"testing"
	"time"
)

// 端到端视觉验证：in-proc SSH server + runSSHTests 并发执行，观察实时进度输出。
// 仅作人工目验（go test -v 时可见 stderr 刷新行），断言只检查完成状态。
func TestProgressRunSSHTestsE2E(t *testing.T) {
	addr := startInProcSSHServer(t, "Test@12345")
	cfg := inProcSSHCfg(addr)
	cfg.SSH.Workers = 3
	cfg.ExecList = []ExecStep{
		commandStep("remote", "echo step1; sleep 0.5"),
		commandStep("remote", "echo step2; sleep 0.5"),
	}

	var vms []*VM
	for i := 0; i < 6; i++ {
		vms = append(vms, &VM{
			ID:       string(rune('a' + i)),
			Name:     "host-0" + string(rune('1'+i)),
			Type:     "虚拟机",
			IP:       "127.0.0.1",
			Password: "Test@12345",
		})
	}
	runSSHTests(cfg, vms, nil, false)

	for i, vm := range vms {
		if vm.SSHResult == "" || !strings.HasPrefix(vm.SSHResult, "✓") {
			t.Errorf("主机%d 登录应成功: %q", i, vm.SSHResult)
		}
		if len(vm.ExecSteps) != 2 {
			t.Errorf("主机%d 应有2个步骤结果: %d", i, len(vm.ExecSteps))
		}
	}
	if sshProgress != nil {
		t.Error("runSSHTests 结束后 sshProgress 应复位为 nil")
	}
	time.Sleep(50 * time.Millisecond)
}
