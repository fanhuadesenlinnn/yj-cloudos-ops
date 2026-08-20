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

func TestPipelineStr(t *testing.T) {
	if got := pipelineStr(&VM{}); got != "—" {
		t.Errorf("无步骤应显示 —: %q", got)
	}
	vm := &VM{ExecSteps: []*ExecStepResult{
		{Name: "a", State: "success"},
		{Name: "b", State: "fail"},
		{Name: "c", State: "timeout"},
		{Name: "d", State: "interrupted"},
		{Name: "e", State: "skipped"},
		{Name: "f", State: "error"},
	}}
	if got := pipelineStr(vm); got != "1✓ 2✗ 3超 4断 5- 6!" {
		t.Errorf("流水线摘要错误: %q", got)
	}
}

func TestStepResultLabel(t *testing.T) {
	cases := []struct {
		s    *ExecStepResult
		want string
	}{
		{nil, "未执行"},
		{&ExecStepResult{State: "success"}, "成功"},
		{&ExecStepResult{State: "fail", ExitCode: 42}, "失败(exit 42)"},
		{&ExecStepResult{State: "timeout"}, "超时"},
		{&ExecStepResult{State: "interrupted"}, "会话中断(疑似关机/重启)"},
		{&ExecStepResult{State: "skipped"}, "未执行(上游失败)"},
		{&ExecStepResult{State: "error", Error: "读取脚本文件 x 失败"}, "未执行: 读取脚本文件 x 失败"},
	}
	for _, c := range cases {
		if got := stepResultLabel(c.s); got != c.want {
			t.Errorf("stepResultLabel(%+v) = %q, want %q", c.s, got, c.want)
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
func TestPrintStepFailure(t *testing.T) {
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
	s := &ExecStepResult{Name: "部署", Type: "command", Target: "remote", ExitCode: 1, State: "fail", Error: "exit status 1", Output: strings.Join(lines, "\n")}
	printStepFailure(vm, s)
	w.Close()
	out, _ := io.ReadAll(r)
	got := string(out)

	if !strings.Contains(got, "[流水线] web-01 (10.0.0.1) 步骤[部署]") || !strings.Contains(got, "失败(exit 1)") {
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
	s := &ExecStepResult{Name: "部署命令", Type: "command", Target: "remote", ExitCode: 7, State: "fail", Error: "exit status 7", Output: "line1\nline2"}
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
	for _, want := range []string{"web/01:主", "失败(exit 7)", "exit status 7", "line2", "部署命令"} {
		if !strings.Contains(content, want) {
			t.Errorf("日志缺少 %q: %s", want, content)
		}
	}
}

func TestStepCommandContent(t *testing.T) {
	// command 优先
	content, err := StepCommandContent(ExecStep{Command: "echo hi", Script: "echo no"})
	if err != nil || content != "echo hi" {
		t.Errorf("command 应优先: %q err=%v", content, err)
	}
	// 内嵌 script
	content, err = StepCommandContent(ExecStep{Script: "echo inline\n"})
	if err != nil || content != "echo inline\n" {
		t.Errorf("内嵌脚本内容错误: %q err=%v", content, err)
	}
	// scriptPath 读取文件
	dir := t.TempDir()
	path := filepath.Join(dir, "ops.sh")
	if err := os.WriteFile(path, []byte("echo from-file\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	content, err = StepCommandContent(ExecStep{ScriptPath: path})
	if err != nil || content != "echo from-file\n" {
		t.Errorf("文件脚本内容错误: %q err=%v", content, err)
	}
	// scriptPath 文件为 Windows CRLF -> 统一为 \n
	crlfPath := filepath.Join(dir, "win.sh")
	if err := os.WriteFile(crlfPath, []byte("echo a\r\necho b\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	content, err = StepCommandContent(ExecStep{ScriptPath: crlfPath})
	if err != nil || content != "echo a\necho b\n" {
		t.Errorf("CRLF 文件应归一化为 \\n: %q err=%v", content, err)
	}
	// scriptPath 文件为老 Mac 单独 CR -> 统一为 \n
	crPath := filepath.Join(dir, "mac.sh")
	if err := os.WriteFile(crPath, []byte("echo a\recho b\r"), 0o644); err != nil {
		t.Fatal(err)
	}
	content, err = StepCommandContent(ExecStep{ScriptPath: crPath})
	if err != nil || content != "echo a\necho b\n" {
		t.Errorf("单独 CR 文件应归一化为 \\n: %q err=%v", content, err)
	}
	// 文件不存在 -> 报错
	content, err = StepCommandContent(ExecStep{ScriptPath: filepath.Join(dir, "not-exist.sh")})
	if err == nil {
		t.Errorf("文件不存在应报错")
	}
}

func TestStepRunModeAndOnError(t *testing.T) {
	// 默认值
	if got := StepRunMode(ExecStep{Type: "command", Target: "local"}); got != "once" {
		t.Errorf("command local 默认 once: %q", got)
	}
	if got := StepRunMode(ExecStep{Type: "command", Target: "remote"}); got != "always" {
		t.Errorf("command remote 默认 always: %q", got)
	}
	if got := StepRunMode(ExecStep{Type: "command"}); got != "always" {
		t.Errorf("未配置 target 默认 remote/always: %q", got)
	}
	if got := StepRunMode(ExecStep{Type: "files", Target: "push"}); got != "always" {
		t.Errorf("files 模块固定 always: %q", got)
	}
	if got := StepOnError(ExecStep{}); got != "stop" {
		t.Errorf("onError 默认 stop: %q", got)
	}
	// 显式配置
	if got := StepRunMode(ExecStep{Type: "command", Target: "local", Run: "always"}); got != "always" {
		t.Errorf("显式 always: %q", got)
	}
	if got := StepOnError(ExecStep{OnError: "continue"}); got != "continue" {
		t.Errorf("显式 continue: %q", got)
	}
}

func TestLoadConfigExecListValidation(t *testing.T) {
	base := `
endpoint: "https://127.0.0.1:30990"
accessKeyId: "ak"
accessKeySecret: "sk"
regionId: "cn-beijing"
project:
  names: ["default"]
`
	writeCfg := func(body string) (*Config, error) {
		path := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(path, []byte(base+body), 0o644); err != nil {
			t.Fatal(err)
		}
		return loadConfig(path)
	}

	// 未知步骤类型 -> 报错
	if _, err := writeCfg("execList:\n    - type: \"foo\"\n"); err == nil || !strings.Contains(err.Error(), "type") {
		t.Errorf("未知 type 应报错: %v", err)
	}
	// upload 缺 files -> 报错
	if _, err := writeCfg("execList:\n    - type: \"upload\"\n"); err == nil || !strings.Contains(err.Error(), "files") {
		t.Errorf("upload 缺 files 应报错: %v", err)
	}
	// files push remote 非绝对路径 -> 报错
	if _, err := writeCfg("execList:\n    - type: \"files\"\n      target: push\n      files:\n        - local: \"a.sh\"\n          remote: \"opt/a.sh\"\n"); err == nil || !strings.Contains(err.Error(), "绝对路径") {
		t.Errorf("relative remote 应报错: %v", err)
	}
	// files push remote 以 / 结尾（未精确到文件）-> 报错
	if _, err := writeCfg("execList:\n    - type: \"files\"\n      target: push\n      files:\n        - local: \"a.sh\"\n          remote: \"/opt/\"\n"); err == nil || !strings.Contains(err.Error(), "精确到文件") {
		t.Errorf("remote 以 / 结尾应报错: %v", err)
	}
	// files mode 非法 -> 报错
	if _, err := writeCfg("execList:\n    - type: \"files\"\n      target: push\n      files:\n        - local: \"a.sh\"\n          remote: \"/opt/a.sh\"\n          mode: \"888\"\n"); err == nil || !strings.Contains(err.Error(), "mode") {
		t.Errorf("非法 mode 应报错: %v", err)
	}
	// files target 必填（不写 -> 报错）
	if _, err := writeCfg("execList:\n    - type: \"files\"\n      files:\n        - local: \"a.sh\"\n          remote: \"/opt/a.sh\"\n"); err == nil || !strings.Contains(err.Error(), "target") {
		t.Errorf("files 缺 target 应报错: %v", err)
	}
	// files target 非法值 -> 报错
	if _, err := writeCfg("execList:\n    - type: \"files\"\n      target: \"local\"\n      files:\n        - local: \"a.sh\"\n          remote: \"/opt/a.sh\"\n"); err == nil || !strings.Contains(err.Error(), "push / pull") {
		t.Errorf("files target=local 应报错: %v", err)
	}
	// command 缺内容 -> 报错
	if _, err := writeCfg("execList:\n    - type: \"command\"\n"); err == nil || !strings.Contains(err.Error(), "command / script / scriptPath") {
		t.Errorf("command 缺内容应报错: %v", err)
	}
	// script command 与 script 同时配置 -> 报错
	if _, err := writeCfg("execList:\n    - type: \"command\"\n      command: \"echo a\"\n      script: \"echo b\"\n"); err == nil || !strings.Contains(err.Error(), "只能配置一个") {
		t.Errorf("command+script 同时配置应报错: %v", err)
	}
	// script timeout 非法 -> 报错
	if _, err := writeCfg("execList:\n    - type: \"command\"\n      command: \"echo a\"\n      timeout: \"abc\"\n"); err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Errorf("非法 timeout 应报错: %v", err)
	}
	// command workdir（远端）非绝对路径 -> 报错
	if _, err := writeCfg("execList:\n    - type: \"command\"\n      command: \"echo a\"\n      workdir: \"opt\"\n"); err == nil || !strings.Contains(err.Error(), "workdir") {
		t.Errorf("remote workdir 非绝对路径应报错: %v", err)
	}
	// command workdir（本地）允许相对路径 -> 通过
	if _, err := writeCfg("execList:\n    - type: \"command\"\n      target: local\n      command: \"echo a\"\n      workdir: \"opt\"\n"); err != nil {
		t.Errorf("local workdir 相对路径不应报错: %v", err)
	}
	// run / onError 非法 -> 报错
	if _, err := writeCfg("execList:\n    - type: \"command\"\n      command: \"echo a\"\n      run: \"sometimes\"\n"); err == nil || !strings.Contains(err.Error(), "run") {
		t.Errorf("非法 run 应报错: %v", err)
	}
	if _, err := writeCfg("execList:\n    - type: \"command\"\n      command: \"echo a\"\n      onError: \"maybe\"\n"); err == nil || !strings.Contains(err.Error(), "onError") {
		t.Errorf("非法 onError 应报错: %v", err)
	}
	// 合法配置 -> 通过
	cfg, err := writeCfg(`execList:
  - name: "上传"
    type: files
    target: push
    files:
      - local: "a.sh"
        remote: "/opt/a.sh"
        mode: "0755"
        overwrite: true
      - local: "b.conf"
        remote: "/etc/b.conf"
  - name: "部署"
    type: command
    target: remote
    workdir: "/opt/app"
    command: "bash /opt/a.sh"
    timeout: 30s
  - name: "检查服务"
    type: services
    services: ["sshd", "docker"]
  - name: "采集状态"
    type: status
`)
	if err != nil {
		t.Fatalf("合法配置不应报错: %v", err)
	}
	steps := cfg.ExecList
	if len(steps) != 4 {
		t.Fatalf("步骤数量错误: %d", len(steps))
	}
	if !StepShouldOverwrite(steps[0], steps[0].Files[0]) {
		t.Errorf("单文件 overwrite=true 应生效")
	}
	if StepShouldOverwrite(steps[0], steps[0].Files[1]) {
		t.Errorf("缺省应不覆盖")
	}
	if got := StepServiceNames(steps[2]); len(got) != 2 || got[0] != "sshd" {
		t.Errorf("services 列表错误: %v", got)
	}
}

// 未配置 exec-list 时走默认流水线（status -> services）
func TestEffectiveStepsDefault(t *testing.T) {
	cfg := &Config{}
	steps := cfg.EffectiveSteps()
	if len(steps) != 2 {
		t.Fatalf("默认流水线应为2步: %d", len(steps))
	}
	if steps[0].Type != "status" || steps[1].Type != "services" {
		t.Errorf("默认流水线应为 status -> services: %+v", steps)
	}
	// 显式空列表（execList: []）：不执行任何步骤，只测 SSH 连通性
	cfg.ExecList = []ExecStep{}
	if steps := cfg.EffectiveSteps(); len(steps) != 0 {
		t.Errorf("显式空 execList 应返回空流水线: %+v", steps)
	}
	// 配置了 exec-list 则用配置的
	cfg.ExecList = []ExecStep{{Type: "command", Command: "echo hi"}}
	steps = cfg.EffectiveSteps()
	if len(steps) != 1 || steps[0].Type != "command" {
		t.Errorf("配置 exec-list 后应使用配置的: %+v", steps)
	}
}

func TestStepName(t *testing.T) {
	if got := StepName(ExecStep{Name: "上传"}, 0); got != "上传" {
		t.Errorf("应使用配置的步骤名: %q", got)
	}
	if got := StepName(ExecStep{}, 2); got != "step3" {
		t.Errorf("缺省应自动生成 step3: %q", got)
	}
}
