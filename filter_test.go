package main

import (
	"strings"
	"testing"
)

// vmWithIPs 便捷构造带内网/弹性 IP 的主机
func vmWithIPs(ip, eip string) *VM {
	return &VM{IP: ip, EIP: eip}
}

func TestFilterExactIP(t *testing.T) {
	cfg := &Config{Filter: FilterCfg{
		IncludeIPs: FilterIPs{IP: []string{"10.10.1.5"}},
	}}
	vms := []*VM{
		vmWithIPs("10.10.1.5", ""),
		vmWithIPs("10.10.1.6", ""),
		vmWithIPs("", "10.10.1.5"), // EIP 匹配，但 include 配在 IP 列表，不应命中
	}
	kept, dropped, err := filterVMs(cfg, vms)
	if err != nil {
		t.Fatalf("filterVMs 失败: %v", err)
	}
	if dropped != 2 || len(kept) != 1 {
		t.Fatalf("精确IP过滤错误: kept=%d dropped=%d", len(kept), dropped)
	}
	if kept[0].IP != "10.10.1.5" {
		t.Errorf("应只保留内网IP 10.10.1.5: %+v", kept[0])
	}
}

func TestFilterCIDR(t *testing.T) {
	cfg := &Config{Filter: FilterCfg{
		IncludeIPs: FilterIPs{IP: []string{"10.10.1.0/24"}},
	}}
	vms := []*VM{
		vmWithIPs("10.10.1.200", ""),
		vmWithIPs("10.10.2.1", ""),
		vmWithIPs("10.10.1.255", ""),
	}
	kept, dropped, err := filterVMs(cfg, vms)
	if err != nil {
		t.Fatalf("filterVMs 失败: %v", err)
	}
	if dropped != 1 || len(kept) != 2 {
		t.Fatalf("CIDR过滤错误: kept=%d dropped=%d", len(kept), dropped)
	}
	for _, vm := range kept {
		if !strings.HasPrefix(vm.IP, "10.10.1.") {
			t.Errorf("CIDR 过滤越界: %s", vm.IP)
		}
	}
}

func TestFilterWildcard(t *testing.T) {
	cfg := &Config{Filter: FilterCfg{
		IncludeIPs: FilterIPs{IP: []string{"10.10.0.*"}},
	}}
	vms := []*VM{
		vmWithIPs("10.10.0.1", ""),
		vmWithIPs("10.10.0.99", ""),
		vmWithIPs("10.11.0.1", ""),
		vmWithIPs("10.10.0.", ""), // 空段不匹配（* 至少一段字符）
	}
	kept, dropped, _ := filterVMs(cfg, vms)
	if dropped != 2 || len(kept) != 2 {
		t.Fatalf("通配符过滤错误: kept=%d dropped=%d", len(kept), dropped)
	}
}

func TestFilterNoIncludeAllKept(t *testing.T) {
	cfg := &Config{Filter: FilterCfg{
		ExcludeIPs: FilterIPs{IP: []string{"10.0.0.1"}},
	}}
	vms := []*VM{
		vmWithIPs("10.0.0.1", ""),
		vmWithIPs("10.0.0.2", ""),
	}
	kept, dropped, _ := filterVMs(cfg, vms)
	if dropped != 1 || len(kept) != 1 || kept[0].IP != "10.0.0.2" {
		t.Fatalf("仅 exclude 过滤错误: kept=%d dropped=%d", len(kept), dropped)
	}
}

func TestFilterIncludeThenExclude(t *testing.T) {
	cfg := &Config{Filter: FilterCfg{
		IncludeIPs: FilterIPs{IP: []string{"10.10.1.0/24"}},
		ExcludeIPs: FilterIPs{IP: []string{"10.10.1.7", "10.10.1.9"}},
	}}
	vms := []*VM{
		vmWithIPs("10.10.1.5", ""),
		vmWithIPs("10.10.1.7", ""), // include 命中但被 exclude 剔除
		vmWithIPs("10.10.1.9", ""),
		vmWithIPs("10.20.0.1", ""), // 不在 include 网段
	}
	kept, dropped, _ := filterVMs(cfg, vms)
	if dropped != 3 || len(kept) != 1 || kept[0].IP != "10.10.1.5" {
		t.Fatalf("include+exclude 过滤错误: kept=%+v dropped=%d", kept, dropped)
	}
}

func TestFilterEIP(t *testing.T) {
	// EIP 列表只匹配弹性 IP
	cfg := &Config{Filter: FilterCfg{
		IncludeIPs: FilterIPs{EIP: []string{"1.2.3.4"}},
	}}
	vms := []*VM{
		vmWithIPs("10.0.0.1", "1.2.3.4"), // EIP 命中
		vmWithIPs("1.2.3.4", ""),         // 内网 IP 相同但不匹配 EIP 列表
	}
	kept, dropped, _ := filterVMs(cfg, vms)
	if dropped != 1 || len(kept) != 1 || kept[0].IP != "10.0.0.1" {
		t.Fatalf("EIP 过滤错误: kept=%+v dropped=%d", kept, dropped)
	}
}

func TestFilterEmptyIPIgnored(t *testing.T) {
	// 无 IP/EIP 的主机：include 配置后应被过滤；仅 exclude 时应保留
	cfg := &Config{Filter: FilterCfg{IncludeIPs: FilterIPs{IP: []string{"10.0.0.1"}}}}
	kept, dropped, _ := filterVMs(cfg, []*VM{vmWithIPs("", "")})
	if dropped != 1 || len(kept) != 0 {
		t.Fatalf("空IP应被include过滤: kept=%d dropped=%d", len(kept), dropped)
	}

	cfg = &Config{Filter: FilterCfg{ExcludeIPs: FilterIPs{IP: []string{"10.0.0.1"}}}}
	kept, dropped, _ = filterVMs(cfg, []*VM{vmWithIPs("", "")})
	if dropped != 0 || len(kept) != 1 {
		t.Fatalf("空IP不应被exclude过滤: kept=%d dropped=%d", len(kept), dropped)
	}
}

func TestFilterInvalidRule(t *testing.T) {
	cases := []FilterCfg{
		{IncludeIPs: FilterIPs{IP: []string{"10.0.0.0/33"}}},  // 非法 CIDR
		{ExcludeIPs: FilterIPs{IP: []string{"not-an-ip/24"}}}, // 非法 CIDR
	}
	for i, f := range cases {
		if _, _, err := filterVMs(&Config{Filter: f}, []*VM{vmWithIPs("10.0.0.1", "")}); err == nil {
			t.Errorf("用例%d: 非法规则应报错", i)
		}
	}
	if err := validateFilter(&cases[0]); err == nil {
		t.Error("validateFilter 应拒绝非法 CIDR")
	}
	if err := validateFilter(&FilterCfg{}); err != nil {
		t.Errorf("空 filter 不应报错: %v", err)
	}
}

func TestFilterConfigured(t *testing.T) {
	if filterConfigured(&FilterCfg{}) {
		t.Error("空 filter 不应视为已配置")
	}
	if !filterConfigured(&FilterCfg{IncludeIPs: FilterIPs{EIP: []string{"1.1.1.1"}}}) {
		t.Error("配置了 EIP 规则应视为已配置")
	}
	if !filterConfigured(&FilterCfg{ExcludeIPs: FilterIPs{IP: []string{"1.1.1.1"}}}) {
		t.Error("配置了 exclude 应视为已配置")
	}
}
