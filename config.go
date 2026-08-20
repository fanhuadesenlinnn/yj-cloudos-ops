package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config 全部运行参数从 YAML 配置文件读取
type Config struct {
	Endpoint           string        `yaml:"endpoint"`           // API 服务地址，如 https://k8sVIP:30990
	InsecureSkipVerify bool          `yaml:"insecureSkipVerify"` // 跳过证书校验
	AccessKeyID        string        `yaml:"accessKeyId"`        // 平台凭证 AK
	AccessKeySecret    string        `yaml:"accessKeySecret"`    // 平台凭证 SK
	RegionID           string        `yaml:"regionId"`           // 区域ID
	Project            ProjectCfg    `yaml:"project"`
	Resource           ResourceCfg   `yaml:"resource"`
	Filter             FilterCfg     `yaml:"filter"` // IP 筛选：选定项目后按 IP 圈定/剔除要执行的主机
	HTTP               HTTPCfg       `yaml:"http"`
	Pagination         PaginationCfg `yaml:"pagination"`
	SSH                SSHCfg        `yaml:"ssh"`
	ExecList           []ExecStep    `yaml:"execList"` // 流水线步骤列表（按顺序执行）；未配置时使用默认流水线（status -> services）
	Raw                RawCfg        `yaml:"raw"`
	Output             OutputCfg     `yaml:"output"`
}

// ExecStep 一个流水线模块。步骤按配置顺序执行，前一步失败（且 onError=stop）则后续步骤不执行。
// 支持的模块类型：
//   - files    文件传输模块：push（本机 -> 远端）/ pull（远端 -> 本机），支持文件/文件夹
//   - command  命令执行模块（target 支持 local/remote）：先 cd 到 workdir（可选）再执行命令/脚本
//   - services 服务状态检查模块（target 固定 remote）：检查远端服务运行状态（如 sshd/docker）
//   - status   服务器运行状态采集模块（target 固定 remote）：采集 OS/负载/CPU/内存/磁盘
//
// target 字段按模块取不同语义：files 模块为传输方向 push/pull（必填）；command 模块为执行位置 local/remote（缺省 remote）。
// run 字段：once（本地步骤只跑一次，默认） / always（每台服务器都跑）
// onError 字段：stop（失败终止后续步骤，默认） / continue（失败后继续下一步）
type ExecStep struct {
	Name    string `yaml:"name"`    // 步骤名，展示/结果表用；缺省自动生成 step1/step2...
	Type    string `yaml:"type"`    // files / command / services / status
	Target  string `yaml:"target"`  // files: push/pull（传输方向，必填）；command: local/remote（执行位置）；services/status 固定 remote
	Run     string `yaml:"run"`     // once / always；缺省: local=once, remote=always（仅 command target=local 有意义）
	OnError string `yaml:"onError"` // stop / continue；缺省 stop

	// files 模块字段（Type=files）
	Files     []StepUploadFile `yaml:"files"`     // 传输规则列表；push: local(本机源) -> remote(远端目标)；pull: remote(远端源) -> local(本机目标)
	Overwrite *bool            `yaml:"overwrite"` // 步骤级默认是否覆盖目标端同名文件；缺省 false（已存在则跳过），单条规则 overwrite 优先
	Mkdirs    *bool            `yaml:"mkdirs"`    // 目标端父目录不存在时自动创建；缺省 true

	// command 模块字段（Type=command）
	Command    string `yaml:"command"`    // 单行命令（本地: 经 shell 执行；远端: 经 bash 执行），与 script/scriptPath 三选一
	Script     string `yaml:"script"`     // 内嵌脚本内容（多行，经 stdin 传远端 bash -s；本地则经 shell 执行）
	ScriptPath string `yaml:"scriptPath"` // 本地脚本文件路径，读取内容后执行
	Timeout    string `yaml:"timeout"`    // 单次执行超时，默认 60s；超时强制中断并标记失败
	Workdir    string `yaml:"workdir"`    // 执行前先进入的工作目录（可选）：远端为 cd 目录（绝对路径），本地为 cmd.Dir

	// services 模块字段（Type=services）
	Services []string `yaml:"services"` // 要检查的服务名列表；留空默认检查 sshd
}

// StepUploadFile 一条传输规则（files 模块）
// push 方向：local=本地源（文件/文件夹，文件夹递归传输保持结构），remote=远端目标（精确到文件）
// pull 方向：remote=远端源（文件/文件夹，递归拉取保持结构），local=本机目标（文件或目录）
type StepUploadFile struct {
	Local     string `yaml:"local"`     // 本机路径：push=源 / pull=目标
	Remote    string `yaml:"remote"`    // 远端路径：push=目标 / pull=源
	Mode      string `yaml:"mode"`      // 目标端文件权限，八进制字符串如 "0755" / "644"，默认 0644
	Overwrite *bool  `yaml:"overwrite"` // 是否覆盖目标端同名文件；缺省用步骤级 overwrite
}

