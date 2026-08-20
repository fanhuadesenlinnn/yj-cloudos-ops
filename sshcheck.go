package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// statusCommand 采集服务器运行状态的命令（输出用 ===标记=== 分段，便于解析）
const statusCommand = `echo "===OS==="; . /etc/os-release 2>/dev/null; echo "$PRETTY_NAME"; echo "===KERNEL==="; uname -r; echo "===UPTIME==="; uptime; echo "===CPU==="; top -bn1 2>/dev/null | grep -m1 -E '^%Cpu|Cpu\\(s\\)'; echo "===MEM==="; free -m 2>/dev/null; echo "===DISK==="; df -h -x tmpfs -x devtmpfs 2>/dev/null`

// sshProgress 当前 runSSHTests 的实时进度控制器；未运行时为 nil（测试直接调用 trySSH/testOne 时不受影响）。
var sshProgress *progressMgr

// vmProgress 单台机器的实时执行状态（进度显示用）
type vmProgress struct {
	ip        string
	name      string
	stepIdx   int    // 当前执行到第几步（从1起；0 表示尚未开始步骤，如 SSH 连接中）
	stepTotal int    // 流水线总步骤数
	stepName  string // 当前步骤名
}

// progressMgr 实时执行进度控制器。
// CLI 模式：sink 为 nil，stderr 单行刷新，显示 完成数/总数/百分比 + 正在执行的主机与当前步骤；
// 与其他 stderr 输出通过 clear/refresh 协调，避免互相覆盖。
// Web 模式：sink 非 nil（如 SSE 推送），每次状态变化调用 sink(p)，不输出到 stderr、不启动 ticker。
type progressMgr struct {
	mu      sync.Mutex
	total   int
	done    int
	active  map[string]*vmProgress // key: 主机内网 IP（begin 注册）
	aliases map[string]string      // 弹性IP(EIP) -> 主 key（内网IP），setStep/end 按实际连接 IP 解析
	sink    func(p *progressMgr)   // Web 模式：状态变化回调（nil=CLI stderr 模式）
	ticker  *time.Ticker
	stopCh  chan struct{}
	lastLen int // 上次输出行宽（显示宽度），清行用
}

// maxProgressWidth 进度单行最大显示宽度（超出截断，保持终端干净）
const maxProgressWidth = 120

func newProgressMgr(total int, sink func(p *progressMgr)) *progressMgr {
	return &progressMgr{total: total, sink: sink, active: map[string]*vmProgress{}, aliases: map[string]string{}}
}

// start 启动实时刷新（每 500ms 重绘一次），并立即输出首行
func (p *progressMgr) start() {
	if p == nil {
		return
	}
	p.refresh()
	p.stopCh = make(chan struct{})
	p.ticker = time.NewTicker(500 * time.Millisecond)
	go func() {
		for {
			select {
			case <-p.ticker.C:
				p.refresh()
			case <-p.stopCh:
				return
			}
		}
	}()
}

// stop 停止刷新并清掉进度行（换行，恢复普通输出）
func (p *progressMgr) stop() {
	if p == nil {
		return
	}
	if p.ticker != nil {
		p.ticker.Stop()
		close(p.stopCh)
		p.ticker = nil
	}
	p.clear()
	fmt.Fprintln(os.Stderr)
}

// begin 登记一台主机开始执行（worker 领走即调用，含 SSH 连接阶段）
func (p *progressMgr) begin(vm *VM) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.active[vm.IP] = &vmProgress{ip: vm.IP, name: vm.Name}
	if vm.EIP != "" {
		p.aliases[vm.EIP] = vm.IP
	}
	p.mu.Unlock()
	p.refresh()
}

// setStep 更新主机当前执行的步骤；ip 为实际连接 IP（useIp=eip 时是弹性 IP，经别名解析到主 key）
func (p *progressMgr) setStep(ip string, idx, total int, name string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	key := ip
	if k, ok := p.aliases[ip]; ok {
		key = k
	}
	if a, ok := p.active[key]; ok {
		a.stepIdx = idx
		a.stepTotal = total
		a.stepName = name
	}
	p.mu.Unlock()
}

// end 标记一台主机执行完成（无论成功失败）
func (p *progressMgr) end(vm *VM) {
	if p == nil {
		return
	}
	p.mu.Lock()
	delete(p.active, vm.IP)
	if vm.EIP != "" {
		delete(p.aliases, vm.EIP)
	}
	p.done++
	p.mu.Unlock()
	p.refresh()
}

