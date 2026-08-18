package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestTruncate(t *testing.T) {
	if got := truncate("短文本", 30); got != "短文本" {
		t.Errorf("短文本不应截断: %q", got)
	}
	got := truncate("这是一段比较长的中文文本用来测试截断", 10)
	if len([]rune(got)) != 11 { // 10 字符 + 省略号
		t.Errorf("截断长度错误: %q", got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("截断应带省略号: %q", got)
	}
}

func TestScriptStr(t *testing.T) {
	if got := scriptStr(&VM{}); got != "—" {
		t.Errorf("未配置脚本应显示 —: %q", got)
	}
	if got := scriptStr(&VM{Script: &ScriptResult{OK: true, State: "success"}}); got != "✓ 成功" {
		t.Errorf("成功应显示 ✓ 成功: %q", got)
	}
	if got := scriptStr(&VM{Script: &ScriptResult{OK: false, ExitCode: 7, State: "fail"}}); got != "✗ 失败(exit 7)" {
		t.Errorf("失败应显示退出码: %q", got)
	}
	if got := scriptStr(&VM{Script: &ScriptResult{OK: false, State: "timeout"}}); got != "✗ 超时" {
		t.Errorf("超时应显示 ✗ 超时: %q", got)
	}
	if got := scriptStr(&VM{Script: &ScriptResult{OK: false, State: "interrupted"}}); got != "✗ 会话中断(疑似关机/重启)" {
		t.Errorf("会话中断应独立标记: %q", got)
	}
	if got := scriptStr(&VM{Script: &ScriptResult{OK: false, State: "error", Error: "脚本内容为空"}}); got != "✗ 脚本内容为空" {
		t.Errorf("error 状态应展示原因: %q", got)
	}
	// 长错误信息应截断
	longErr := strings.Repeat("错误", 40)
	if got := scriptStr(&VM{Script: &ScriptResult{OK: false, State: "error", Error: longErr}}); len([]rune(got)) > 35 {
		t.Errorf("长错误信息应截断: %q", got)
	}
}

func TestScriptResultLabel(t *testing.T) {
	cases := []struct {
		s    *ScriptResult
		want string
	}{
		{nil, "未配置脚本"},
		{&ScriptResult{State: "success", OK: true}, "成功"},
		{&ScriptResult{State: "fail", ExitCode: 42}, "失败(exit 42)"},
		{&ScriptResult{State: "timeout"}, "超时"},
		{&ScriptResult{State: "interrupted"}, "会话中断(疑似关机/重启)"},
		{&ScriptResult{State: "error", Error: "读取脚本文件 x 失败"}, "未执行: 读取脚本文件 x 失败"},
	}
	for _, c := range cases {
		if got := scriptResultLabel(c.s); got != c.want {
			t.Errorf("scriptResultLabel(%+v) = %q, want %q", c.s, got, c.want)
		}
	}
}

func TestTruncateOutput(t *testing.T) {
	// 未超限：原样返回
	out, truncated := truncateOutput("hello", 100)
	if truncated || out != "hello" {
		t.Errorf("未超限不应截断: truncated=%v out=%q", truncated, out)
	}
	// 超限：保留末尾 + 标注
	big := strings.Repeat("a", 1000) + "TAIL-END"
	out, truncated = truncateOutput(big, 100)
	if !truncated {
		t.Fatalf("超限应截断")
	}
	if !strings.Contains(out, "已截断") || !strings.HasSuffix(out, "TAIL-END") {
		t.Errorf("应保留末尾并带标注: %q", out)
	}
	// 中文不切坏（UTF-8 边界）
	chinese := strings.Repeat("中", 500)
	out, _ = truncateOutput(chinese, 100)
	for _, r := range out {
		if r == 0xFFFD {
			t.Errorf("截断产生了非法字符: %q", out)
		}
	}
}

func TestLastLines(t *testing.T) {
	if got := lastLines("", 5); got != "" {
		t.Errorf("空输出: %q", got)
	}
	if got := lastLines("a\nb\nc", 5); got != "a\nb\nc" {
		t.Errorf("不足行数应原样返回: %q", got)
	}
	if got := lastLines("1\n2\n3\n4\n5\n6\n7", 3); got != "5\n6\n7" {
		t.Errorf("末尾3行错误: %q", got)
	}
}

func TestIsInterrupted(t *testing.T) {
	if isInterrupted(nil) {
		t.Errorf("nil 不应视为中断")
	}
	// 收到退出码的普通失败不算中断
	if isInterrupted(&ssh.ExitError{}) {
		t.Errorf("ExitError 不应视为中断")
	}
	// 未收到退出码（通道已关）：典型 init 0 / reboot 场景
	if !isInterrupted(&ssh.ExitMissingError{}) {
		t.Errorf("ExitMissingError 应视为中断")
	}
	// 网络层错误
	for _, msg := range []string{"EOF", "connection reset by peer", "broken pipe", "i/o timeout"} {
		if !isInterrupted(fmt.Errorf("%s", msg)) {
			t.Errorf("%q 应视为中断", msg)
		}
	}
	if isInterrupted(fmt.Errorf("other random error")) {
		t.Errorf("无关错误不应视为中断")
	}
}

func TestMergeOutput(t *testing.T) {
	if got := mergeOutput("", "err"); got != "err" {
		t.Errorf("merge empty out: %q", got)
	}
	if got := mergeOutput("out", ""); got != "out" {
		t.Errorf("merge empty err: %q", got)
	}
	if got := mergeOutput("out", "err"); got != "out\nerr" {
		t.Errorf("merge both: %q", got)
	}
}

func TestSanitizeFileName(t *testing.T) {
	if got := sanitizeFileName("web/01:主"); got != "web_01_主" {
		t.Errorf("sanitize 错误: %q", got)
	}
	if got := sanitizeFileName(""); got != "unknown" {
		t.Errorf("空名应返回 unknown: %q", got)
	}
}

// 失败现场回显：捕获 stderr，验证只回显状态与输出尾部（最后20行）
func TestPrintScriptFailure(t *testing.T) {
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	defer func() { os.Stderr = old }()

	var lines []string
	for i := 1; i <= 22; i++ {
		lines = append(lines, fmt.Sprintf("l%d", i))
	}
	vm := &VM{Name: "web-01", IP: "10.0.0.1"}
	s := &ScriptResult{OK: false, ExitCode: 1, State: "fail", Error: "exit status 1", Output: strings.Join(lines, "\n")}
	printScriptFailure(vm, s)
	w.Close()
	out, _ := io.ReadAll(r)
	got := string(out)

	if !strings.Contains(got, "[脚本] web-01 (10.0.0.1)") || !strings.Contains(got, "失败(exit 1)") {
		t.Errorf("应包含机器名与状态: %s", got)
	}
	if !strings.Contains(got, "原因: exit status 1") {
		t.Errorf("应回显原因: %s", got)
	}
	// 按行判断：应含 l3~l22，不含 l1/l2（避免 "l1" 命中 "l10" 这类子串误判）
	lineSet := map[string]bool{}
	for _, ln := range strings.Split(got, "\n") {
		lineSet[strings.TrimSpace(ln)] = true
	}
	if !lineSet["l3"] || !lineSet["l22"] || lineSet["l1"] || lineSet["l2"] {
		t.Errorf("应只回显最后20行(l3~l22): %s", got)
	}
}

// 脚本输出落盘：文件名净化 + 内容含状态/退出码/完整输出
func TestWriteScriptLog(t *testing.T) {
	dir := t.TempDir()
	vm := &VM{Name: "web/01:主", IP: "10.0.0.1"}
	s := &ScriptResult{OK: false, ExitCode: 7, State: "fail", Error: "exit status 7", Output: "line1\nline2"}
	writeScriptLog(dir, vm, s)

	files, _ := filepath.Glob(filepath.Join(dir, "*"))
	if len(files) != 1 {
		t.Fatalf("应生成一个日志文件: %v", files)
	}
	if strings.Contains(filepath.Base(files[0]), "/") || strings.Contains(filepath.Base(files[0]), ":") {
		t.Errorf("文件名应已净化: %s", filepath.Base(files[0]))
	}
	data, _ := os.ReadFile(files[0])
	content := string(data)
	for _, want := range []string{"web/01:主", "失败(exit 7)", "exit status 7", "line2"} {
		if !strings.Contains(content, want) {
			t.Errorf("日志缺少 %q: %s", want, content)
		}
	}
}

func TestScriptEnabled(t *testing.T) {
	cfg := &Config{}
	if cfg.ScriptEnabled() {
		t.Errorf("空配置不应启用脚本")
	}
	cfg = &Config{}
	cfg.SSH.Script = "echo hi"
	if !cfg.ScriptEnabled() {
		t.Errorf("内嵌脚本应启用")
	}
	cfg = &Config{}
	cfg.SSH.ScriptPath = "x.sh"
	if !cfg.ScriptEnabled() {
		t.Errorf("脚本路径应启用")
	}
}

func TestScriptContentInline(t *testing.T) {
	cfg := &Config{}
	cfg.SSH.Script = "#!/bin/bash\necho hi\n"
	content, err := cfg.ScriptContent()
	if err != nil {
		t.Fatalf("内嵌脚本加载失败: %v", err)
	}
	if content != "#!/bin/bash\necho hi\n" {
		t.Errorf("内嵌脚本内容错误: %q", content)
	}
	// 重复调用应返回相同结果（sync.Once 缓存）
	content2, _ := cfg.ScriptContent()
	if content2 != content {
		t.Errorf("缓存失效")
	}
}

func TestScriptContentFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ops.sh")
	script := "echo from-file\n"
	if err := os.WriteFile(path, []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &Config{}
	cfg.SSH.ScriptPath = path
	content, err := cfg.ScriptContent()
	if err != nil {
		t.Fatalf("文件脚本加载失败: %v", err)
	}
	if content != script {
		t.Errorf("文件脚本内容错误: %q", content)
	}
}

func TestScriptContentFileMissing(t *testing.T) {
	cfg := &Config{}
	cfg.SSH.ScriptPath = filepath.Join(t.TempDir(), "not-exist.sh")
	if _, err := cfg.ScriptContent(); err == nil {
		t.Errorf("文件不存在应报错")
	}
}

func TestLoadConfigScriptMutualExclusion(t *testing.T) {
	yaml := `
endpoint: "https://127.0.0.1:30990"
accessKeyId: "ak"
accessKeySecret: "sk"
regionId: "cn-beijing"
project:
  names: ["default"]
ssh:
  script: "echo hi"
  scriptPath: "ops.sh"
`
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(path); err == nil || !strings.Contains(err.Error(), "只能配置一个") {
		t.Errorf("script 与 scriptPath 同时配置应报错: %v", err)
	}
}
