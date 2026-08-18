package main

import (
	"fmt"
	"net"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"golang.org/x/crypto/ssh"
)

// statusCommand 登录成功后采集服务器运行状态的命令（输出用 ===标记=== 分段，便于解析）
const statusCommand = `echo "===OS==="; . /etc/os-release 2>/dev/null; echo "$PRETTY_NAME"; echo "===KERNEL==="; uname -r; echo "===UPTIME==="; uptime; echo "===CPU==="; top -bn1 2>/dev/null | grep -m1 -E '^%Cpu|Cpu\\(s\\)'; echo "===MEM==="; free -m 2>/dev/null; echo "===DISK==="; df -h -x tmpfs -x devtmpfs 2>/dev/null`

// runSSHTests 并发执行 SSH 登录测试与运行状态采集，结果写回 vm.SSHResult/vm.ServerStatus，进度输出到 stderr
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
				result, status := testOne(cfg, vm)
				vm.SSHResult = result
				vm.ServerStatus = status
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

// testOne 对单台虚拟机做登录测试，成功且开启采集时返回服务器运行状态
func testOne(cfg *Config, vm *VM) (string, *ServerStatus) {
	if vm.Password == "" {
		return "无密码(GetEcsPassword未返回)", nil
	}
	ips := candidateIPs(cfg, vm)
	if len(ips) == 0 {
		return "无可用IP", nil
	}
	var lastErr error
	for _, ip := range ips {
		status, err := trySSH(cfg, ip, vm.Password)
		if err == nil {
			return "✓ 成功 (" + ip + ")", status
		}
		lastErr = err
	}
	return "✗ " + classifySSHErr(lastErr), nil
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

// trySSH 用 root+密码 连接，执行验证命令；开启采集时再执行状态采集命令
func trySSH(cfg *Config, ip, password string) (*ServerStatus, error) {
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
		return nil, err
	}
	defer conn.Close()

	sshConn, chans, reqs, err := ssh.NewClientConn(conn, addr, clientCfg)
	if err != nil {
		return nil, err
	}
	defer sshConn.Close()

	client := ssh.NewClient(sshConn, chans, reqs)
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return nil, err
	}
	defer session.Close()

	if _, err := session.Output(cfg.SSH.VerifyCommand); err != nil {
		return nil, err
	}

	if !cfg.CheckStatusEnabled() {
		return nil, nil
	}
	// 重新开 session 采集运行状态
	session2, err := client.NewSession()
	if err != nil {
		return nil, err
	}
	defer session2.Close()
	out, err := session2.Output(statusCommand)
	if err != nil {
		// 登录成功但状态采集失败：仍视为登录成功，返回已解析的部分状态
		return parseStatus(string(out)), nil
	}
	return parseStatus(string(out)), nil
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

// ---------- 服务器运行状态解析 ----------

var (
	reLoadAvg = regexp.MustCompile(`load average:\s*([0-9.]+),\s*([0-9.]+),\s*([0-9.]+)`)
	reUp      = regexp.MustCompile(`up\s+(.+?),\s+\d+\s+users,`)
	reCPUId   = regexp.MustCompile(`(\d+(?:\.\d+)?)\s*id`)
	reMemHead = regexp.MustCompile(`Mem:`)
	reDiskRow = regexp.MustCompile(`^(\S+)\s+(\S+)\s+(\S+)\s+(\S+)\s+(\S+)\s+/$`)
)

// parseStatus 解析 statusCommand 输出
func parseStatus(out string) *ServerStatus {
	st := &ServerStatus{}
	section := ""
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		switch {
		case strings.Contains(line, "===OS==="):
			section = "os"
		case strings.Contains(line, "===KERNEL==="):
			section = "kernel"
		case strings.Contains(line, "===UPTIME==="):
			section = "uptime"
		case strings.Contains(line, "===CPU==="):
			section = "cpu"
		case strings.Contains(line, "===MEM==="):
			section = "mem"
		case strings.Contains(line, "===DISK==="):
			section = "disk"
		case line == "":
			continue
		default:
			switch section {
			case "os":
				if st.OS == "" && !strings.HasPrefix(line, "/") {
					st.OS = line
				}
			case "kernel":
				st.Kernel = line
			case "uptime":
				if m := reLoadAvg.FindStringSubmatch(line); m != nil {
					st.LoadAvg = m[1] + ", " + m[2] + ", " + m[3]
				}
				if m := reUp.FindStringSubmatch(line); m != nil {
					st.Uptime = strings.TrimSpace(m[1])
				}
			case "cpu":
				if m := reCPUId.FindStringSubmatch(line); m != nil {
					if idle, err := strconv.ParseFloat(m[1], 64); err == nil {
						st.CPUUsed = fmt.Sprintf("%.1f", 100-idle)
					}
				}
			case "mem":
				if reMemHead.MatchString(line) {
					fields := strings.Fields(line)
					// Mem:  total used free shared buff/cache available
					if len(fields) >= 7 {
						st.MemTotal = fmt.Sprintf("%.1fG", atof(fields[1])/1024)
						st.MemUsed = fmt.Sprintf("%.1fG", atof(fields[2])/1024)
						total := atof(fields[1])
						if total > 0 {
							st.MemUsedPct = fmt.Sprintf("%.1f", atof(fields[2])/total*100)
						}
					}
				}
			case "disk":
				if m := reDiskRow.FindStringSubmatch(line); m != nil {
					st.DiskTotal = m[2]
					st.DiskUsed = m[3]
					st.DiskUsePct = strings.TrimSuffix(m[5], "%")
				}
			}
		}
	}
	return st
}

func atof(s string) float64 {
	v, _ := strconv.ParseFloat(s, 64)
	return v
}