// clear 清掉当前进度行（其他 stderr 输出前调用；Web 模式 sink 非 nil 时 no-op，由客户端覆盖整行）
func (p *progressMgr) clear() {
	if p == nil {
		return
	}
	if p.sink != nil {
		return
	}
	p.mu.Lock()
	n := p.lastLen
	p.lastLen = 0
	p.mu.Unlock()
	if n > 0 {
		fmt.Fprintf(os.Stderr, "\r%s\r", strings.Repeat(" ", n))
	} else {
		fmt.Fprint(os.Stderr, "\r")
	}
}

// refresh 立即重绘进度行（CLI: stderr 清行重写；Web: 调用 sink 推送）
func (p *progressMgr) refresh() {
	if p == nil {
		return
	}
	if p.sink != nil {
		p.sink(p)
		return
	}
	p.clear()
	line := p.line()
	if w := displayWidth(line); w > maxProgressWidth {
		line = truncateWidth(line, maxProgressWidth) + "…"
	}
	p.mu.Lock()
	p.lastLen = displayWidth(line)
	p.mu.Unlock()
	fmt.Fprint(os.Stderr, line)
}

// line 生成进度行文本：完成数/总数/百分比 + 执行中主机列表 + 完成数
func (p *progressMgr) line() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	pct := 0
	if p.total > 0 {
		pct = p.done * 100 / p.total
	}
	var b strings.Builder
	fmt.Fprintf(&b, "[%d/%d] %d%%", p.done, p.total, pct)
	if len(p.active) > 0 {
		b.WriteString(" | 执行中: ")
		keys := make([]string, 0, len(p.active))
		for k := range p.active {
			keys = append(keys, k)
		}
		sort.Strings(keys) // 稳定顺序，避免 map 随机导致跳动
		for i, k := range keys {
			if i > 0 {
				b.WriteString("  ")
			}
			a := p.active[k]
			b.WriteString(a.ip)
			if a.name != "" && a.name != a.ip {
				b.WriteString(" " + a.name)
			}
			if a.stepTotal > 0 {
				fmt.Fprintf(&b, "(%d/%d", a.stepIdx, a.stepTotal)
				if a.stepName != "" {
					b.WriteString(" " + a.stepName)
				}
				b.WriteString(")")
			}
		}
	}
	fmt.Fprintf(&b, " | 完成: %d", p.done)
	return b.String()
}

// progressPrint 在进度行之外输出内容（先清行、输出、再恢复进度行），用于失败回显等
func progressPrint(fn func()) {
	sshProgress.clear()
	fn()
	sshProgress.refresh()
}

// runeWidth 字符的终端显示宽度（全角/中日韩字符按 2 列）
func runeWidth(r rune) int {
	if r >= 0x1100 && (r <= 0x115F || r == 0x2329 || r == 0x232A ||
		(0x2E80 <= r && r <= 0xA4CF && r != 0x303F) ||
		(0xAC00 <= r && r <= 0xD7A3) || (0xF900 <= r && r <= 0xFAFF) ||
		(0xFE30 <= r && r <= 0xFE4F) || (0xFF00 <= r && r <= 0xFF60) ||
		(0xFFE0 <= r && r <= 0xFFE6)) {
		return 2
	}
	return 1
}

// displayWidth 字符串的终端显示宽度
func displayWidth(s string) int {
	w := 0
	for _, r := range s {
		w += runeWidth(r)
	}
	return w
}

