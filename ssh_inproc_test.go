package main

import (
	"crypto/rand"
	"crypto/rsa"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// 进程内 SSH 服务器：用于在无外部 sshd 的情况下端到端验证 trySSH/runScript
// （stdin 传脚本、退出码、超时中断、会话中断）。仅支持 session 通道的 exec 请求。

// inProcOpts 测试服务器选项
type inProcOpts struct {
	AbortBashS bool // 对 "bash -s"（脚本执行）不返回退出码直接关会话，模拟 init 0/reboot 掐断
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
			runRemoteCommand(channel, payload.Command)
			// 模拟真实 sshd：命令结束后发送 EOF 并关闭会话，
			// 否则客户端 Wait() 会一直等 stdout/stderr 拷贝协程结束
			channel.CloseWrite()
			return
		default:
			req.Reply(false, nil) // 不支持 pty-req / shell 等
		}
	}
}

// runRemoteCommand 用 bash -c 执行命令，stdin/stdout/stderr 接到 channel，最后发送 exit-status。
// 注意: stdin 必须用 os.Pipe + 独立 goroutine 桥接——若直接 cmd.Stdin = channel，
// os/exec 的 Wait() 会等待 stdin 拷贝协程结束，而客户端执行不读 stdin 的命令时
// 从不发送 stdin 数据/EOF，会导致服务端与客户端互相死锁（真实 sshd 不 join 该拷贝，无此问题）。
func runRemoteCommand(channel ssh.Channel, command string) {
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

// inProcSSHCfg 构造指向进程内 SSH 服务器的配置（只测脚本，关闭状态/服务采集）
func inProcSSHCfg(addr string) *Config {
	cfg := &Config{}
	cfg.SSH.Username = "root"
	_, portStr, _ := net.SplitHostPort(addr)
	var port int
	fmt.Sscanf(portStr, "%d", &port)
	cfg.SSH.Port = port
	cfg.SSH.Timeout = "5s"
	cfg.SSH.VerifyCommand = "echo ok"
	cfg.SSH.CheckStatus = boolPtr(false)
	cfg.SSH.CheckServices = boolPtr(false)
	return cfg
}

func boolPtr(b bool) *bool { return &b }

// 脚本内容通过 stdin 传给远端 bash -s，正常输出与退出码
func TestScriptExecInProc(t *testing.T) {
	addr := startInProcSSHServer(t, "Test@12345")
	cfg := inProcSSHCfg(addr)
	cfg.SSH.Script = "echo hello-from-script; echo '含 中文 和 \"引号\" $符号'"
	cfg.SSH.ScriptTimeout = "5s"

	_, _, script, err := trySSH(cfg, "127.0.0.1", "Test@12345")
	if err != nil {
		t.Fatalf("trySSH 失败: %v", err)
	}
	if script == nil || !script.OK || script.ExitCode != 0 || script.State != "success" {
		t.Fatalf("脚本应成功: %+v", script)
	}
	if !strings.Contains(script.Output, "hello-from-script") {
		t.Errorf("脚本输出缺失: %q", script.Output)
	}
	if !strings.Contains(script.Output, "含 中文") {
		t.Errorf("中文/特殊字符输出缺失: %q", script.Output)
	}
	t.Logf("脚本输出: %s", script.Output)
}

// 非零退出码：标记失败(State=fail)但登录结果不受影响
func TestScriptExecNonZeroExitInProc(t *testing.T) {
	addr := startInProcSSHServer(t, "Test@12345")
	cfg := inProcSSHCfg(addr)
	cfg.SSH.Script = "echo before-fail; exit 42"
	cfg.SSH.ScriptTimeout = "5s"

	result, _, _, script := testOne(cfg, &VM{IP: "127.0.0.1", Password: "Test@12345"})
	if !strings.HasPrefix(result, "✓") {
		t.Errorf("登录结果不应受脚本失败影响: %q", result)
	}
	if script == nil || script.OK || script.ExitCode != 42 || script.State != "fail" {
		t.Errorf("脚本应标记失败且退出码42: %+v", script)
	}
}

// 会话中断（模拟 init 0/reboot 掐断连接、无退出码）：标记 interrupted，登录结果不受影响
func TestScriptExecInterruptedInProc(t *testing.T) {
	addr := startInProcSSHServerOpts(t, "Test@12345", inProcOpts{AbortBashS: true})
	cfg := inProcSSHCfg(addr)
	cfg.SSH.Script = "echo before-interrupt"
	cfg.SSH.ScriptTimeout = "5s"

	result, _, _, script := testOne(cfg, &VM{IP: "127.0.0.1", Password: "Test@12345"})
	if !strings.HasPrefix(result, "✓") {
		t.Errorf("登录结果不应受会话中断影响: %q", result)
	}
	if script == nil || script.OK || script.State != "interrupted" {
		t.Errorf("脚本应标记会话中断: %+v", script)
	}
	if script.ExitCode != -1 {
		t.Errorf("会话中断退出码应为-1: %+v", script)
	}
	if !strings.Contains(script.Output, "shutting down") {
		t.Errorf("应保留中断前已收到的输出: %q", script.Output)
	}
}

// 超时：强制中断并标记超时(State=timeout)，登录结果不受影响
func TestScriptExecTimeoutInProc(t *testing.T) {
	addr := startInProcSSHServer(t, "Test@12345")
	cfg := inProcSSHCfg(addr)
	cfg.SSH.Script = "sleep 30; echo never"
	cfg.SSH.ScriptTimeout = "1s"

	start := time.Now()
	result, _, _, script := testOne(cfg, &VM{IP: "127.0.0.1", Password: "Test@12345"})
	if err := time.Since(start); err > 5*time.Second {
		t.Errorf("超时未生效，耗时: %v", err)
	}
	if !strings.HasPrefix(result, "✓") {
		t.Errorf("登录结果不应受脚本超时影响: %q", result)
	}
	if script == nil || script.OK || script.State != "timeout" {
		t.Errorf("脚本应标记超时失败: %+v", script)
	}
	if !strings.Contains(script.Error, "超时") {
		t.Errorf("错误信息应包含超时: %q", script.Error)
	}
}

// 认证失败时脚本结果应为空
func TestScriptNotRunOnAuthFailInProc(t *testing.T) {
	addr := startInProcSSHServer(t, "Test@12345")
	cfg := inProcSSHCfg(addr)
	cfg.SSH.Script = "echo never-runs"
	cfg.SSH.ScriptTimeout = "5s"

	result, _, script, err := trySSH(cfg, "127.0.0.1", "wrong-password")
	if err == nil {
		t.Fatalf("密码错误应登录失败")
	}
	_ = result
	if script != nil {
		t.Errorf("登录失败时不应有脚本结果: %+v", script)
	}
}
