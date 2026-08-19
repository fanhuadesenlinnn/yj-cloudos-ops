package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------- 配置与辅助函数 ----------

func TestUploadEnabled(t *testing.T) {
	cfg := &Config{}
	if cfg.UploadEnabled() {
		t.Errorf("空配置不应启用上传")
	}
	cfg.SSH.Upload = []UploadFile{{Local: "a.sh", Remote: "/opt/a.sh"}}
	if !cfg.UploadEnabled() {
		t.Errorf("配置了 upload 应启用")
	}
}

func TestUploadMkdirsEnabled(t *testing.T) {
	if got := (&Config{}).UploadMkdirsEnabled(); !got {
		t.Errorf("未配置 uploadMkdirs 应默认 true")
	}
	cfg := &Config{}
	cfg.SSH.UploadMkdirs = boolPtr(false)
	if got := cfg.UploadMkdirsEnabled(); got {
		t.Errorf("uploadMkdirs=false 应返回 false")
	}
}

func TestUploadShouldOverwrite(t *testing.T) {
	// 缺省用全局
	cfg := &Config{}
	if got := cfg.UploadShouldOverwrite(UploadFile{}); got {
		t.Errorf("全局默认应为不覆盖: %v", got)
	}
	cfg.SSH.UploadOverwrite = true
	if got := cfg.UploadShouldOverwrite(UploadFile{}); !got {
		t.Errorf("全局 uploadOverwrite=true 应覆盖")
	}
	// 单文件 overwrite 优先
	cfg.SSH.UploadOverwrite = true
	if got := cfg.UploadShouldOverwrite(UploadFile{Overwrite: boolPtr(false)}); got {
		t.Errorf("单文件 overwrite=false 应覆盖全局 true")
	}
	cfg.SSH.UploadOverwrite = false
	if got := cfg.UploadShouldOverwrite(UploadFile{Overwrite: boolPtr(true)}); !got {
		t.Errorf("单文件 overwrite=true 应覆盖全局 false")
	}
}

func TestParseFileMode(t *testing.T) {
	cases := []struct{ in, want string }{
		{"0755", "0755"},
		{"755", "0755"},
		{"644", "0644"},
		{"0644", "0644"},
		{"777", "0777"},
	}
	for _, c := range cases {
		m, err := parseFileMode(c.in)
		if err != nil {
			t.Errorf("parseFileMode(%q) 报错: %v", c.in, err)
			continue
		}
		if got := fmt.Sprintf("%04o", m); got != c.want {
			t.Errorf("parseFileMode(%q) = %s, want %s", c.in, got, c.want)
		}
	}
	if _, err := parseFileMode("888"); err == nil {
		t.Errorf("非法八进制 888 应报错")
	}
	if _, err := parseFileMode("xyz"); err == nil {
		t.Errorf("非法权限 xyz 应报错")
	}
}

func TestUploadFileModeDefault(t *testing.T) {
	cfg := &Config{}
	m, err := cfg.UploadFileMode(UploadFile{})
	if err != nil {
		t.Fatalf("默认权限解析失败: %v", err)
	}
	if got := fmt.Sprintf("%04o", m); got != "0644" {
		t.Errorf("默认权限应为 0644: %s", got)
	}
	cfg.SSH.Upload = []UploadFile{{Local: "a", Remote: "/a", Mode: "0755"}}
	m, err = cfg.UploadFileMode(cfg.SSH.Upload[0])
	if err != nil {
		t.Fatalf("0755 解析失败: %v", err)
	}
	if got := fmt.Sprintf("%04o", m); got != "0755" {
		t.Errorf("权限应为 0755: %s", got)
	}
}

func TestShellQuote(t *testing.T) {
	if got := shellQuote("/opt/my app"); got != "'/opt/my app'" {
		t.Errorf("含空格路径转义错误: %s", got)
	}
	if got := shellQuote("/opt/it's"); got != "'/opt/it'\\''s'" {
		t.Errorf("含单引号路径应转义: %s", got)
	}
	// 危险字符应被引号包裹（不被当作命令分隔符执行）
	if got := shellQuote("/opt/x;rm -rf /"); got != "'/opt/x;rm -rf /'" {
		t.Errorf("危险字符应整体加引号: %s", got)
	}
}