// truncateWidth 按显示宽度截断字符串（保留完整字符，不截半）
func truncateWidth(s string, max int) string {
	w := 0
	for i, r := range s {
		cw := runeWidth(r)
		if w+cw > max {
			return s[:i]
		}
		w += cw
	}
	return s
}
// onceResults 是阶段一（本地 once 步骤）的结果，按下标与 cfg.EffectiveSteps() 对齐（非 once 步骤为 nil）；
// globalStopped 表示阶段一因某 once 步骤失败（onError=stop）已全局终止。
// prog 非 nil 时使用传入的进度控制器（Web 模式带 sink 推送）；nil 时内部创建 CLI stderr 版。
// onVM 非 nil 时每台主机执行完成（成功或失败）后调用，用于 Web 日志流。
// checkOnly=true 时只测 SSH 连通性（验证命令），不执行 exec-list 流水线（检查模式，不产生副作用）。
func runSSHTests(cfg *Config, vms []*VM, onceResults []*ExecStepResult, globalStopped bool, prog *progressMgr, onVM func(vm *VM), checkOnly bool) {
	total := len(vms)
	if total == 0 {
		return
	}
	// 脚本输出落盘目录（output.scriptDir 配置了才开）
	scriptDir := scriptLogDir(cfg)

	// 实时进度：worker 领走即标记“执行中”，每步更新，完成后累计；CLI 模式 stderr 单行刷新
	if prog == nil {
		sshProgress = newProgressMgr(total, nil)
		sshProgress.start()
	} else {
		sshProgress = prog
	}
	defer func() {
		sshProgress.stop()
		sshProgress = nil
	}()

	jobs := make(chan *VM)
	var wg sync.WaitGroup

	for i := 0; i < cfg.SSH.Workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for vm := range jobs {
				sshProgress.begin(vm) // 被 worker 领走（开始 SSH 连接）即算“开始”
				result, status, services, steps := testOne(cfg, vm, onceResults, globalStopped, checkOnly)
				vm.SSHResult = result
				vm.ServerStatus = status
				vm.Services = services
				vm.ExecSteps = steps
				for _, s := range steps {
					if s == nil {
						continue
					}
					if !stepOK(s) && s.State != "skipped" {
						// 失败/超时/会话中断：先清进度行再回显现场，完后恢复
						progressPrint(func() { printStepFailure(vm, s) })
					}
					if scriptDir != "" && s.Type == "command" {
						writeScriptLog(scriptDir, vm, s)
					}
				}
				if onVM != nil {
					onVM(vm)
				}
				sshProgress.end(vm)
			}
		}()
	}
	for _, vm := range vms {
		jobs <- vm
	}
	close(jobs)
	wg.Wait()
}

// testOne 对单台虚拟机做登录测试，成功后按流水线（exec-list）依次执行各步骤。
// checkOnly=true 时只测 SSH 连通性，不执行流水线（返回空步骤结果）。
// 返回 SSH 登录结果、status/services 步骤采集的结构化数据与该台机器的步骤结果列表。
func testOne(cfg *Config, vm *VM, onceResults []*ExecStepResult, globalStopped bool, checkOnly bool) (string, *ServerStatus, []ServiceStatus, []*ExecStepResult) {
	if vm.Password == "" {
		return "无密码(GetEcsPassword未返回)", nil, nil, nil
	}
	ips := candidateIPs(cfg, vm)
	if len(ips) == 0 {
		return "无可用IP", nil, nil, nil
	}
	var lastErr error
	for _, ip := range ips {
		status, services, steps, err := trySSH(cfg, ip, vm.Password, onceResults, globalStopped, checkOnly)
		if err == nil {
			return "✓ 成功 (" + ip + ")", status, services, steps
		}
		lastErr = err
	}
	return "✗ " + classifySSHErr(lastErr), nil, nil, nil
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

// trySSH 用 root+密码 连接并验证；成功后按流水线顺序执行各步骤（本地 once 步骤复用阶段一结果）。
// 返回 status/services 步骤采集的结构化数据与该台机器的步骤结果列表。
func trySSH(cfg *Config, ip, password string, onceResults []*ExecStepResult, globalStopped bool, checkOnly bool) (*ServerStatus, []ServiceStatus, []*ExecStepResult, error) {
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
		return nil, nil, nil, err
	}
	defer conn.Close()

	sshConn, chans, reqs, err := ssh.NewClientConn(conn, addr, clientCfg)
	if err != nil {
		return nil, nil, nil, err
	}
	defer sshConn.Close()

	client := ssh.NewClient(sshConn, chans, reqs)
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return nil, nil, nil, err
	}
	defer session.Close()

	if _, err := session.Output(cfg.SSH.VerifyCommand); err != nil {
		return nil, nil, nil, err
	}

	// 检查模式：只验证 SSH 连通性（verifyCommand 已执行成功），不跑 exec-list 流水线
	if checkOnly {
		return nil, nil, nil, nil
	}
	// 流水线执行
	status, services, steps := runPipeline(cfg, client, ip, onceResults, globalStopped)
	return status, services, steps, nil
}

// ---------- 流水线执行 ----------

// runPipelineOnce 阶段一：串行执行所有 target=local 且 run=once 的步骤（只跑一次）。
// 返回与 cfg.EffectiveSteps() 按下标对齐的结果切片（非 once 本地步骤为 nil），
// 以及是否因某步骤失败(onError=stop)导致全局终止。
func runPipelineOnce(cfg *Config) ([]*ExecStepResult, bool) {
	steps := cfg.EffectiveSteps()
	results := make([]*ExecStepResult, len(steps))
	for i, step := range steps {
		if !StepIsLocal(step) || StepRunMode(step) != "once" {
			continue
		}
		res := execStepLocal(step, i)
		results[i] = res
		if !stepOK(res) && StepOnError(step) == "stop" {
			return results, true
		}
	}
	return results, false
}

