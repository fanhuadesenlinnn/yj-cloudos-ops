package main

import (
	"crypto/rand"
	"crypto/rsa"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// 进程内 SSH 服务器：用于在无外部 sshd 的情况下端到端验证 trySSH/runPipeline
// （stdin 传脚本、退出码、超时中断、会话中断）。仅支持 session 通道的 exec 请求。

// inProcOpts 测试服务器选项
type inProcOpts struct {
	AbortBashS bool   // 对 "bash -s"（脚本执行）不返回退出码直接关会话，模拟 init 0/reboot 掐断
	SFTPDir    string // 非空时支持 sftp subsystem，并以此目录为服务器工作目录（相对路径映射到此目录下）
}

func startInProcSSHServer(t *testing.T, password string) string {
	t.Helper()
	return startInProcSSHServerOpts(t, password, inProcOpts{})
}

func startInProcSSHServerOpts(t *testing.T, password string, opts inProcOpts) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("生成主机密钥失败: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatalf("创建签名器失败: %v", err)
	}
	config := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			if string(pass) == password {
				return nil, nil
			}
			return nil, fmt.Errorf("password rejected")
		},
	}
	config.AddHostKey(signer)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听失败: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go serveSSHConn(conn, config, opts)
		}
	}()
	return ln.Addr().String()
}

func serveSSHConn(nConn net.Conn, config *ssh.ServerConfig, opts inProcOpts) {
	_, chans, reqs, err := ssh.NewServerConn(nConn, config)
	if err != nil {
		return
	}
	defer nConn.Close()
	go ssh.DiscardRequests(reqs)
	for newChannel := range chans {
		if newChannel.ChannelType() != "session" {
			newChannel.Reject(ssh.UnknownChannelType, "unknown channel type")
			continue
		}
		channel, requests, err := newChannel.Accept()
		if err != nil {
			continue
		}
		go serveSession(channel, requests, opts)
	}
}

func serveSession(channel ssh.Channel, requests <-chan *ssh.Request, opts inProcOpts) {
	defer channel.Close()
	for req := range requests {
		switch req.Type {
		case "exec":
			var payload struct{ Command string }
			if err := ssh.Unmarshal(req.Payload, &payload); err != nil {
				req.Reply(false, nil)
				continue
			}
			req.Reply(true, nil)
			if opts.AbortBashS && payload.Command == "bash -s" {
				// 模拟 init 0/reboot：写入少量输出后直接关会话，不发送 exit-status
				channel.Write([]byte("shutting down...\n"))
				channel.CloseWrite()
				return
			}
			serveRemoteCommand(channel, payload.Command)
			// 模拟真实 sshd：命令结束后发送 EOF 并关闭会话，
			// 否则客户端 Wait() 会一直等 stdout/stderr 拷贝协程结束
			channel.CloseWrite()
			return
		case "subsystem":
			var payload struct{ Name string }
			if err := ssh.Unmarshal(req.Payload, &payload); err != nil {
				req.Reply(false, nil)
				continue
			}
			if payload.Name != "sftp" || opts.SFTPDir == "" {
				req.Reply(false, nil)
				continue
			}
			req.Reply(true, nil)
			// 用 pkg/sftp 的服务器实现：相对路径映射到 SFTPDir 下，绝对路径原样传递（与真实 sshd 一致）
			server, err := sftp.NewServer(channel, sftp.WithServerWorkingDirectory(opts.SFTPDir))
			if err != nil {
				return
			}
			server.Serve()
			return
		default:
			req.Reply(false, nil) // 不支持 pty-req / shell 等
		}
	}
}

// serveRemoteCommand 用 bash -c 执行命令，stdin/stdout/stderr 接到 channel，最后发送 exit-status。
// 注意: stdin 必须用 os.Pipe + 独立 goroutine 桥接——若直接 cmd.Stdin = channel，
// os/exec 的 Wait() 会等待 stdin 拷贝协程结束，而客户端执行不读 stdin 的命令时
// 从不发送 stdin 数据/EOF，会导致服务端与客户端互相死锁（真实 sshd 不 join 该拷贝，无此问题）。
func serveRemoteCommand(channel ssh.Channel, command string) {
	cmd := exec.Command("bash", "-c", command)
	pr, pw, err := os.Pipe()
	if err == nil {
		cmd.Stdin = pr
		go func() {
			io.Copy(pw, channel) // 客户端关闭写端(EOF)后 io.Copy 返回，关闭管道
			pw.Close()
		}()
	}
	cmd.Stdout = channel
	cmd.Stderr = channel
	err = cmd.Run()
	if pr != nil {
		pr.Close()
	}
	status := uint32(0)
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			status = uint32(ee.ExitCode())
		} else {
			status = 1
		}
	}
	channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{status}))
}

