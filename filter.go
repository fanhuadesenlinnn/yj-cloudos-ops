package main

import (
	"fmt"
	"net"
	"regexp"
	"strings"
)

// FilterCfg IP 筛选：选定项目后再按 IP 过滤需要执行的主机。
// 被过滤掉的主机不执行 SSH 测试、不取密码、不出现在结果表中，仅统计显示。
// 语义：先按 includeIPs 圈定白名单（未配置则不限制），再剔除 excludeIPs 命中的主机。
// IP 列表匹配内网 IP，EIP 列表匹配弹性 IP；每条规则支持精确 IP / CIDR（10.0.0.0/24）/ 通配符（10.0.0.*）。
type FilterCfg struct {
	IncludeIPs FilterIPs `yaml:"includeIPs"`
	ExcludeIPs FilterIPs `yaml:"excludeIPs"`
}

// FilterIPs 一类 IP（内网 IP 或弹性 IP）的匹配规则列表
type FilterIPs struct {
	IP  []string `yaml:"IP"`
	EIP []string `yaml:"EIP"`
}

// ipMatcher 一组 IP 匹配规则（精确 / CIDR / 通配符），命中其一即匹配
type ipMatcher struct {
	exact map[string]bool // 精确 IP
	cidrs []*net.IPNet    // CIDR 网段
	wild  []*regexp.Regexp // 通配符（已转正则）
}

// newIPMatcher 编译规则列表；空列表返回空 matcher（不匹配任何 IP）
func newIPMatcher(rules []string) (*ipMatcher, error) {
	m := &ipMatcher{exact: map[string]bool{}}
	for _, r := range rules {
		rule := strings.TrimSpace(r)
		if rule == "" {
			continue
		}
		switch {
		case strings.Contains(rule, "/"):
			// CIDR：10.0.0.0/24 / 2001:db8::/32
			_, ipnet, err := net.ParseCIDR(rule)
			if err != nil {
				return nil, fmt.Errorf("CIDR 格式非法 %q: %v", rule, err)
			}
			m.cidrs = append(m.cidrs, ipnet)
		case strings.Contains(rule, "*"):
			// 通配符：10.0.0.*（* 匹配一段非点字符）
			re, err := wildcardToRegexp(rule)
			if err != nil {
				return nil, fmt.Errorf("通配符格式非法 %q: %v", rule, err)
			}
			m.wild = append(m.wild, re)
		default:
			// 精确 IP
			m.exact[rule] = true
		}
	}
	return m, nil
}

// hasRules 是否配置了规则
func (m *ipMatcher) hasRules() bool {
	return len(m.exact) > 0 || len(m.cidrs) > 0 || len(m.wild) > 0
}

// match 判断 IP 是否命中任一规则
func (m *ipMatcher) match(ip string) bool {
	if ip == "" {
		return false
	}
	if m.exact[ip] {
		return true
	}
	if addr := net.ParseIP(ip); addr != nil {
		for _, c := range m.cidrs {
			if c.Contains(addr) {
				return true
			}
		}
	}
	for _, re := range m.wild {
		if re.MatchString(ip) {
			return true
		}
	}
	return false
}

// wildcardToRegexp 把通配符模式（如 10.0.0.*）转成正则：* 匹配一段非点字符，其余特殊字符转义
func wildcardToRegexp(pat string) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteString("^")
	for _, ch := range pat {
		switch ch {
		case '*':
			b.WriteString("[^.]+")
		case '.', '+', '(', ')', '[', ']', '{', '}', '^', '$', '|', '\\', '?':
			b.WriteByte('\\')
			b.WriteRune(ch)
		default:
			b.WriteRune(ch)
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}

// filterVMs 按 filter 配置过滤主机：先 include 圈定（未配置则不限制），再剔除 exclude。
// 返回过滤后的切片与被过滤掉的数量。IP 规则匹配内网 IP，EIP 规则匹配弹性 IP。
func filterVMs(cfg *Config, vms []*VM) ([]*VM, int, error) {
	incIP, err := newIPMatcher(cfg.Filter.IncludeIPs.IP)
	if err != nil {
		return nil, 0, err
	}
	incEIP, err := newIPMatcher(cfg.Filter.IncludeIPs.EIP)
	if err != nil {
		return nil, 0, err
	}
	excIP, err := newIPMatcher(cfg.Filter.ExcludeIPs.IP)
	if err != nil {
		return nil, 0, err
	}
	excEIP, err := newIPMatcher(cfg.Filter.ExcludeIPs.EIP)
	if err != nil {
		return nil, 0, err
	}

	hasInclude := incIP.hasRules() || incEIP.hasRules()
	kept := make([]*VM, 0, len(vms))
	for _, vm := range vms {
		// include 圈定：配置了白名单时，内网 IP / 弹性 IP 均未命中则剔除
		if hasInclude && !incIP.match(vm.IP) && !incEIP.match(vm.EIP) {
			continue
		}
		// exclude 剔除：内网 IP 或弹性 IP 命中任一黑名单规则即剔除
		if excIP.match(vm.IP) || excEIP.match(vm.EIP) {
			continue
		}
		kept = append(kept, vm)
	}
	return kept, len(vms) - len(kept), nil
}

// filterConfigured 是否配置了 IP 筛选（任意规则非空）
func filterConfigured(f *FilterCfg) bool {
	return len(f.IncludeIPs.IP) > 0 || len(f.IncludeIPs.EIP) > 0 ||
		len(f.ExcludeIPs.IP) > 0 || len(f.ExcludeIPs.EIP) > 0
}

// validateFilter 校验 filter 规则的合法性（CIDR/通配符格式等），配置错误尽早暴露
func validateFilter(f *FilterCfg) error {
	groups := []struct {
		name string
		list []string
	}{
		{"filter.includeIPs.IP", f.IncludeIPs.IP},
		{"filter.includeIPs.EIP", f.IncludeIPs.EIP},
		{"filter.excludeIPs.IP", f.ExcludeIPs.IP},
		{"filter.excludeIPs.EIP", f.ExcludeIPs.EIP},
	}
	for _, g := range groups {
		if _, err := newIPMatcher(g.list); err != nil {
			return fmt.Errorf("%s: %w", g.name, err)
		}
	}
	return nil
}
