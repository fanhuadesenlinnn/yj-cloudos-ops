package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/pkg/sftp"
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
	// 脚本输出落盘目录（output.scriptDir 配置了才开）
	scriptDir := scriptLogDir(cfg)

	jobs := make(chan *VM)
	var wg sync.WaitGroup
	var done int64

	for i := 0; i < cfg.SSH.Workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for vm := range jobs {
				result, status, services, uploads, script := testOne(cfg, vm)
				vm.SSHResult = result
				vm.ServerStatus = status
				vm.Services = services
				vm.Uploads = uploads
				vm.Script = script
				if len(uploads) > 0 {
					printUploadResults(vm, uploads) // 上传失败/跳过：stderr 回显现场
				}
				if script != nil {
					if !script.OK && script.State != "error" {
						printScriptFailure(vm, script) // 失败/超时/会话中断：stderr 回显现场
					}
					if scriptDir != "" {
						writeScriptLog(scriptDir, vm, script)
					}
				}
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

// testOne 对单台虚拟机做登录测试，成功且开启采集时返回服务器运行状态、服务状态、上传结果与脚本执行结果
func testOne(cfg *Config, vm *VM) (string, *ServerStatus, []ServiceStatus, []*UploadResult, *ScriptResult) {
	if vm.Password == "" {
		return "无密码(GetEcsPassword未返回)", nil, nil, nil, nil
	}
	ips := candidateIPs(cfg, vm)
	if len(ips) == 0 {
		return "无可用IP", nil, nil, nil, nil
	}
	var lastErr error
	for _, ip := range ips {
		status, services, uploads, script, err := trySSH(cfg, ip, vm.Password)
		if err == nil {
			return "✓ 成功 (" + ip + ")", status, services, uploads, script
		}
		lastErr = err
	}
	return "✗ " + classifySSHErr(lastErr), nil, nil, nil, nil
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

// trySSH 用 root+密码 连接，执行验证命令；成功后按需上传文件、执行脚本、采集状态与服务检查
func trySSH(cfg *Config, ip, password string) (*ServerStatus, []ServiceStatus, []*UploadResult, *ScriptResult, error) {
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
		return nil, nil, nil, nil, err
	}
	defer conn.Close()

	sshConn, chans, reqs, err := ssh.NewClientConn(conn, addr, clientCfg)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	defer sshConn.Close()

	client := ssh.NewClient(sshConn, chans, reqs)
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return nil, nil, nil, nil, err
	}
	defer session.Close()

	if _, err := session.Output(cfg.SSH.VerifyCommand); err != nil {
		return nil, nil, nil, nil, err
	}

	// 1. 上传文件（登录成功后、执行脚本前）
	var uploads []*UploadResult
	if cfg.UploadEnabled() {
		uploads = uploadFiles(client, cfg)
	}

	// 2. 执行脚本
	var script *ScriptResult
	if cfg.ScriptEnabled() {
		if fail := firstUploadError(uploads); fail != nil {
			// 上传失败时脚本不执行，避免误跑远端旧文件
			script = &ScriptResult{ExitCode: -1, State: "error", Error: "上传失败，脚本未执行: " + fail.Error}
		} else {
			script = runScript(client, cfg)
		}
	}

	// 3. 服务器运行状态 / 服务状态采集
	var status *ServerStatus
	if cfg.CheckStatusEnabled() {
		status = collectServerStatus(client)
	}
	var services []ServiceStatus
	if cfg.CheckServicesEnabled() {
		services = collectServiceStatus(client, cfg.ServiceNames())
	}
	return status, services, uploads, script, nil
}

// ---------- 文件上传（SFTP） ----------

// uploadFiles 通过 SFTP 把本地文件传到远端指定路径（可覆盖同名文件），返回每文件结果。
// 纯 Go 实现（github.com/pkg/sftp），兼容 CGO_ENABLED=0 静态编译。
func uploadFiles(client *ssh.Client, cfg *Config) []*UploadResult {
	sftpClient, err := sftp.NewClient(client)
	if err != nil {
		return []*UploadResult{{State: "error", Error: "创建SFTP会话失败: " + err.Error()}}
	}
	defer sftpClient.Close()

	results := make([]*UploadResult, 0, len(cfg.SSH.Upload))
	for _, f := range cfg.SSH.Upload {
		results = append(results, uploadOne(sftpClient, f, cfg))
	}
	return results
}