type RawCfg struct {
	Dir string `yaml:"dir"` // 接口原始返回数据保存目录，留空则不保存
}

type ProjectCfg struct {
	Name  string   `yaml:"name"`  // 兼容单个项目（旧配置）
	Names []string `yaml:"names"` // 多个项目名称；含 "*" 或 "all" 表示全部项目
}

// ResourceCfg 检查的资源类型
type ResourceCfg struct {
	Type string `yaml:"type"` // ecs(默认) / bms / all
}

type HTTPCfg struct {
	Timeout    string `yaml:"timeout"`    // 如 30s
	Concurrent int    `yaml:"concurrent"` // API 并发请求数（取密码/详情等），默认 10
}

type PaginationCfg struct {
	PageSize int `yaml:"pageSize"`
}

type SSHCfg struct {
	Username      string   `yaml:"username"`      // 默认 root
	Port          int      `yaml:"port"`          // 默认 22
	Timeout       string   `yaml:"timeout"`       // 默认 10s
	Workers       int      `yaml:"workers"`       // 并发数，默认 5
	VerifyCommand string   `yaml:"verifyCommand"` // 登录成功后执行的验证命令，默认 "echo ok"
	UseIP         string   `yaml:"useIp"`         // internal / eip / internal-then-eip，默认 internal
	Services      []string `yaml:"services"`      // 默认流水线 services 步骤的服务名列表；留空默认检查 sshd（execList 配置了则用步骤自己的 services）
}

type OutputCfg struct {
	ShowPassword bool   `yaml:"showPassword"` // 屏幕是否显示密码，默认 true
	Dir          string `yaml:"dir"`         // 导出目录（相对/绝对），留空则不导出；文件名自动生成: <配置名>_<时间戳>.xlsx
	ScriptDir    string `yaml:"scriptDir"`    // 脚本输出落盘目录（留空不落盘），目录结构 scriptDir/<运行时间戳>/<机器名>_<IP>.log
}

func loadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cfg := &Config{}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("解析YAML失败: %w", err)
	}

	// 默认值
	if cfg.Endpoint == "" {
		return nil, fmt.Errorf("缺少配置 endpoint")
	}
	if cfg.AccessKeyID == "" || cfg.AccessKeySecret == "" {
		return nil, fmt.Errorf("缺少配置 accessKeyId / accessKeySecret")
	}
	if cfg.RegionID == "" {
		return nil, fmt.Errorf("缺少配置 regionId")
	}
	if cfg.Project.Name == "" && len(cfg.Project.Names) == 0 {
		return nil, fmt.Errorf("缺少配置 project.name / project.names")
	}
	switch cfg.Resource.Type {
	case "", "ecs", "bms", "all":
		if cfg.Resource.Type == "" {
			cfg.Resource.Type = "ecs"
		}
	default:
		return nil, fmt.Errorf("resource.type 取值非法: %s（支持 ecs / bms / all）", cfg.Resource.Type)
	}
	if cfg.Pagination.PageSize <= 0 {
		cfg.Pagination.PageSize = 100
	}
	if cfg.Pagination.PageSize > 100 {
		cfg.Pagination.PageSize = 100
	}
	if cfg.SSH.Username == "" {
		cfg.SSH.Username = "root"
	}
	if cfg.SSH.Port <= 0 {
		cfg.SSH.Port = 22
	}
	if cfg.SSH.VerifyCommand == "" {
		cfg.SSH.VerifyCommand = "echo ok"
	}
	switch cfg.SSH.UseIP {
	case "", "internal", "eip", "internal-then-eip":
		if cfg.SSH.UseIP == "" {
			cfg.SSH.UseIP = "internal"
		}
	default:
		return nil, fmt.Errorf("ssh.useIp 取值非法: %s（支持 internal / eip / internal-then-eip）", cfg.SSH.UseIP)
	}
	// 流水线步骤校验
	if err := validateExecList(cfg.ExecList); err != nil {
		return nil, err
	}
	// IP 筛选规则校验（CIDR/通配符格式）
	if err := validateFilter(&cfg.Filter); err != nil {
		return nil, err
	}
	return cfg, nil
}