// runPipeline 对单台已登录的服务器按顺序执行流水线步骤。
// 本地 once 步骤直接复用阶段一结果；其余步骤在本机或远端执行。
// 任一步骤失败且 onError=stop 时，该台后续步骤标记 skipped 不再执行。
// ip 为实际连接 IP（用于实时进度定位主机，可能是 EIP）。
func runPipeline(cfg *Config, client *ssh.Client, ip string, onceResults []*ExecStepResult, globalStopped bool) (*ServerStatus, []ServiceStatus, []*ExecStepResult) {
	steps := cfg.EffectiveSteps()
	results := make([]*ExecStepResult, 0, len(steps))
	var status *ServerStatus
	var services []ServiceStatus

	stopped := globalStopped
	for i, step := range steps {
		name := StepName(step, i)
		if stopped {
			// 全局/上游终止：本地 once 步骤已执行过则复用其结果（如实展示失败现场），其余步骤标记未执行
			if StepIsLocal(step) && StepRunMode(step) == "once" && i < len(onceResults) && onceResults[i] != nil {
				results = append(results, onceResults[i])
			} else {
				results = append(results, skippedStepResult(step, name, "上游步骤失败，流水线已终止，本步骤未执行"))
			}
			continue
		}
		// 本地 once 步骤：复用阶段一结果（按步骤下标对齐）
		if StepIsLocal(step) && StepRunMode(step) == "once" {
			if i < len(onceResults) && onceResults[i] != nil {
				results = append(results, onceResults[i])
				if !stepOK(onceResults[i]) && StepOnError(step) == "stop" {
					stopped = true
				}
				continue
			}
			// 阶段一未执行到（理论不会发生），兜底本机执行
			res := execStepLocal(step, i)
			results = append(results, res)
			if !stepOK(res) && StepOnError(step) == "stop" {
				stopped = true
			}
			continue
		}

		var res *ExecStepResult
		// 实时进度：更新该主机当前执行的步骤
		if sshProgress != nil {
			sshProgress.setStep(ip, i+1, len(steps), StepName(step, i))
		}
		switch step.Type {
		case "files":
			res = execFilesStep(client, step, name)
		case "command":
			if StepIsLocal(step) {
				res = runLocalCommand(step, name)
			} else {
				res = runRemoteCommand(client, step, name)
			}
		case "services":
			var svcs []ServiceStatus
			res, svcs = execServicesStep(client, step, name)
			if svcs != nil {
				services = svcs
			}
		case "status":
			var st *ServerStatus
			res, st = execStatusStep(client, step, name)
			if st != nil {
				status = st
			}
		default:
			res = &ExecStepResult{Name: name, Type: step.Type, Target: StepTarget(step), ExitCode: -1,
				State: "error", Error: "未知步骤类型: " + step.Type}
		}
		results = append(results, res)
		if !stepOK(res) && StepOnError(step) == "stop" {
			stopped = true
		}
	}
	return status, services, results
}

// execStepLocal 在本机执行单个步骤（当前仅 command 模块支持 target=local）
func execStepLocal(step ExecStep, idx int) *ExecStepResult {
	return runLocalCommand(step, StepName(step, idx))
}

// stepOK 步骤结果是否成功（success 或跳过 skipped 均视为不阻断流水线）
func stepOK(res *ExecStepResult) bool {
	return res != nil && (res.State == "success" || res.State == "skipped")
}

// skippedStepResult 构造一个未执行（被跳过）的步骤结果
func skippedStepResult(step ExecStep, name, reason string) *ExecStepResult {
	return &ExecStepResult{Name: name, Type: step.Type, Target: StepTarget(step),
		State: "skipped", Error: reason, ExitCode: -1}
}

// stepDuration 步骤耗时
func stepDuration(start time.Time) string {
	return time.Since(start).Truncate(time.Millisecond).String()
}

// ---------- files 模块（SFTP 双向传输：push 本机->远端 / pull 远端->本机） ----------