func TestFirstUploadError(t *testing.T) {
	if got := firstUploadError(nil); got != nil {
		t.Errorf("nil 应返回 nil")
	}
	ups := []*UploadResult{{State: "success"}, {State: "skipped"}}
	if got := firstUploadError(ups); got != nil {
		t.Errorf("无 error 应返回 nil: %+v", got)
	}
	ups = append(ups, &UploadResult{State: "error", Error: "boom"})
	if got := firstUploadError(ups); got == nil || got.Error != "boom" {
		t.Errorf("应返回第一个 error: %+v", got)
	}
}

func TestUploadStr(t *testing.T) {
	if got := uploadStr(&VM{}); got != "—" {
		t.Errorf("未配置上传应显示 —: %q", got)
	}
	if got := uploadStr(&VM{Uploads: []*UploadResult{{State: "success"}, {State: "success"}}}); got != "✓ 2/2" {
		t.Errorf("全部成功: %q", got)
	}
	if got := uploadStr(&VM{Uploads: []*UploadResult{{State: "success"}, {State: "skipped"}}}); got != "✓ 1/2(跳过1)" {
		t.Errorf("含跳过: %q", got)
	}
	if got := uploadStr(&VM{Uploads: []*UploadResult{{State: "success"}, {State: "error"}}}); got != "✗ 1失败" {
		t.Errorf("含失败: %q", got)
	}
}

func TestUploadResultLabel(t *testing.T) {
	if got := uploadResultLabel(&UploadResult{State: "success"}); got != "上传成功" {
		t.Errorf("新建应标上传成功: %q", got)
	}
	if got := uploadResultLabel(&UploadResult{State: "success", Overwritten: true}); got != "已覆盖" {
		t.Errorf("覆盖应标已覆盖: %q", got)
	}
	if got := uploadResultLabel(&UploadResult{State: "skipped"}); got != "跳过(已存在)" {
		t.Errorf("跳过标签错误: %q", got)
	}
	if got := uploadResultLabel(&UploadResult{State: "error"}); got != "失败" {
		t.Errorf("失败标签错误: %q", got)
	}
}

func TestLoadConfigUploadValidation(t *testing.T) {
	base := `
endpoint: "https://127.0.0.1:30990"
accessKeyId: "ak"
accessKeySecret: "sk"
regionId: "cn-beijing"
project:
  names: ["default"]
ssh:
  upload:
`
	// remote 不是绝对路径 -> 报错
	yaml := base + `    - local: "a.sh"
      remote: "opt/a.sh"
`
	path := filepath.Join(t.TempDir(), "config.yaml")
	os.WriteFile(path, []byte(yaml), 0o644)
	if _, err := loadConfig(path); err == nil || !strings.Contains(err.Error(), "绝对路径") {
		t.Errorf("relative remote 应报错: %v", err)
	}

	// mode 非法 -> 报错
	yaml = base + `    - local: "a.sh"
      remote: "/opt/a.sh"
      mode: "888"
`
	os.WriteFile(path, []byte(yaml), 0o644)
	if _, err := loadConfig(path); err == nil || !strings.Contains(err.Error(), "mode") {
		t.Errorf("非法 mode 应报错: %v", err)
	}

	// remoteWorkDir 不是绝对路径 -> 报错
	yaml = base + `    - local: "a.sh"
      remote: "/opt/a.sh"
  remoteWorkDir: "opt"
`
	os.WriteFile(path, []byte(yaml), 0o644)
	if _, err := loadConfig(path); err == nil || !strings.Contains(err.Error(), "remoteWorkDir") {
		t.Errorf("relative remoteWorkDir 应报错: %v", err)
	}

	// 合法配置 -> 通过
	yaml = base + `    - local: "a.sh"
      remote: "/opt/a.sh"
      mode: "0755"
      overwrite: true
    - local: "b.conf"
      remote: "/etc/b.conf"
`
	os.WriteFile(path, []byte(yaml), 0o644)
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("合法配置不应报错: %v", err)
	}
	if len(cfg.SSH.Upload) != 2 {
		t.Fatalf("upload 数量错误: %d", len(cfg.SSH.Upload))
	}
	if !cfg.UploadShouldOverwrite(cfg.SSH.Upload[0]) {
		t.Errorf("单文件 overwrite=true 应生效")
	}
	if cfg.UploadShouldOverwrite(cfg.SSH.Upload[1]) {
		t.Errorf("缺省应不覆盖")
	}
}