// validateExecList 校验流水线步骤：类型/目标/运行方式/失败策略合法，各类型必填字段齐全
func validateExecList(steps []ExecStep) error {
	if len(steps) == 0 {
		return nil // 未配置 exec-list：走默认流水线（status -> services）
	}
	for i, s := range steps {
		prefix := fmt.Sprintf("execList[%d]", i)
		switch s.Type {
		case "files":
			// files 模块 target 必填且只能为 push / pull（传输方向，强制写清）
			switch s.Target {
			case "push", "pull":
			default:
				return fmt.Errorf("%s: files 模块 target 必填且只能为 push / pull（得到 %q）", prefix, s.Target)
			}
			if len(s.Files) == 0 {
				return fmt.Errorf("%s: files 模块必须配置 files", prefix)
			}
			for j, f := range s.Files {
				fp := fmt.Sprintf("%s.files[%d]", prefix, j)
				if f.Local == "" {
					return fmt.Errorf("%s.local 不能为空", fp)
				}
				if f.Remote == "" {
					return fmt.Errorf("%s.remote 不能为空", fp)
				}
				// 远端路径（push 的目标 / pull 的源）必须是绝对路径
				if !strings.HasPrefix(f.Remote, "/") {
					return fmt.Errorf("%s.remote 必须是远端绝对路径（以 / 开头）: %s", fp, f.Remote)
				}
				if s.Target == "push" && strings.HasSuffix(f.Remote, "/") {
					return fmt.Errorf("%s.remote 必须精确到文件（不能以 / 结尾）: %s", fp, f.Remote)
				}
				if f.Mode != "" {
					if _, err := parseFileMode(f.Mode); err != nil {
						return fmt.Errorf("%s.mode 非法: %w", fp, err)
					}
				}
			}
			if err := validateRunOnError(s, prefix); err != nil {
				return err
			}
		case "command":
			switch s.Target {
			case "", "local", "remote":
			default:
				return fmt.Errorf("%s: command 模块 target 只能为 local / remote（得到 %q）", prefix, s.Target)
			}
			if s.Command == "" && s.Script == "" && s.ScriptPath == "" {
				return fmt.Errorf("%s: command 模块必须配置 command / script / scriptPath 之一", prefix)
			}
			n := 0
			for _, v := range []string{s.Command, s.Script, s.ScriptPath} {
				if v != "" {
					n++
				}
			}
			if n > 1 {
				return fmt.Errorf("%s: command 模块的 command / script / scriptPath 只能配置一个", prefix)
			}
			if s.Timeout != "" {
				if d, err := time.ParseDuration(s.Timeout); err != nil || d <= 0 {
					return fmt.Errorf("%s.timeout 非法: %q", prefix, s.Timeout)
				}
			}
			// workdir 可选；远端执行时必须是绝对路径（cd 目标明确），本地执行允许相对路径
			if s.Workdir != "" && s.Target != "local" && !strings.HasPrefix(s.Workdir, "/") {
				return fmt.Errorf("%s.workdir 必须是远端绝对路径（以 / 开头）: %s", prefix, s.Workdir)
			}
			if err := validateRunOnError(s, prefix); err != nil {
				return err
			}
		case "services":
			if s.Target != "" && s.Target != "remote" {
				return fmt.Errorf("%s: services 模块 target 只能为 remote（得到 %q）", prefix, s.Target)
			}
			if err := validateRunOnError(s, prefix); err != nil {
				return err
			}
		case "status":
			if s.Target != "" && s.Target != "remote" {
				return fmt.Errorf("%s: status 模块 target 只能为 remote（得到 %q）", prefix, s.Target)
			}
			if err := validateRunOnError(s, prefix); err != nil {
				return err
			}
		default:
			return fmt.Errorf("%s.type 取值非法: %q（支持 files / command / services / status）", prefix, s.Type)
		}
	}
	return nil
}

// validateRunOnError 校验 run / onError 取值
func validateRunOnError(s ExecStep, prefix string) error {
	if s.Run != "" && s.Run != "once" && s.Run != "always" {
		return fmt.Errorf("%s.run 取值非法: %q（支持 once / always）", prefix, s.Run)
	}
	if s.OnError != "" && s.OnError != "stop" && s.OnError != "continue" {
		return fmt.Errorf("%s.onError 取值非法: %q（支持 stop / continue）", prefix, s.OnError)
	}
	return nil
}

// EffectiveSteps 生效的流水线步骤列表：
//   - execList 未配置（缺失或 null）→ 默认流水线（status -> services），保持工具原有检查能力
//   - execList 显式配置了 → 完全按配置执行；其中 `execList: []`（显式空列表）= 只测 SSH 连通性，不执行任何步骤
func (c *Config) EffectiveSteps() []ExecStep {
	if c.ExecList != nil {
		return c.ExecList
	}
	return []ExecStep{
		{Name: "采集运行状态", Type: "status", Target: "remote", OnError: "continue"},
		{Name: "检查服务状态", Type: "services", Target: "remote", OnError: "continue", Services: c.SSH.Services},
	}
}

