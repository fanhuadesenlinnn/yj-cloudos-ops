package main

import (
	"strings"
	"testing"
)

// progressMgr 纯逻辑测试：不启动 ticker（不 start），只验证状态管理与行文本生成

func TestProgressLineEmpty(t *testing.T) {
	p := newProgressMgr(10, nil)
	line := p.line()
	if !strings.Contains(line, "[0/10] 0%") || !strings.Contains(line, "完成: 0") {
		t.Errorf("空进度行格式错误: %q", line)
	}
	if strings.Contains(line, "执行中") {
		t.Errorf("无执行中主机时不应出现执行中段: %q", line)
	}
}

func TestProgressBeginEnd(t *testing.T) {
	p := newProgressMgr(3, nil)
	p.begin(&VM{IP: "10.0.0.1", Name: "web-01"})
	line := p.line()
	if !strings.Contains(line, "10.0.0.1") || !strings.Contains(line, "web-01") {
		t.Errorf("执行中主机应显示 IP+主机名: %q", line)
	}
	p.end(&VM{IP: "10.0.0.1"})
	line = p.line()
	if !strings.Contains(line, "[1/3] 33%") || !strings.Contains(line, "完成: 1") {
		t.Errorf("完成后进度应更新: %q", line)
	}
	if strings.Contains(line, "10.0.0.1") {
		t.Errorf("完成后不应再显示该主机: %q", line)
	}
}

func TestProgressSetStep(t *testing.T) {
	p := newProgressMgr(1, nil)
	p.begin(&VM{IP: "10.0.0.1", Name: "db-01", EIP: "1.2.3.4"})
	// 用 EIP 更新步骤（模拟 useIp=eip 时按连接 IP 定位），应经别名解析到主 key
	p.setStep("1.2.3.4", 2, 3, "部署")
	line := p.line()
	if !strings.Contains(line, "(2/3 部署)") {
		t.Errorf("步骤进度未显示: %q", line)
	}
	// 步骤未开始时（stepTotal=0）不显示括号
	p2 := newProgressMgr(1, nil)
	p2.begin(&VM{IP: "10.0.0.2"})
	if strings.Contains(p2.line(), "(") {
		t.Errorf("未开始步骤不应显示进度括号: %q", p2.line())
	}
}

func TestProgressAliasCleanup(t *testing.T) {
	p := newProgressMgr(1, nil)
	p.begin(&VM{IP: "10.0.0.1", EIP: "1.2.3.4"})
	p.end(&VM{IP: "10.0.0.1", EIP: "1.2.3.4"})
	// end 后别名应清理，重新 begin 同 IP 不应残留旧别名映射
	p.begin(&VM{IP: "10.0.0.1", EIP: "5.6.7.8"})
	p.setStep("1.2.3.4", 1, 1, "旧") // 旧别名已删，不应命中
	if strings.Contains(p.line(), "旧") {
		t.Errorf("end 后别名未清理: %q", p.line())
	}
}

func TestProgressPercentRounding(t *testing.T) {
	p := newProgressMgr(3, nil)
	p.end(&VM{IP: "10.0.0.1"})
	if !strings.Contains(p.line(), "33%") {
		t.Errorf("百分比取整错误: %q", p.line())
	}
}

func TestProgressNilSafe(t *testing.T) {
	// sshProgress 为 nil（测试/未运行时）时所有方法应安全 no-op
	var p *progressMgr
	p.begin(&VM{IP: "1.1.1.1"})
	p.setStep("1.1.1.1", 1, 1, "x")
	p.end(&VM{IP: "1.1.1.1"})
	p.clear()
	p.refresh()
	p.stop()
	progressPrint(func() {})
	// 到这里不 panic 即通过
}

func TestDisplayWidth(t *testing.T) {
	if displayWidth("abc") != 3 {
		t.Error("ASCII 宽度错误")
	}
	if displayWidth("中文") != 4 {
		t.Errorf("全角字符应按2列: got %d", displayWidth("中文"))
	}
	if displayWidth("a中文b") != 6 {
		t.Errorf("混合宽度错误: got %d", displayWidth("a中文b"))
	}
}

func TestTruncateWidth(t *testing.T) {
	s := "abcdef"
	if truncateWidth(s, 3) != "abc" {
		t.Errorf("截断错误: %q", truncateWidth(s, 3))
	}
	if truncateWidth(s, 10) != s {
		t.Errorf("超宽不应截断: %q", truncateWidth(s, 10))
	}
	// 不截半字符
	if truncateWidth("中文abc", 3) != "中" {
		t.Errorf("不应截半字符: %q", truncateWidth("中文abc", 3))
	}
	if truncateWidth("中文abc", 4) != "中文" {
		t.Errorf("宽度4应截出两个全角: %q", truncateWidth("中文abc", 4))
	}
}
