package main

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// 检查模式：SSH 连通成功但只返回连通性结果，不执行流水线步骤（无副作用）
func TestCheckOnlyNoPipelineExec(t *testing.T) {
	addr := startInProcSSHServer(t, "Test@12345")
	cfg := inProcSSHCfg(addr)
	cfg.ExecList = []ExecStep{commandStep("remote", "echo should-not-run; touch /tmp/evil-marker")}

	// 检查模式（checkOnly=true）
	_, _, steps, err := trySSH(cfg, "127.0.0.1", "Test@12345", nil, false, true)
	if err != nil {
		t.Fatalf("trySSH 失败: %v", err)
	}
	if len(steps) != 0 {
		t.Errorf("检查模式不应执行流水线步骤: %+v", steps)
	}

	// 正常模式（checkOnly=false）应执行步骤
	_, _, steps2, err := trySSH(cfg, "127.0.0.1", "Test@12345", nil, false, false)
	if err != nil {
		t.Fatalf("trySSH 失败: %v", err)
	}
	if len(steps2) != 1 || steps2[0].State != "success" {
		t.Errorf("正常模式应执行流水线: %+v", steps2)
	}
}

// 检查模式 + runSSHTests：并发跑多台，只测连通，每台 steps 为空
func TestCheckOnlyRunSSHTests(t *testing.T) {
	addr := startInProcSSHServer(t, "Test@12345")
	cfg := inProcSSHCfg(addr)
	cfg.SSH.Workers = 2
	cfg.ExecList = []ExecStep{commandStep("remote", "echo nope")}

	vms := []*VM{
		{Name: "h1", IP: "127.0.0.1", Password: "Test@12345"},
		{Name: "h2", IP: "127.0.0.1", Password: "Test@12345"},
	}
	runSSHTests(cfg, vms, nil, false, nil, nil, true)

	for i, vm := range vms {
		if !strings.HasPrefix(vm.SSHResult, "✓") {
			t.Errorf("主机%d 应连通: %q", i, vm.SSHResult)
		}
		if len(vm.ExecSteps) != 0 {
			t.Errorf("主机%d 检查模式不应有步骤: %+v", i, vm.ExecSteps)
		}
	}
}

// 检查模式 + Web job：mode=check 时 runJob 不导出、进度含友好总结
func TestCheckModeJobSummary(t *testing.T) {
	_, h, _ := newTestWebServer(t)
	cookie := doLogin(t, h)
	// 平台不可达：check 模式在解析项目阶段就失败（友好错误）
	yml := "endpoint: https://127.0.0.1:1\naccessKeyId: ak\naccessKeySecret: sk\nregionId: cn-beijing\nproject:\n  names: [\"x\"]\n"
	doJSON(t, h, cookie, "POST", "/api/configs", `{"name":"离线检查","yaml":`+jsonStr(yml)+`}`)

	rec, m := doJSON(t, h, cookie, "POST", "/api/run", `{"profile":"离线检查","mode":"check"}`)
	if rec.Code != 200 {
		t.Fatalf("check 启动应 200: %d %s", rec.Code, rec.Body.String())
	}
	if m["mode"] != "check" {
		t.Errorf("job mode 应为 check: %v", m["mode"])
	}
	jobID := fmt.Sprint(m["id"])
	// 等待结束
	deadline := time.Now().Add(10 * time.Second)
	var summary map[string]any
	for time.Now().Before(deadline) {
		rec, job := doJSON(t, h, cookie, "GET", "/api/result?job="+jobID, "")
		if rec.Code == 200 {
			summary, _ = job["summary"].(map[string]any)
			if summary != nil && summary["status"] != "running" {
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if summary == nil || summary["status"] == "running" {
		t.Fatal("check 任务应已结束")
	}
	if summary["status"] != "failed" {
		t.Errorf("平台不可达 check 应失败: %v", summary)
	}
	// 错误信息应友好（包含“解析项目失败”）
	if err := fmt.Sprint(summary["error"]); !strings.Contains(err, "解析项目失败") {
		t.Errorf("错误信息应友好: %v", err)
	}
}

func fmtAny(v any) string { return fmt.Sprint(v) }