// ---------- 端到端：进程内 SSH + SFTP 上传 ----------

// inProcUploadCfg 构造指向带 SFTP 支持的进程内 SSH 服务器的配置
func inProcUploadCfg(addr string) *Config {
	cfg := inProcSSHCfg(addr)
	cfg.SSH.ScriptTimeout = "5s"
	return cfg
}

// 上传到指定位置（自动创建父目录），可校验内容与权限
func TestUploadInProc(t *testing.T) {
	remoteRoot := t.TempDir()
	addr := startInProcSSHServerOpts(t, "Test@12345", inProcOpts{SFTPDir: remoteRoot})
	cfg := inProcUploadCfg(addr)

	local := filepath.Join(t.TempDir(), "deploy.sh")
	os.WriteFile(local, []byte("#!/bin/bash\necho hello-upload\n"), 0o644)
	remote := filepath.Join(remoteRoot, "opt/myapp/deploy.sh")
	cfg.SSH.Upload = []UploadFile{
		{Local: local, Remote: remote, Mode: "0755"},
	}
	cfg.SSH.Script = "bash deploy.sh"
	cfg.SSH.RemoteWorkDir = filepath.Join(remoteRoot, "opt/myapp")

	_, _, uploads, script, err := trySSH(cfg, "127.0.0.1", "Test@12345")
	if err != nil {
		t.Fatalf("trySSH 失败: %v", err)
	}
	if len(uploads) != 1 || uploads[0].State != "success" {
		t.Fatalf("上传应成功: %+v", uploads)
	}
	if uploads[0].Overwritten {
		t.Errorf("首次上传不应标记为覆盖: %+v", uploads[0])
	}

	// 远端文件内容与权限
	got, err := os.ReadFile(remote)
	if err != nil {
		t.Fatalf("远端文件不存在: %v", err)
	}
	if !strings.Contains(string(got), "hello-upload") {
		t.Errorf("远端内容错误: %q", got)
	}
	info, err := os.Stat(remote)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm().String() != "-rwxr-xr-x" {
		t.Errorf("权限应为 0755: %v", info.Mode().Perm())
	}

	// remoteWorkDir 生效：脚本在 opt/myapp 下执行 bash deploy.sh
	if script == nil || !script.OK || script.State != "success" {
		t.Fatalf("脚本应在上传目录执行成功: %+v", script)
	}
	if !strings.Contains(script.Output, "hello-upload") {
		t.Errorf("脚本输出缺失: %q", script.Output)
	}
}

