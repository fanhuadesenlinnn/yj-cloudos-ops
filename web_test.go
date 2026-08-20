package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---------- settings ----------

func TestLoadSettingsCreateDefault(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.yaml")
	s, err := loadSettings(path)
	if err != nil {
		t.Fatalf("loadSettings 失败: %v", err)
	}
	if s.Auth.Username != "admin" {
		t.Errorf("默认用户名应为 admin: %q", s.Auth.Username)
	}
	if s.ConfigsDir != "configs" {
		t.Errorf("默认配置目录应为 configs: %q", s.ConfigsDir)
	}
	if s.HistorySize != 10 {
		t.Errorf("默认历史保留应为 10: %d", s.HistorySize)
	}
	// 默认密码 admin 可登录
	if !s.checkPassword("admin", "admin") {
		t.Error("默认密码 admin 应可登录")
	}
	if s.checkPassword("admin", "wrong") {
		t.Error("错误密码不应通过")
	}
	if s.checkPassword("other", "admin") {
		t.Error("错误用户名不应通过")
	}
	// 文件已生成且不含明文密码
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "admin") && strings.Contains(string(data), "passwordHash: admin") {
		t.Error("设置文件不应存明文密码")
	}
	if strings.Contains(string(data), "admin\n") && !strings.Contains(string(data), "passwordHash:") {
		t.Error("应存哈希而非明文")
	}
}

func TestLoadSettingsReuseAndChangePassword(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.yaml")
	s, _ := loadSettings(path)
	s.setPassword("ops", "newpass123")
	if err := saveSettings(path, s); err != nil {
		t.Fatalf("saveSettings 失败: %v", err)
	}
	// 重新加载：新账号生效，旧密码失效
	s2, err := loadSettings(path)
	if err != nil {
		t.Fatalf("loadSettings 失败: %v", err)
	}
	if s2.Auth.Username != "ops" {
		t.Errorf("用户名应为 ops: %q", s2.Auth.Username)
	}
	if !s2.checkPassword("ops", "newpass123") {
		t.Error("新密码应可登录")
	}
	if s2.checkPassword("ops", "admin") {
		t.Error("旧密码不应再有效")
	}
	// 哈希不应与明文相同
	if s2.Auth.PasswordHash == "newpass123" {
		t.Error("密码不应明文存储")
	}
}

func TestSettingsApplyDefaults(t *testing.T) {
	s := &Settings{}
	s.applyDefaults()
	if s.Auth.Username != "admin" || s.ConfigsDir != "configs" || s.HistorySize != 10 {
		t.Errorf("applyDefaults 填充错误: %+v", s)
	}
}

// ---------- 配置 CRUD ----------

func TestSplitDesc(t *testing.T) {
	// 首行描述注释
	desc, rest := splitDesc("# 描述: 生产环境\nendpoint: x\n")
	if desc != "生产环境" || !strings.Contains(rest, "endpoint: x") || strings.Contains(rest, "描述") {
		t.Errorf("splitDesc 首行错误: desc=%q rest=%q", desc, rest)
	}
	// 无描述
	desc, rest = splitDesc("endpoint: x\n")
	if desc != "" || rest != "endpoint: x\n" {
		t.Errorf("splitDesc 无描述错误: desc=%q rest=%q", desc, rest)
	}
}

func TestListProfiles(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "生产.yaml"), []byte("# 描述: 生产巡检\nendpoint: a\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "测试.yaml"), []byte("endpoint: b\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "note.txt"), []byte("x"), 0o644)
	metas, err := listProfiles(dir)
	if err != nil {
		t.Fatalf("listProfiles 失败: %v", err)
	}
	if len(metas) != 2 {
		t.Fatalf("应列出 2 个 yaml 配置（忽略 txt）: %d", len(metas))
	}
	byName := map[string]profileMeta{}
	for _, m := range metas {
		byName[m.Name] = m
	}
	if byName["生产"].Desc != "生产巡检" {
		t.Errorf("描述解析错误: %q", byName["生产"].Desc)
	}
	if byName["测试"].Desc != "" {
		t.Errorf("无描述配置应为空: %q", byName["测试"].Desc)
	}
}

// ---------- 自动导出文件名 ----------

func TestAutoExcelPath(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{}
	cfg.Output.Dir = dir

	p1 := autoExcelPath("生产环境", cfg)
	if !strings.HasPrefix(filepath.Base(p1), "生产环境_") || !strings.HasSuffix(p1, ".xlsx") {
		t.Errorf("文件名格式错误: %q", p1)
	}
	if !strings.Contains(filepath.Base(p1), time.Now().Format("20060102")) {
		t.Errorf("文件名应含日期: %q", p1)
	}
	// 同秒撞名：追加序号
	os.WriteFile(p1, []byte("x"), 0o644)
	p2 := autoExcelPath("生产环境", cfg)
	if p2 == p1 {
		t.Errorf("撞名应生成新路径: %q", p2)
	}
	if !strings.Contains(filepath.Base(p2), "_1.xlsx") {
		t.Errorf("撞名应追加序号: %q", filepath.Base(p2))
	}
	// dir 为空：不导出
	cfg.Output.Dir = ""
	if p := autoExcelPath("x", cfg); p != "" {
		t.Errorf("dir 为空不应导出: %q", p)
	}
}

func TestValidConfigName(t *testing.T) {
	valid := []string{"生产环境", "prod-test", "test_1", "abc123"}
	for _, n := range valid {
		if !validConfigName.MatchString(n) {
			t.Errorf("应合法: %q", n)
		}
	}
	invalid := []string{"../etc", "a/b", ".hidden", "a b", "a.b"}
	for _, n := range invalid {
		if validConfigName.MatchString(n) {
			t.Errorf("应非法: %q", n)
		}
	}
}