// uploadOne 上传单个文件：同名已存在且未开启覆盖 -> 跳过(skipped)；否则创建父目录、流式写入并设置权限。
func uploadOne(sc *sftp.Client, f UploadFile, cfg *Config) *UploadResult {
	res := &UploadResult{Local: f.Local, Remote: f.Remote}

	mode, err := cfg.UploadFileMode(f)
	if err != nil {
		res.State = "error"
		res.Error = err.Error()
		return res
	}
	res.Mode = fmt.Sprintf("%04o", mode)

	// 同名文件已存在且未开启覆盖：跳过（安全默认，避免误覆盖）
	existed := false
	if _, err := sc.Stat(f.Remote); err == nil {
		existed = true
	}
	if existed && !cfg.UploadShouldOverwrite(f) {
		res.State = "skipped"
		res.Error = "远端已存在同名文件，未开启覆盖，已跳过"
		return res
	}

	local, err := os.Open(f.Local)
	if err != nil {
		res.State = "error"
		res.Error = "打开本地文件失败: " + err.Error()
		return res
	}
	defer local.Close()

	// 远端父目录不存在时自动创建
	if cfg.UploadMkdirsEnabled() {
		if dir := path.Dir(f.Remote); dir != "." && dir != "/" {
			if err := sc.MkdirAll(dir); err != nil {
				res.State = "error"
				res.Error = "创建远端目录失败: " + err.Error()
				return res
			}
		}
	}

	// O_TRUNC 覆盖写入（支持二进制/大文件，io.Copy 流式传输）
	remote, err := sc.OpenFile(f.Remote, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
	if err != nil {
		res.State = "error"
		res.Error = "打开远端文件失败: " + err.Error()
		return res
	}
	if _, err := io.Copy(remote, local); err != nil {
		remote.Close()
		res.State = "error"
		res.Error = "写入远端文件失败: " + err.Error()
		return res
	}
	if err := remote.Close(); err != nil {
		res.State = "error"
		res.Error = "关闭远端文件失败: " + err.Error()
		return res
	}
	if err := sc.Chmod(f.Remote, mode); err != nil {
		res.State = "error"
		res.Error = "设置远端权限失败: " + err.Error()
		return res
	}

	res.Overwritten = existed
	res.State = "success"
	return res
}

// firstUploadError 返回上传结果中第一个 error 状态（无则返回 nil）
func firstUploadError(uploads []*UploadResult) *UploadResult {
	for _, u := range uploads {
		if u != nil && u.State == "error" {
			return u
		}
	}
	return nil
}

// printUploadResults 上传失败/跳过时在 stderr 回显现场，便于当场定位
func printUploadResults(vm *VM, uploads []*UploadResult) {
	var lines []string
	for _, u := range uploads {
		if u == nil {
			continue
		}
		switch u.State {
		case "skipped":
			lines = append(lines, fmt.Sprintf("  跳过: %s -> %s（%s）", u.Local, u.Remote, u.Error))
		case "error":
			lines = append(lines, fmt.Sprintf("  失败: %s -> %s（%s）", u.Local, u.Remote, u.Error))
		}
	}
	if len(lines) == 0 {
		return
	}
	fmt.Fprintf(os.Stderr, "\n[上传] %s (%s)\n%s\n", vm.Name, vm.IP, strings.Join(lines, "\n"))
}

// shellQuote POSIX 单引号转义，把远端路径安全嵌入命令（防止路径含特殊字符注入）
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// runScript 通过 stdin 以 `bash -s` 在远端执行脚本（内容不经过 shell 拼接，无转义问题）。
// 带脚本级超时（默认 60s），超时关闭会话强制中断远端命令；失败不影响 SSH 登录结果标记。
// 结果按 State 分类：success / fail(收到退出码) / timeout / interrupted(会话被掐断，如 init 0、reboot) / error(未执行)。
func runScript(client *ssh.Client, cfg *Config) *ScriptResult {
	res := &ScriptResult{ExitCode: -1}

	content, err := cfg.ScriptContent()
	if err != nil {
		res.State = "error"
		res.Error = err.Error()
		return res
	}
	if strings.TrimSpace(content) == "" {
		res.State = "error"
		res.Error = "脚本内容为空"
		return res
	}

	session, err := client.NewSession()
	if err != nil {
		res.State = "error"
		res.Error = "创建会话失败: " + err.Error()
		return res
	}
	defer session.Close()

	// 脚本内容走 SSH channel 的 stdin 传给远端 bash，规避引号/特殊字符问题
	session.Stdin = strings.NewReader(content)
	var outBuf, errBuf syncBuf
	session.Stdout = &outBuf
	session.Stderr = &errBuf

	done := make(chan error, 1)
	cmd := "bash -s"
	if cfg.SSH.RemoteWorkDir != "" {
		// 在指定目录下执行脚本（配合 upload 把脚本传到该目录后运行）
		cmd = "cd " + shellQuote(cfg.SSH.RemoteWorkDir) + " && bash -s"
	}
	go func() { done <- session.Run(cmd) }()

	select {
	case err := <-done:
		res.Output, res.Truncated = truncateOutput(mergeOutput(outBuf.String(), errBuf.String()), maxScriptOutput)
		if err == nil {
			res.OK = true
			res.ExitCode = 0
			res.State = "success"
		} else {
			res.ExitCode = exitCode(err)
			res.Error = err.Error()
			if isInterrupted(err) {
				res.State = "interrupted"
			} else {
				res.State = "fail"
			}
		}
	case <-time.After(cfg.ScriptTimeoutDuration()):
		// 超时：关闭会话以中断远端命令，并尽量保存已收到的输出
		session.Close()
		select {
		case <-done: // 等拷贝协程收尾后再读缓冲区
		case <-time.After(2 * time.Second):
		}
		res.Output, res.Truncated = truncateOutput(mergeOutput(outBuf.String(), errBuf.String()), maxScriptOutput)
		res.State = "timeout"
		res.Error = fmt.Sprintf("脚本执行超时(%s)", cfg.ScriptTimeoutDuration())
	}
	return res
}

// isInterrupted 判断脚本执行是否属于"会话中断"（未收到退出码或连接断开）。
// 典型场景：脚本里执行了 init 0 / reboot / shutdown -h now 等导致 SSH 会话被掐断，
// 此时命令可能已成功下发、机器正在关机，不应简单归为"执行失败"。
func isInterrupted(err error) bool {
	if err == nil {
		return false
	}
	var ee *ssh.ExitError
	if errors.As(err, &ee) {
		return false // 收到了退出码，属于正常的非零退出
	}
	var em *ssh.ExitMissingError
	if errors.As(err, &em) {
		return true // 通道关闭但未收到退出码
	}
	msg := strings.ToLower(err.Error())
	for _, kw := range []string{"eof", "connection reset", "broken pipe", "closed network connection", "i/o timeout", "timed out"} {
		if strings.Contains(msg, kw) {
			return true
		}
	}
	return false
}

// maxScriptOutput 单台脚本输出保留上限；超出截断（保留末尾），防止脚本刷屏拖垮内存/Excel 导出
const maxScriptOutput = 100 << 10 // 100KB

// truncateOutput 将输出截断到 max 字节（保留末尾，回退到 UTF-8 边界），返回截断后的内容与是否截断
func truncateOutput(out string, max int) (string, bool) {
	if len(out) <= max {
		return out, false
	}
	start := len(out) - max
	for start < len(out) && !utf8.RuneStart(out[start]) {
		start++ // 回退到合法 rune 边界
	}
	return "[输出过长，已截断，仅保留末尾]\n" + out[start:], true
}

// lastLines 取字符串末尾 n 行（输出不足 n 行时原样返回）
func lastLines(s string, n int) string {
	if s == "" {
		return ""
	}
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}

// mergeOutput 合并 stdout 与 stderr 输出
func mergeOutput(out, errOut string) string {
	switch {
	case out == "":
		return errOut
	case errOut == "":
		return out
	default:
		return out + "\n" + errOut
	}
}

// syncBuf 线程安全的输出缓冲（SSH 会话的 stdout/stderr 拷贝协程可能并发写入，
// 且超时路径会在协程收尾前读取）
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// scriptFailTailLines 脚本失败/超时/会话中断时，stderr 回显输出尾部行数
const scriptFailTailLines = 20

// printScriptFailure 脚本非成功结果时在 stderr 回显状态、原因与输出尾部，便于当场定位
func printScriptFailure(vm *VM, s *ScriptResult) {
	fmt.Fprintf(os.Stderr, "\n[脚本] %s (%s) %s\n", vm.Name, vm.IP, scriptResultLabel(s))
	if s.Error != "" {
		fmt.Fprintf(os.Stderr, "  原因: %s\n", s.Error)
	}
	if tail := lastLines(s.Output, scriptFailTailLines); tail != "" {
		fmt.Fprintf(os.Stderr, "  ----- 输出尾部(最后%d行) -----\n%s\n  ------------------------------\n", scriptFailTailLines, tail)
	}
}

// scriptLogDir 创建脚本输出落盘目录（output.scriptDir 配置了才开），返回目录路径
func scriptLogDir(cfg *Config) string {
	if cfg.Output.ScriptDir == "" {
		return ""
	}
	dir := filepath.Join(cfg.Output.ScriptDir, time.Now().Format("20060102_150405"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "警告: 创建脚本输出目录失败: %v（不落盘）\n", err)
		return ""
	}
	fmt.Fprintf(os.Stderr, "脚本输出保存目录: %s\n", dir)
	return dir
}

// writeScriptLog 将单台机器的脚本结果写入 <机器名>_<IP>.log
func writeScriptLog(dir string, vm *VM, s *ScriptResult) {
	content := fmt.Sprintf("# %s (%s)\n# 状态: %s | 退出码: %d%s\n%s",
		vm.Name, vm.IP, scriptResultLabel(s), s.ExitCode, orErrSuffix(s), s.Output)
	path := filepath.Join(dir, sanitizeFileName(vm.Name)+"_"+sanitizeFileName(vm.IP)+".log")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "\n警告: 写入脚本输出 %s 失败: %v\n", path, err)
	}
}