// inProcSSHCfg 构造指向进程内 SSH 服务器的配置（默认流水线为空，测试各自配置 execList）
func inProcSSHCfg(addr string) *Config {
	cfg := &Config{}
	cfg.SSH.Username = "root"
	_, portStr, _ := net.SplitHostPort(addr)
	var port int
	fmt.Sscanf(portStr, "%d", &port)
	cfg.SSH.Port = port
	cfg.SSH.Timeout = "5s"
	cfg.SSH.VerifyCommand = "echo ok"
	return cfg
}

func boolPtr(b bool) *bool { return &b }

// scriptStep 便捷构造 script 步骤
func commandStep(target, content string) ExecStep {
	return ExecStep{Name: "命令", Type: "command", Target: target, Script: content, Timeout: "5s"}
}

// 脚本内容通过 stdin 传给远端 bash -s，正常输出与退出码
func TestScriptExecInProc(t *testing.T) {
	addr := startInProcSSHServer(t, "Test@12345")
	cfg := inProcSSHCfg(addr)
	cfg.ExecList = []ExecStep{commandStep("remote", "echo hello-from-script; echo '含 中文 和 \"引号\" $符号'")}

	_, _, steps, err := trySSH(cfg, "127.0.0.1", "Test@12345", nil, false)
	if err != nil {
		t.Fatalf("trySSH 失败: %v", err)
	}
	if len(steps) != 1 {
		t.Fatalf("应有一个步骤结果: %+v", steps)
	}
	s := steps[0]
	if s == nil || s.State != "success" || s.ExitCode != 0 {
		t.Fatalf("脚本应成功: %+v", s)
	}
	if !strings.Contains(s.Output, "hello-from-script") {
		t.Errorf("脚本输出缺失: %q", s.Output)
	}
	if !strings.Contains(s.Output, "含 中文") {
		t.Errorf("中文/特殊字符输出缺失: %q", s.Output)
	}
	t.Logf("脚本输出: %s", s.Output)
}