// StepName 步骤显示名（缺省自动生成 step1/step2...）
func StepName(s ExecStep, idx int) string {
	if s.Name != "" {
		return s.Name
	}
	return "step" + itoa(idx+1)
}

// StepTarget 步骤 target 原始值：files 模块为传输方向 push/pull；command 模块为执行位置 local/remote（缺省 remote）；
// services/status 固定 remote。
func StepTarget(s ExecStep) string {
	if s.Target != "" {
		return s.Target
	}
	return "remote"
}

// StepIsLocal 步骤是否在本机执行（command 模块 target=local；files/services/status 均在远端执行）
func StepIsLocal(s ExecStep) bool {
	return s.Type == "command" && StepTarget(s) == "local"
}

// StepRunMode 步骤运行方式：once（本地步骤默认）/ always（远端步骤默认）
func StepRunMode(s ExecStep) string {
	if s.Run != "" {
		return s.Run
	}
	if StepIsLocal(s) {
		return "once"
	}
	return "always"
}

// StepOnError 步骤失败策略：缺省 stop
func StepOnError(s ExecStep) string {
	if s.OnError != "" {
		return s.OnError
	}
	return "stop"
}

// StepCommandContent 获取 command 步骤的执行内容：command 优先，其次 scriptPath 读取本地文件，再次内嵌 script。
// scriptPath 读取文件后统一换行符为 \n：Windows(CRLF)/老Mac(CR) 的脚本若原样传给远端/本机 bash，
// 行尾 \r 会被当作命令/参数的一部分，导致 "$'\r': command not found"、if/for 语法错误、
// 变量尾随 \r 等（YAML 内嵌 script 已由解析器归一化为 \n，仅需处理文件读取）。
func StepCommandContent(s ExecStep) (string, error) {
	switch {
	case s.Command != "":
		return s.Command, nil
	case s.ScriptPath != "":
		data, err := os.ReadFile(s.ScriptPath)
		if err != nil {
			return "", fmt.Errorf("读取脚本文件 %s 失败: %w", s.ScriptPath, err)
		}
		text := strings.ReplaceAll(string(data), "\r\n", "\n") // Windows CRLF -> LF
		text = strings.ReplaceAll(text, "\r", "\n")            // 老 Mac 单独 CR -> LF
		return text, nil
	default:
		return s.Script, nil
	}
}

// StepTimeout 单次命令执行超时（默认 60s）
func StepTimeout(s ExecStep) time.Duration {
	return parseDuration(s.Timeout, 60*time.Second)
}

// StepMkdirsEnabled 目标端父目录不存在时是否自动创建（未配置默认 true）
func StepMkdirsEnabled(s ExecStep) bool {
	return s.Mkdirs == nil || *s.Mkdirs
}

// StepShouldOverwrite 单条传输规则是否覆盖目标端同名文件：单文件 overwrite 优先，缺省用步骤级 overwrite
func StepShouldOverwrite(s ExecStep, f StepUploadFile) bool {
	if f.Overwrite != nil {
		return *f.Overwrite
	}
	return s.Overwrite != nil && *s.Overwrite
}

// StepFileMode 解析单条传输规则的目标端权限（默认 0644）
func StepFileMode(s ExecStep, f StepUploadFile) (os.FileMode, error) {
	if f.Mode == "" {
		return 0o644, nil
	}
	return parseFileMode(f.Mode)
}

// StepServiceNames services 步骤要检查的服务名列表（未配置默认检查 sshd）
func StepServiceNames(s ExecStep) []string {
	if len(s.Services) == 0 {
		return []string{"sshd"}
	}
	return s.Services
}

// parseFileMode 解析八进制权限字符串："0755" / "755" / "644" -> os.FileMode
func parseFileMode(s string) (os.FileMode, error) {
	v, err := strconv.ParseUint(strings.TrimPrefix(s, "0"), 8, 32)
	if err != nil {
		return 0, fmt.Errorf("八进制权限解析失败 %q（如 0755 / 644）: %w", s, err)
	}
	return os.FileMode(v), nil
}

// HTTPTimeout 解析 HTTP 超时
func (c *Config) HTTPTimeout() time.Duration {
	return parseDuration(c.HTTP.Timeout, 30*time.Second)
}

// HTTPWorkers API 并发请求数（默认 10）
func (c *Config) HTTPWorkers() int {
	if c.HTTP.Concurrent <= 0 {
		return 10
	}
	return c.HTTP.Concurrent
}

// SSHSingleTimeout 单台登录测试超时
func (c *Config) SSHSingleTimeout() time.Duration {
	return parseDuration(c.SSH.Timeout, 10*time.Second)
}

func parseDuration(s string, def time.Duration) time.Duration {
	if s == "" {
		return def
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return def
	}
	return d
}