// orErrSuffix 错误信息的前缀后缀（无错误返回空串）
func orErrSuffix(s *ScriptResult) string {
	if s.Error == "" {
		return ""
	}
	return " | 错误: " + s.Error
}

// sanitizeFileName 过滤文件名的路径分隔符等危险字符
func sanitizeFileName(name string) string {
	if name == "" {
		return "unknown"
	}
	repl := func(r rune) rune {
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|', ' ', '\t', '\n', '\r':
			return '_'
		}
		return r
	}
	return strings.Map(repl, name)
}

// exitCode 从 SSH 命令错误中提取远端退出码；非 ExitError 返回 -1
func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *ssh.ExitError
	if errors.As(err, &ee) {
		return ee.ExitStatus()
	}
	return -1
}

// collectServerStatus 采集服务器运行状态（尽力而为，失败不阻塞）
func collectServerStatus(client *ssh.Client) *ServerStatus {
	session, err := client.NewSession()
	if err != nil {
		return nil
	}
	defer session.Close()
	out, _ := session.Output(statusCommand)
	return parseStatus(string(out))
}

// collectServiceStatus 检查服务运行状态（尽力而为，失败不阻塞）
func collectServiceStatus(client *ssh.Client, names []string) []ServiceStatus {
	cmd := serviceCheckCommand(names)
	if cmd == "" {
		return nil
	}
	session, err := client.NewSession()
	if err != nil {
		return nil
	}
	defer session.Close()
	out, _ := session.Output(cmd)
	return parseServices(string(out))
}

// serviceCheckCommand 生成服务状态检查命令（systemd/systemctl 优先，兼容 SysV service）
// 服务名只允许字母数字与 - _ .，防止注入
func serviceCheckCommand(names []string) string {
	valid := make([]string, 0, len(names))
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		ok := true
		for _, r := range n {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
				ok = false
				break
			}
		}
		if ok {
			valid = append(valid, n)
		}
	}
	if len(valid) == 0 {
		return ""
	}
	list := strings.Join(valid, " ")
	return `for s in ` + list + `; do
  st="unknown"
  if command -v systemctl >/dev/null 2>&1; then
    st=$(systemctl is-active "$s" 2>/dev/null)
    [ -z "$st" ] && st="not-found"
  elif command -v service >/dev/null 2>&1; then
    if service "$s" status >/dev/null 2>&1; then st="active"; else st="inactive"; fi
  fi
  echo "$s=$st"
done`
}

// parseServices 解析服务状态输出（每行: 服务名=状态）
func parseServices(out string) []ServiceStatus {
	var svcs []ServiceStatus
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		svcs = append(svcs, ServiceStatus{Name: parts[0], State: parts[1]})
	}
	return svcs
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
