package main

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"golang.org/x/crypto/ssh"
)

// runSSHTests 并发执行 SSH 登录测试，结果写回 vm.SSHResult，进度输出到 stderr
func runSSHTests(cfg *Config, vms []*VM) {
	total := len(vms)
	if total == 0 {
		return
	}
	jobs := make(chan *VM)
	var wg sync.WaitGroup
	var done int64

	for i := 0; i < cfg.SSH.Workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for vm := range jobs {
				vm.SSHResult = testOne(cfg, vm)
				n := atomic.AddInt64(&done, 1)
				fmt.Fprintf(os.Stderr, "\rSSH登录测试进度: %d/%d", n, total)
			}
		}()
	}
	for _, vm := range vms {
		jobs <- vm
	}
	close(jobs)
	wg.Wait()
	fmt.Fprintln(os.Stderr)
}

// testOne 对单台虚拟机做登录测试
func testOne(cfg *Config, vm *VM) string {
	if vm.Password == "" {
		return "无密码(GetEcsPassword未返回)"
	}
	ips := candidateIPs(cfg, vm)
	if len(ips) == 0 {
		return "无可用IP"
	}
	var lastErr error
	for _, ip := range ips {
		if err := trySSH(cfg, ip, vm.Password); err == nil {
			return "✓ 成功 (" + ip + ")"
		} else {
			lastErr = err
		}
	}
	return "✗ " + classifySSHErr(lastErr)
}

// candidateIPs 按配置选择测试用的 IP
func candidateIPs(cfg *Config, vm *VM) []string {
	switch cfg.SSH.UseIP {
	case "eip":
		if vm.EIP != "" {
			return []string{vm.EIP}
		}
		return nil
	case "internal-then-eip":
		var ips []string
		if vm.IP != "" {
			ips = append(ips, vm.IP)
		}
		if vm.EIP != "" {
			ips = append(ips, vm.EIP)
		}
		return ips
	default: // internal
		if vm.IP != "" {
			return []string{vm.IP}
		}
		return nil
	}
}

// trySSH 用 root+密码 连接并执行验证命令
func trySSH(cfg *Config, ip, password string) error {
	addr := net.JoinHostPort(ip, strconv.Itoa(cfg.SSH.Port))
	timeout := cfg.SSHSingleTimeout()

	clientCfg := &ssh.ClientConfig{
		User:            cfg.SSH.Username,
		Auth:            []ssh.AuthMethod{ssh.Password(password)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // 仅做登录连通性测试
		Timeout:         timeout,
	}

	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return err
	}
	defer conn.Close()

	sshConn, chans, reqs, err := ssh.NewClientConn(conn, addr, clientCfg)
	if err != nil {
		return err
	}
	defer sshConn.Close()

	client := ssh.NewClient(sshConn, chans, reqs)
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return err
	}
	defer session.Close()

	_, err = session.Output(cfg.SSH.VerifyCommand)
	return err
}

// classifySSHErr 将错误归类为易读的失败原因
func classifySSHErr(err error) string {
	if err == nil {
		return "成功"
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "timed out") || strings.Contains(msg, "i/o timeout"):
		return "连接超时"
	case strings.Contains(msg, "connection refused"):
		return "拒绝连接"
	case strings.Contains(msg, "no route to host"):
		return "网络不可达"
	case strings.Contains(msg, "unable to authenticate"):
		return "认证失败(密码错误或已修改)"
	case strings.Contains(msg, "handshake failed"):
		return "SSH握手失败"
	case strings.Contains(msg, "host key"):
		return "主机密钥错误"
	default:
		return "其他错误: " + err.Error()
	}
}