// scriptPath 读取 Windows CRLF 脚本：换行符归一化为 \n 后远端 bash 才能正确执行
// （修复前 \r 残留会导致 if/then 语法错误、变量尾随 \r）
func TestScriptPathCRLFInProc(t *testing.T) {
	addr := startInProcSSHServer(t, "Test@12345")
	cfg := inProcSSHCfg(addr)

	// 条件用所有平台都成立的内置变量（/etc/hostname 仅 Linux 存在，macOS 上条件为假导致输出为空），
	// 测试目的是验证 CRLF 归一化后 if/then 语法可正常执行。
	path := filepath.Join(t.TempDir(), "deploy.sh")
	if err := os.WriteFile(path, []byte("if [ -n \"$PATH\" ]\r\n"+
		"then\r\n"+
		"  echo crlf-ok\r\n"+
		"fi\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg.ExecList = []ExecStep{{Name: "CRLF脚本", Type: "command", Target: "remote", ScriptPath: path, Timeout: "5s"}}

	_, _, steps, err := trySSH(cfg, "127.0.0.1", "Test@12345", nil, false)
	if err != nil {
		t.Fatalf("trySSH 失败: %v", err)
	}
	s := steps[0]
	if s == nil || s.State != "success" || s.ExitCode != 0 {
		t.Fatalf("CRLF 脚本应成功执行: %+v", s)
	}
	if !strings.Contains(s.Output, "crlf-ok") {
		t.Errorf("CRLF 脚本输出缺失: %q", s.Output)
	}
}

// 非零退出码：标记失败(State=fail)但登录结果不受影响
func TestScriptExecNonZeroExitInProc(t *testing.T) {
	addr := startInProcSSHServer(t, "Test@12345")
	cfg := inProcSSHCfg(addr)
	cfg.ExecList = []ExecStep{commandStep("remote", "echo before-fail; exit 42")}

	result, _, _, steps := testOne(cfg, &VM{IP: "127.0.0.1", Password: "Test@12345"}, nil, false)
	if !strings.HasPrefix(result, "✓") {
		t.Errorf("登录结果不应受脚本失败影响: %q", result)
	}
	s := steps[0]
	if s == nil || s.State != "fail" || s.ExitCode != 42 {
		t.Errorf("脚本应标记失败且退出码42: %+v", s)
	}
}

// 会话中断（模拟 init 0/reboot 掐断连接、无退出码）：标记 interrupted，登录结果不受影响
func TestScriptExecInterruptedInProc(t *testing.T) {
	addr := startInProcSSHServerOpts(t, "Test@12345", inProcOpts{AbortBashS: true})
	cfg := inProcSSHCfg(addr)
	cfg.ExecList = []ExecStep{commandStep("remote", "echo before-interrupt")}

	result, _, _, steps := testOne(cfg, &VM{IP: "127.0.0.1", Password: "Test@12345"}, nil, false)
	if !strings.HasPrefix(result, "✓") {
		t.Errorf("登录结果不应受会话中断影响: %q", result)
	}
	s := steps[0]
	if s == nil || s.State != "interrupted" {
		t.Errorf("脚本应标记会话中断: %+v", s)
	}
	if s.ExitCode != -1 {
		t.Errorf("会话中断退出码应为-1: %+v", s)
	}
	if !strings.Contains(s.Output, "shutting down") {
		t.Errorf("应保留中断前已收到的输出: %q", s.Output)
	}
}

// 超时：强制中断并标记超时(State=timeout)，登录结果不受影响
func TestScriptExecTimeoutInProc(t *testing.T) {
	addr := startInProcSSHServer(t, "Test@12345")
	cfg := inProcSSHCfg(addr)
	step := commandStep("remote", "sleep 30; echo never")
	step.Timeout = "1s"
	cfg.ExecList = []ExecStep{step}

	start := time.Now()
	result, _, _, steps := testOne(cfg, &VM{IP: "127.0.0.1", Password: "Test@12345"}, nil, false)
	if err := time.Since(start); err > 5*time.Second {
		t.Errorf("超时未生效，耗时: %v", err)
	}
	if !strings.HasPrefix(result, "✓") {
		t.Errorf("登录结果不应受脚本超时影响: %q", result)
	}
	s := steps[0]
	if s == nil || s.State != "timeout" {
		t.Errorf("脚本应标记超时失败: %+v", s)
	}
	if !strings.Contains(s.Error, "超时") {
		t.Errorf("错误信息应包含超时: %q", s.Error)
	}
}

// 认证失败时不应有步骤结果
func TestScriptNotRunOnAuthFailInProc(t *testing.T) {
	addr := startInProcSSHServer(t, "Test@12345")
	cfg := inProcSSHCfg(addr)
	cfg.ExecList = []ExecStep{commandStep("remote", "echo never-runs")}

	_, _, steps, err := trySSH(cfg, "127.0.0.1", "wrong-password", nil, false)
	if err == nil {
		t.Fatalf("密码错误应登录失败")
	}
	if steps != nil {
		t.Errorf("登录失败时不应有步骤结果: %+v", steps)
	}
}

// 流水线顺序执行 + onError=stop：第1步失败则第2步不执行（skipped）
func TestPipelineStopOnErrorInProc(t *testing.T) {
	addr := startInProcSSHServer(t, "Test@12345")
	cfg := inProcSSHCfg(addr)
	cfg.ExecList = []ExecStep{
		commandStep("remote", "echo fail-first; exit 3"),
		commandStep("remote", "echo should-not-run"),
	}

	_, _, steps, err := trySSH(cfg, "127.0.0.1", "Test@12345", nil, false)
	if err != nil {
		t.Fatalf("trySSH 失败: %v", err)
	}
	if steps[0].State != "fail" {
		t.Errorf("第1步应失败: %+v", steps[0])
	}
	if steps[1].State != "skipped" {
		t.Errorf("第2步应被跳过(上游失败): %+v", steps[1])
	}
}

// onError=continue：第1步失败后第2步照常执行
func TestPipelineContinueOnErrorInProc(t *testing.T) {
	addr := startInProcSSHServer(t, "Test@12345")
	cfg := inProcSSHCfg(addr)
	cfg.ExecList = []ExecStep{
		{Name: "失败步骤", Type: "command", Target: "remote", Script: "exit 9", Timeout: "5s", OnError: "continue"},
		commandStep("remote", "echo still-runs"),
	}

	_, _, steps, err := trySSH(cfg, "127.0.0.1", "Test@12345", nil, false)
	if err != nil {
		t.Fatalf("trySSH 失败: %v", err)
	}
	if steps[0].State != "fail" {
		t.Errorf("第1步应失败: %+v", steps[0])
	}
	if steps[1].State != "success" {
		t.Errorf("onError=continue 时第2步应照常执行: %+v", steps[1])
	}
}

// 本地 once 步骤：阶段一执行一次，每台机器复用结果
func TestPipelineLocalOnceInProc(t *testing.T) {
	addr := startInProcSSHServer(t, "Test@12345")
	cfg := inProcSSHCfg(addr)
	cfg.ExecList = []ExecStep{
		{Name: "本地准备", Type: "command", Target: "local", Run: "once", Command: "echo local-once", Timeout: "5s"},
		commandStep("remote", "echo remote-step"),
	}

	onceResults, stopped := runPipelineOnce(cfg)
	if stopped {
		t.Fatalf("本地步骤不应终止流水线")
	}
	if onceResults[0] == nil || onceResults[0].State != "success" {
		t.Fatalf("本地 once 步骤应成功: %+v", onceResults[0])
	}
	if !strings.Contains(onceResults[0].Output, "local-once") {
		t.Errorf("本地步骤输出缺失: %q", onceResults[0].Output)
	}

	_, _, steps, err := trySSH(cfg, "127.0.0.1", "Test@12345", onceResults, false)
	if err != nil {
		t.Fatalf("trySSH 失败: %v", err)
	}
	if len(steps) != 2 {
		t.Fatalf("应有2个步骤结果: %+v", steps)
	}
	if steps[0] != onceResults[0] {
		t.Errorf("本地 once 步骤应复用阶段一结果")
	}
	if steps[1].State != "success" || !strings.Contains(steps[1].Output, "remote-step") {
		t.Errorf("远端步骤应执行成功: %+v", steps[1])
	}
}

// 本地 once 步骤失败且 onError=stop：全局终止，远端步骤全部 skipped
func TestPipelineLocalOnceFailStopsGlobal(t *testing.T) {
	addr := startInProcSSHServer(t, "Test@12345")
	cfg := inProcSSHCfg(addr)
	cfg.ExecList = []ExecStep{
		{Name: "本地构建", Type: "command", Target: "local", Run: "once", Command: "exit 5", Timeout: "5s"},
		commandStep("remote", "echo never"),
	}

	onceResults, stopped := runPipelineOnce(cfg)
	if !stopped {
		t.Fatalf("本地步骤失败应全局终止")
	}
	if onceResults[0].State != "fail" {
		t.Errorf("本地步骤应失败: %+v", onceResults[0])
	}

	_, _, steps, err := trySSH(cfg, "127.0.0.1", "Test@12345", onceResults, true)
	if err != nil {
		t.Fatalf("trySSH 失败: %v", err)
	}
	if steps[0] != onceResults[0] {
		t.Errorf("本地 once 步骤应复用阶段一结果")
	}
	if steps[1].State != "skipped" {
		t.Errorf("全局终止后远端步骤应 skipped: %+v", steps[1])
	}
}

// 显式空 execList（execList: []）：只测 SSH 连通性，不执行任何步骤
func TestEmptyExecListInProc(t *testing.T) {
	addr := startInProcSSHServer(t, "Test@12345")
	cfg := inProcSSHCfg(addr)
	cfg.ExecList = []ExecStep{} // 显式空流水线

	result, _, _, steps := testOne(cfg, &VM{IP: "127.0.0.1", Password: "Test@12345"}, nil, false)
	if !strings.HasPrefix(result, "✓") {
		t.Errorf("登录应成功: %q", result)
	}
	if len(steps) != 0 {
		t.Errorf("显式空流水线不应有步骤结果: %+v", steps)
	}
}

// 默认流水线（未配置 execList）：登录后自动执行 status -> services 两步
func TestDefaultPipelineInProc(t *testing.T) {
	addr := startInProcSSHServer(t, "Test@12345")
	cfg := inProcSSHCfg(addr) // 不配置 ExecList

	_, _, steps, err := trySSH(cfg, "127.0.0.1", "Test@12345", nil, false)
	if err != nil {
		t.Fatalf("trySSH 失败: %v", err)
	}
	if len(steps) != 2 {
		t.Fatalf("默认流水线应为2步: %+v", steps)
	}
	if steps[0].Type != "status" || steps[1].Type != "services" {
		t.Errorf("默认流水线顺序错误: %+v", steps)
	}
}

// 本地脚本：成功/失败/超时
func TestLocalScriptInProc(t *testing.T) {
	// 成功
	res := runLocalCommand(ExecStep{Name: "本地", Type: "command", Target: "local", Command: "echo hello-local; exit 0", Timeout: "5s"}, "本地")
	if res.State != "success" || res.ExitCode != 0 || !strings.Contains(res.Output, "hello-local") {
		t.Errorf("本地脚本应成功: %+v", res)
	}
	// 失败
	res = runLocalCommand(ExecStep{Name: "本地", Type: "command", Target: "local", Command: "echo oops; exit 7", Timeout: "5s"}, "本地")
	if res.State != "fail" || res.ExitCode != 7 {
		t.Errorf("本地脚本应失败 exit7: %+v", res)
	}
	// 超时：应快速返回（进程组被杀，不会等 sleep 自然结束）
	start := time.Now()
	res = runLocalCommand(ExecStep{Name: "本地", Type: "command", Target: "local", Command: "sleep 30", Timeout: "1s"}, "本地")
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("本地脚本超时应快速返回，实际耗时: %v", elapsed)
	}
	if res.State != "timeout" || !strings.Contains(res.Error, "超时") {
		t.Errorf("本地脚本应超时: %+v", res)
	}
	// 空内容
	res = runLocalCommand(ExecStep{Name: "本地", Type: "command", Target: "local"}, "本地")
	if res.State != "error" {
		t.Errorf("空内容应报错: %+v", res)
	}
}
