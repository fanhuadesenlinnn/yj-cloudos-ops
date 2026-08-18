package main

import (
	"fmt"
	"os"
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
	HTTP               HTTPCfg       `yaml:"http"`
	Pagination         PaginationCfg `yaml:"pagination"`
	SSH                SSHCfg        `yaml:"ssh"`
	Raw                RawCfg        `yaml:"raw"`
	Output             OutputCfg     `yaml:"output"`
}

type RawCfg struct {
	Dir string `yaml:"dir"` // 接口原始返回数据保存目录，留空则不保存
}

type ProjectCfg struct {
	Name  string   `yaml:"name"`  // 兼容单个项目（旧配置）
	Names []string `yaml:"names"` // 多个项目名称；含 "*" 或 "all" 表示全部项目
}

type HTTPCfg struct {
	Timeout string `yaml:"timeout"` // 如 30s
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
	CheckStatus   *bool    `yaml:"checkStatus"`   // 登录成功后采集服务器运行状态（CPU/内存/磁盘/负载/OS），未配置默认 true
	CheckServices *bool    `yaml:"checkServices"` // 登录成功后检查服务运行状态，未配置默认 true
	Services      []string `yaml:"services"`      // 要检查的服务名列表；留空默认检查 sshd
}

type OutputCfg struct {
	ShowPassword bool   `yaml:"showPassword"` // 屏幕是否显示密码，默认 true
	CSVPath      string `yaml:"csvPath"`      // 为空则不导出 CSV
	ExcelPath    string `yaml:"excelPath"`    // 为空则不导出 Excel
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
	return cfg, nil
}

// CheckStatusEnabled 服务器运行状态采集开关（未配置默认开启）
func (c *Config) CheckStatusEnabled() bool {
	return c.SSH.CheckStatus == nil || *c.SSH.CheckStatus
}

// CheckServicesEnabled 服务运行状态检查开关（未配置默认开启）
func (c *Config) CheckServicesEnabled() bool {
	return c.SSH.CheckServices == nil || *c.SSH.CheckServices
}

// ServiceNames 需要检查的服务名列表（未配置默认检查 sshd）
func (c *Config) ServiceNames() []string {
	if len(c.SSH.Services) == 0 {
		return []string{"sshd"}
	}
	return c.SSH.Services
}

// HTTPTimeout 解析 HTTP 超时
func (c *Config) HTTPTimeout() time.Duration {
	return parseDuration(c.HTTP.Timeout, 30*time.Second)
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