// execFilesStep 执行 files 步骤：按 target 方向（push/pull，校验已强制必填）传输所有规则。
// push: 本机文件/文件夹 -> 远端（精确到文件）；pull: 远端文件/文件夹 -> 本机（保持目录结构）。
func execFilesStep(client *ssh.Client, step ExecStep, name string) *ExecStepResult {
	start := time.Now()
	direction := StepTarget(step) // push / pull
	res := &ExecStepResult{Name: name, Type: "files", Target: direction, ExitCode: -1}

	sftpClient, err := sftp.NewClient(client)
	if err != nil {
		res.State = "error"
		res.Error = "创建SFTP会话失败: " + err.Error()
		res.Duration = stepDuration(start)
		return res
	}
	defer sftpClient.Close()

	var lines []string
	fail := 0
	for _, f := range step.Files {
		if direction == "pull" {
			// pull：远端 -> 本机
			results, err := pullRule(sftpClient, step, f)
			if err != nil {
				fail++
				lines = append(lines, "失败: "+f.Remote+" -> "+f.Local+"（"+err.Error()+"）")
				continue
			}
			for _, one := range results {
				lines = append(lines, filesResultLine(one))
				if one.State == "error" {
					fail++
				}
			}
			continue
		}
		// push：本机 -> 远端
		pairs, err := expandLocalFiles(f)
		if err != nil {
			fail++
			lines = append(lines, "失败: "+f.Local+" -> "+f.Remote+"（"+err.Error()+"）")
			continue
		}
		for _, p := range pairs {
			one := pushOne(sftpClient, step, f, p.local, p.remote)
			lines = append(lines, filesResultLine(one))
			if one.State == "error" {
				fail++
			}
		}
	}
	res.Output = strings.Join(lines, "\n")
	res.Duration = stepDuration(start)
	if fail > 0 {
		res.State = "error"
		res.Error = fmt.Sprintf("%d 个文件传输失败", fail)
	} else {
		res.State = "success"
	}
	return res
}

// pullRule 按 pull 方向展开并拉取一条规则：remote 为远端源（文件/文件夹），拉回 local（本机目标）。
// 远端为文件夹时递归拉取，每个文件映射到 local/<相对路径>（保持目录结构）。
func pullRule(sc *sftp.Client, step ExecStep, f StepUploadFile) ([]*UploadResult, error) {
	info, err := sc.Stat(f.Remote)
	if err != nil {
		return nil, fmt.Errorf("远端路径不存在: %w", err)
	}
	if !info.IsDir() {
		return []*UploadResult{pullOne(sc, step, f, f.Remote, f.Local)}, nil
	}
	files, err := walkRemoteFiles(sc, f.Remote)
	if err != nil {
		return nil, err
	}
	base := strings.TrimSuffix(path.Clean(f.Remote), "/")
	var results []*UploadResult
	for _, rf := range files {
		rel := strings.TrimPrefix(strings.TrimPrefix(rf, base), "/")
		if rel == "" {
			rel = path.Base(rf)
		}
		results = append(results, pullOne(sc, step, f, rf, filepath.Join(f.Local, filepath.FromSlash(rel))))
	}
	return results, nil
}

// walkRemoteFiles 递归列出远端目录下的所有文件路径（sftp 无 Walk，手写递归）
func walkRemoteFiles(sc *sftp.Client, dir string) ([]string, error) {
	entries, err := sc.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("读取远端目录 %s 失败: %w", dir, err)
	}
	var files []string
	for _, e := range entries {
		p := path.Join(dir, e.Name())
		if e.IsDir() {
			sub, err := walkRemoteFiles(sc, p)
			if err != nil {
				return nil, err
			}
			files = append(files, sub...)
		} else {
			files = append(files, p)
		}
	}
	return files, nil
}

// pullOne 拉取单个远端文件到本机：本机同名已存在且未开启覆盖 -> 跳过；否则建父目录、流式写入并设权限。
func pullOne(sc *sftp.Client, step ExecStep, f StepUploadFile, remotePath, localPath string) *UploadResult {
	res := &UploadResult{Local: localPath, Remote: remotePath}

	mode, err := StepFileMode(step, f)
	if err != nil {
		res.State = "error"
		res.Error = err.Error()
		return res
	}
	res.Mode = fmt.Sprintf("%04o", mode)

	// 本机同名已存在且未开启覆盖：跳过（安全默认）
	existed := false
	if _, err := os.Stat(localPath); err == nil {
		existed = true
	}
	if existed && !StepShouldOverwrite(step, f) {
		res.State = "skipped"
		res.Error = "本机已存在同名文件，未开启覆盖，已跳过"
		return res
	}

	remote, err := sc.Open(remotePath)
	if err != nil {
		res.State = "error"
		res.Error = "打开远端文件失败: " + err.Error()
		return res
	}
	defer remote.Close()

	// 本机父目录不存在时自动创建
	if StepMkdirsEnabled(step) {
		if dir := filepath.Dir(localPath); dir != "." && dir != "/" {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				res.State = "error"
				res.Error = "创建本机目录失败: " + err.Error()
				return res
			}
		}
	}

	// O_TRUNC 覆盖写入（支持二进制/大文件，io.Copy 流式传输）
	local, err := os.OpenFile(localPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		res.State = "error"
		res.Error = "打开本机文件失败: " + err.Error()
		return res
	}
	if _, err := io.Copy(local, remote); err != nil {
		local.Close()
		res.State = "error"
		res.Error = "写入本机文件失败: " + err.Error()
		return res
	}
	if err := local.Close(); err != nil {
		res.State = "error"
		res.Error = "关闭本机文件失败: " + err.Error()
		return res
	}
	if err := os.Chmod(localPath, mode); err != nil {
		res.State = "error"
		res.Error = "设置本机权限失败: " + err.Error()
		return res
	}

	res.Overwritten = existed
	res.State = "success"
	return res
}

