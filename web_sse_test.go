package main

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

// SSE 事件订阅/发布
func TestEventHubPublishSubscribe(t *testing.T) {
	h := newEventHub()
	ch := h.subscribe("job1")
	defer h.unsubscribe("job1", ch)

	h.publish("job1", "progress", map[string]any{"done": 1, "total": 5})
	select {
	case ev := <-ch:
		if ev.Type != "progress" {
			t.Errorf("事件类型错误: %s", ev.Type)
		}
		var m map[string]any
		json.Unmarshal(ev.Data, &m)
		if m["done"] != float64(1) || m["total"] != float64(5) {
			t.Errorf("事件数据错误: %v", m)
		}
	case <-time.After(time.Second):
		t.Fatal("未收到事件")
	}
	// 其他 job 的事件不投递
	h.publish("job2", "progress", map[string]any{})
	select {
	case <-ch:
		t.Fatal("不应收到其他 job 的事件")
	case <-time.After(50 * time.Millisecond):
	}
	// 取消订阅后不再收到
	h.unsubscribe("job1", ch)
	h.publish("job1", "progress", map[string]any{})
	select {
	case <-ch:
		t.Fatal("取消订阅后不应收到事件")
	case <-time.After(50 * time.Millisecond):
	}
}

// Web 模式进度 sink：runSSHTests 使用带 sink 的 progressMgr 时，状态变化应触发 sink
func TestProgressSinkCalledInRunSSHTests(t *testing.T) {
	addr := startInProcSSHServer(t, "Test@12345")
	cfg := inProcSSHCfg(addr)
	cfg.SSH.Workers = 2
	cfg.ExecList = []ExecStep{commandStep("remote", "echo hi")}

	var mu sync.Mutex
	lines := []string{}
	prog := newProgressMgr(3, func(p *progressMgr) {
		mu.Lock()
		lines = append(lines, p.line())
		mu.Unlock()
	})
	var vmLogs []string
	vms := []*VM{
		{Name: "h1", IP: "127.0.0.1", Password: "Test@12345"},
		{Name: "h2", IP: "127.0.0.1", Password: "Test@12345"},
		{Name: "h3", IP: "127.0.0.1", Password: "Test@12345"},
	}
	runSSHTests(cfg, vms, nil, false, prog, func(vm *VM) { vmLogs = append(vmLogs, vm.SSHResult) }, false)

	if len(vmLogs) != 3 {
		t.Errorf("onVM 应每台调用一次: %d", len(vmLogs))
	}
	mu.Lock()
	defer mu.Unlock()
	if len(lines) == 0 {
		t.Fatal("sink 应被调用")
	}
	// 最后一次应显示完成
	last := lines[len(lines)-1]
	if !strings.Contains(last, "[3/3] 100%") {
		t.Errorf("最终进度应为 100%%: %q", last)
	}
	// 应出现过执行中状态
	hasActive := false
	for _, l := range lines {
		if strings.Contains(l, "执行中") {
			hasActive = true
			break
		}
	}
	if !hasActive {
		t.Error("进度应出现过执行中状态")
	}
	// 完成后全局进度控制器应复位（不影响后续）
	if sshProgress != nil {
		t.Error("runSSHTests 结束后 sshProgress 应复位")
	}
}