// 覆盖语义：默认不覆盖 -> 同名已存在则跳过；overwrite=true -> 替换内容
func TestUploadOverwriteInProc(t *testing.T) {
	remoteRoot := t.TempDir()
	addr := startInProcSSHServerOpts(t, "Test@12345", inProcOpts{SFTPDir: remoteRoot})
	cfg := inProcUploadCfg(addr)

	dir := t.TempDir()
	local := filepath.Join(dir, "deploy.sh")
	os.WriteFile(local, []byte("NEW-CONTENT\n"), 0o644)

	remoteFile := filepath.Join(remoteRoot, "opt/deploy.sh")
	os.MkdirAll(filepath.Dir(remoteFile), 0o755)
	os.WriteFile(remoteFile, []byte("OLD-CONTENT\n"), 0o644)

	// 1. 默认不覆盖：跳过，远端内容保持旧值
	cfg.SSH.Upload = []UploadFile{{Local: local, Remote: "opt/deploy.sh"}}
	_, _, uploads, _, err := trySSH(cfg, "127.0.0.1", "Test@12345")
	if err != nil {
		t.Fatalf("trySSH 失败: %v", err)
	}
	if len(uploads) != 1 || uploads[0].State != "skipped" {
		t.Fatalf("同名已存在且未开启覆盖应跳过: %+v", uploads)
	}
	data, _ := os.ReadFile(remoteFile)
	if !strings.Contains(string(data), "OLD-CONTENT") {
		t.Errorf("跳过时远端内容不应被修改: %q", data)
	}

	// 2. 单文件 overwrite=true：覆盖，内容替换为新值
	cfg.SSH.Upload = []UploadFile{{Local: local, Remote: "opt/deploy.sh", Overwrite: boolPtr(true)}}
	_, _, uploads, _, err = trySSH(cfg, "127.0.0.1", "Test@12345")
	if err != nil {
		t.Fatalf("trySSH 失败: %v", err)
	}
	if len(uploads) != 1 || uploads[0].State != "success" || !uploads[0].Overwritten {
		t.Fatalf("overwrite=true 应覆盖成功: %+v", uploads)
	}
	data, _ = os.ReadFile(remoteFile)
	if !strings.Contains(string(data), "NEW-CONTENT") {
		t.Errorf("覆盖后内容应更新: %q", data)
	}

	// 3. 全局 uploadOverwrite=true 且单文件缺省：覆盖
	os.WriteFile(remoteFile, []byte("OLD-AGAIN\n"), 0o644)
	cfg.SSH.UploadOverwrite = true
	cfg.SSH.Upload = []UploadFile{{Local: local, Remote: "opt/deploy.sh"}}
	_, _, uploads, _, err = trySSH(cfg, "127.0.0.1", "Test@12345")
	if err != nil {
		t.Fatalf("trySSH 失败: %v", err)
	}
	if len(uploads) != 1 || uploads[0].State != "success" || !uploads[0].Overwritten {
		t.Fatalf("全局覆盖应生效: %+v", uploads)
	}
}

// 上传失败（本地文件缺失）时脚本不执行，标记 error
func TestUploadFailBlocksScriptInProc(t *testing.T) {
	remoteRoot := t.TempDir()
	addr := startInProcSSHServerOpts(t, "Test@12345", inProcOpts{SFTPDir: remoteRoot})
	cfg := inProcUploadCfg(addr)

	cfg.SSH.Upload = []UploadFile{{Local: filepath.Join(t.TempDir(), "not-exist.sh"), Remote: "opt/x.sh"}}
	cfg.SSH.Script = "echo should-not-run"

	_, _, uploads, script, err := trySSH(cfg, "127.0.0.1", "Test@12345")
	if err != nil {
		t.Fatalf("trySSH 失败: %v", err)
	}
	if len(uploads) != 1 || uploads[0].State != "error" {
		t.Fatalf("上传应标记失败: %+v", uploads)
	}
	if script == nil || script.State != "error" || !strings.Contains(script.Error, "上传失败") {
		t.Fatalf("上传失败时脚本应标记 error 且不执行: %+v", script)
	}
}

// 只上传不执行脚本（upload 独立于 script）
func TestUploadWithoutScriptInProc(t *testing.T) {
	remoteRoot := t.TempDir()
	addr := startInProcSSHServerOpts(t, "Test@12345", inProcOpts{SFTPDir: remoteRoot})
	cfg := inProcUploadCfg(addr)

	dir := t.TempDir()
	local := filepath.Join(dir, "app.conf")
	os.WriteFile(local, []byte("key=value\n"), 0o644)
	cfg.SSH.Upload = []UploadFile{{Local: local, Remote: "etc/app.conf"}}

	_, _, uploads, script, err := trySSH(cfg, "127.0.0.1", "Test@12345")
	if err != nil {
		t.Fatalf("trySSH 失败: %v", err)
	}
	if len(uploads) != 1 || uploads[0].State != "success" {
		t.Fatalf("上传应成功: %+v", uploads)
	}
	if script != nil {
		t.Errorf("未配置脚本不应有脚本结果: %+v", script)
	}
	data, err := os.ReadFile(filepath.Join(remoteRoot, "etc/app.conf"))
	if err != nil {
		t.Fatalf("远端文件缺失: %v", err)
	}
	if string(data) != "key=value\n" {
		t.Errorf("远端内容错误: %q", data)
	}
}