// localRemotePair 一条展开后的上传任务：本地文件 -> 远端精确文件路径
type localRemotePair struct {
	local  string
	remote string
}

// expandLocalFiles 展开上传规则：local 为文件 -> 单条；local 为文件夹 -> 递归展开为
// remote/<相对路径> 列表（远端目标精确到文件，父目录自动创建）。
func expandLocalFiles(f StepUploadFile) ([]localRemotePair, error) {
	info, err := os.Stat(f.Local)
	if err != nil {
		return nil, fmt.Errorf("本地路径不存在: %w", err)
	}
	if !info.IsDir() {
		return []localRemotePair{{local: f.Local, remote: f.Remote}}, nil
	}
	var pairs []localRemotePair
	base := filepath.Clean(f.Local)
	err = filepath.Walk(base, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if fi.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(base, p)
		if err != nil {
			return err
		}
		pairs = append(pairs, localRemotePair{
			local:  p,
			remote: path.Join(f.Remote, filepath.ToSlash(rel)),
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("遍历文件夹失败: %w", err)
	}
	return pairs, nil
}

// pushOne 推送单个文件到远端：同名已存在且未开启覆盖 -> 跳过(skipped)；否则创建父目录、流式写入并设置权限。
func pushOne(sc *sftp.Client, step ExecStep, f StepUploadFile, localPath, remotePath string) *UploadResult {
	res := &UploadResult{Local: localPath, Remote: remotePath}

	mode, err := StepFileMode(step, f)
	if err != nil {
		res.State = "error"
		res.Error = err.Error()
		return res
	}
	res.Mode = fmt.Sprintf("%04o", mode)

	// 同名文件已存在且未开启覆盖：跳过（安全默认，避免误覆盖）
	existed := false
	if _, err := sc.Stat(remotePath); err == nil {
		existed = true
	}
	if existed && !StepShouldOverwrite(step, f) {
		res.State = "skipped"
		res.Error = "远端已存在同名文件，未开启覆盖，已跳过"
		return res
	}

	local, err := os.Open(localPath)
	if err != nil {
		res.State = "error"
		res.Error = "打开本地文件失败: " + err.Error()
		return res
	}
	defer local.Close()

	// 远端父目录不存在时自动创建
	if StepMkdirsEnabled(step) {
		if dir := path.Dir(remotePath); dir != "." && dir != "/" {
			if err := sc.MkdirAll(dir); err != nil {
				res.State = "error"
				res.Error = "创建远端目录失败: " + err.Error()
				return res
			}
		}
	}

	// O_TRUNC 覆盖写入（支持二进制/大文件，io.Copy 流式传输）
	remote, err := sc.OpenFile(remotePath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
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
	if err := sc.Chmod(remotePath, mode); err != nil {
		res.State = "error"
		res.Error = "设置远端权限失败: " + err.Error()
		return res
	}

	res.Overwritten = existed
	res.State = "success"
	return res
}

// filesResultLine 单条传输结果的一行摘要（用于步骤 Output；push 为 local->remote，pull 为 remote->local）
func filesResultLine(u *UploadResult) string {
	switch u.State {
	case "success":
		if u.Overwritten {
			return fmt.Sprintf("✓ 已覆盖 %s -> %s (%s)", u.Local, u.Remote, u.Mode)
		}
		return fmt.Sprintf("✓ %s -> %s (%s)", u.Local, u.Remote, u.Mode)
	case "skipped":
		return fmt.Sprintf("跳过: %s -> %s（%s）", u.Local, u.Remote, u.Error)
	default:
		return fmt.Sprintf("失败: %s -> %s（%s）", u.Local, u.Remote, u.Error)
	}
}

// shellQuote POSIX 单引号转义，把远端路径安全嵌入命令（防止路径含特殊字符注入）
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// ---------- command 模块（本地 / 远端：先 cd 到 workdir 再执行命令或脚本） ----------

// runRemoteCommand 通过 SSH 在远端执行 command 步骤：内容经 stdin 以 `bash -s` 执行（不经 shell 拼接）。
// 配置了 workdir 时先 `cd '<workdir>'`（绝对路径，单引号转义防注入）；带步骤级超时，超时关闭会话强制中断；
// 结果分类：success / fail(收到退出码) / timeout / interrupted(会话被掐断，如 init 0/reboot) / error(未执行)。
func runRemoteCommand(client *ssh.Client, step ExecStep, name string) *ExecStepResult {
	start := time.Now()
	res := &ExecStepResult{Name: name, Type: "command", Target: "remote", ExitCode: -1}

	content, err := StepCommandContent(step)
	if err != nil {
		res.State = "error"
		res.Error = err.Error()
		res.Duration = stepDuration(start)
		return res
	}
	if strings.TrimSpace(content) == "" {
		res.State = "error"
		res.Error = "脚本内容为空"
		res.Duration = stepDuration(start)
		return res
	}

	session, err := client.NewSession()
	if err != nil {
		res.State = "error"
		res.Error = "创建会话失败: " + err.Error()
		res.Duration = stepDuration(start)
		return res
	}
	defer session.Close()

	// 内容走 SSH channel 的 stdin 传给远端 bash，规避引号/特殊字符问题
	session.Stdin = strings.NewReader(content)
	var outBuf, errBuf syncBuf
	session.Stdout = &outBuf
	session.Stderr = &errBuf

	done := make(chan error, 1)
	cmd := "bash -s"
	if step.Workdir != "" {
		cmd = "cd " + shellQuote(step.Workdir) + " && bash -s"
	}
	go func() { done <- session.Run(cmd) }()

	timeout := StepTimeout(step)
	select {
	case err := <-done:
		res.Output, res.Truncated = truncateOutput(mergeOutput(outBuf.String(), errBuf.String()), maxScriptOutput)
		if err == nil {
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
	case <-time.After(timeout):
		// 超时：关闭会话以中断远端命令，并尽量保存已收到的输出
		session.Close()
		select {
		case <-done: // 等拷贝协程收尾后再读缓冲区
		case <-time.After(2 * time.Second):
		}
		res.Output, res.Truncated = truncateOutput(mergeOutput(outBuf.String(), errBuf.String()), maxScriptOutput)
		res.State = "timeout"
		res.Error = fmt.Sprintf("脚本执行超时(%s)", timeout)
	}
	res.Duration = stepDuration(start)
	return res
}

// runLocalCommand 在本机执行 command 步骤：先进入 workdir（cmd.Dir），内容经 shell 执行
// （Unix 用 sh -s 传 stdin；Windows 用 cmd /C）。带步骤级超时，超时强制 kill；
// 结果分类：success / fail / timeout / error(未执行)。
func runLocalCommand(step ExecStep, name string) *ExecStepResult {
	start := time.Now()
	res := &ExecStepResult{Name: name, Type: "command", Target: "local", ExitCode: -1}

	content, err := StepCommandContent(step)
	if err != nil {
		res.State = "error"
		res.Error = err.Error()
		res.Duration = stepDuration(start)
		return res
	}
	if strings.TrimSpace(content) == "" {
		res.State = "error"
		res.Error = "脚本内容为空"
		res.Duration = stepDuration(start)
		return res
	}

	ctx, cancel := context.WithTimeout(context.Background(), StepTimeout(step))
	defer cancel()

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd", "/C", content)
	} else {
		cmd = exec.Command("sh", "-s")
		cmd.Stdin = strings.NewReader(content)
	}
	if step.Workdir != "" {
		cmd.Dir = step.Workdir // 本地执行前先进入工作目录
	}
	setProcessGroup(cmd) // Unix: 独立进程组，超时时可整组杀掉（连带 sleep 等子进程）
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	if err := cmd.Start(); err != nil {
		res.State = "error"
		res.Error = "启动本地命令失败: " + err.Error()
		res.Duration = stepDuration(start)
		return res
	}

	// 超时：杀掉整个进程组；不杀孙进程的话，sleep 等子进程会持有 stdout 管道，
	// 导致 cmd.Wait() 一直等待拷贝协程结束（拖到子进程自然退出）。
	// 先 Start 再启动看门狗，保证 cmd.Process 已赋值（无数据竞争）。
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			killProcessGroup(cmd)
		case <-done:
		}
	}()
	err = cmd.Wait()
	close(done)

	res.Output, res.Truncated = truncateOutput(mergeOutput(outBuf.String(), errBuf.String()), maxScriptOutput)
	switch {
	case ctx.Err() == context.DeadlineExceeded:
		res.State = "timeout"
		res.Error = fmt.Sprintf("脚本执行超时(%s)", StepTimeout(step))
	case err != nil:
		if ee, ok := err.(*exec.ExitError); ok {
			res.ExitCode = ee.ExitCode()
		} else {
			res.ExitCode = -1
		}
		res.Error = err.Error()
		res.State = "fail"
	default:
		res.ExitCode = 0
		res.State = "success"
	}
	res.Duration = stepDuration(start)
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

// maxScriptOutput 单步输出保留上限；超出截断（保留末尾），防止刷屏拖垮内存/Excel 导出
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

// stepFailTailLines 步骤失败/超时/会话中断时，stderr 回显输出尾部行数
const stepFailTailLines = 20

// printStepFailure 步骤非成功结果时在 stderr 回显状态、原因与输出尾部，便于当场定位
func printStepFailure(vm *VM, s *ExecStepResult) {
	fmt.Fprintf(os.Stderr, "\n[流水线] %s (%s) 步骤[%s] %s\n", vm.Name, vm.IP, s.Name, stepResultLabel(s))
	if s.Error != "" {
		fmt.Fprintf(os.Stderr, "  原因: %s\n", s.Error)
	}
	if tail := lastLines(s.Output, stepFailTailLines); tail != "" {
		fmt.Fprintf(os.Stderr, "  ----- 输出尾部(最后%d行) -----\n%s\n  ------------------------------\n", stepFailTailLines, tail)
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

// writeScriptLog 将单台机器某个 script 步骤的结果写入 <机器名>_<IP>_<步骤名>.log
func writeScriptLog(dir string, vm *VM, s *ExecStepResult) {
	content := fmt.Sprintf("# %s (%s)\n# 步骤: %s | 类型: %s | 状态: %s | 退出码: %d | 耗时: %s%s\n%s",
		vm.Name, vm.IP, s.Name, s.Type, stepResultLabel(s), s.ExitCode, s.Duration, orErrSuffix(s), s.Output)
	path := filepath.Join(dir, sanitizeFileName(vm.Name)+"_"+sanitizeFileName(vm.IP)+"_"+sanitizeFileName(s.Name)+".log")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		// 进度运行中时先清行再输出警告，避免打断进度条
		progressPrint(func() { fmt.Fprintf(os.Stderr, "\n警告: 写入脚本输出 %s 失败: %v\n", path, err) })
	}
}

// orErrSuffix 错误信息的前缀后缀（无错误返回空串）
func orErrSuffix(s *ExecStepResult) string {
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

// ---------- status / services 模块（复用状态采集） ----------

// execStatusStep 采集服务器运行状态（尽力而为，失败不阻塞流水线）
func execStatusStep(client *ssh.Client, step ExecStep, name string) (*ExecStepResult, *ServerStatus) {
	start := time.Now()
	res := &ExecStepResult{Name: name, Type: "status", Target: "remote", ExitCode: -1}
	st := collectServerStatus(client)
	if st == nil {
		res.State = "error"
		res.Error = "采集服务器运行状态失败"
		res.Duration = stepDuration(start)
		return res, nil
	}
	var parts []string
	if st.OS != "" {
		parts = append(parts, "OS "+st.OS)
	}
	if st.Kernel != "" {
		parts = append(parts, "内核 "+st.Kernel)
	}
	if st.CPUUsed != "" {
		parts = append(parts, "CPU "+st.CPUUsed+"%")
	}
	if st.MemUsedPct != "" {
		parts = append(parts, "内存 "+st.MemUsedPct+"%")
	}
	if st.DiskUsePct != "" {
		parts = append(parts, "磁盘 "+st.DiskUsePct+"%")
	}
	if st.LoadAvg != "" {
		parts = append(parts, "负载 "+st.LoadAvg)
	}
	res.Output = strings.Join(parts, " | ")
	res.State = "success"
	res.Duration = stepDuration(start)
	return res, st
}

// execServicesStep 检查服务运行状态（尽力而为，失败不阻塞流水线）
func execServicesStep(client *ssh.Client, step ExecStep, name string) (*ExecStepResult, []ServiceStatus) {
	start := time.Now()
	res := &ExecStepResult{Name: name, Type: "services", Target: "remote", ExitCode: -1}
	svcs := collectServiceStatus(client, StepServiceNames(step))
	if svcs == nil {
		res.State = "error"
		res.Error = "检查服务状态失败"
		res.Duration = stepDuration(start)
		return res, nil
	}
	lines := make([]string, 0, len(svcs))
	for _, s := range svcs {
		lines = append(lines, s.Name+"="+s.State)
	}
	res.Output = strings.Join(lines, "\n")
	res.State = "success"
	res.Duration = stepDuration(start)
	return res, svcs
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
