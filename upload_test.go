package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------- 配置与辅助函数 ----------

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

func TestStepFileModeDefault(t *testing.T) {
	step := ExecStep{Type: "files", Target: "push"}
	m, err := StepFileMode(step, StepUploadFile{})
	if err != nil {
		t.Fatalf("默认权限解析失败: %v", err)
	}
	if got := fmt.Sprintf("%04o", m); got != "0644" {
		t.Errorf("默认权限应为 0644: %s", got)
	}
	m, err = StepFileMode(step, StepUploadFile{Mode: "0755"})
	if err != nil {
		t.Fatalf("0755 解析失败: %v", err)
	}
	if got := fmt.Sprintf("%04o", m); got != "0755" {
		t.Errorf("权限应为 0755: %s", got)
	}
}

func TestStepShouldOverwrite(t *testing.T) {
	// 步骤级缺省：不覆盖
	if got := StepShouldOverwrite(ExecStep{}, StepUploadFile{}); got {
		t.Errorf("步骤级默认应为不覆盖")
	}
	// 步骤级 overwrite=true
	if got := StepShouldOverwrite(ExecStep{Overwrite: boolPtr(true)}, StepUploadFile{}); !got {
		t.Errorf("步骤级 overwrite=true 应覆盖")
	}
	// 单文件 overwrite 优先于步骤级
	if got := StepShouldOverwrite(ExecStep{Overwrite: boolPtr(true)}, StepUploadFile{Overwrite: boolPtr(false)}); got {
		t.Errorf("单文件 overwrite=false 应覆盖步骤级 true")
	}
	if got := StepShouldOverwrite(ExecStep{Overwrite: boolPtr(false)}, StepUploadFile{Overwrite: boolPtr(true)}); !got {
		t.Errorf("单文件 overwrite=true 应覆盖步骤级 false")
	}
}

func TestStepMkdirsEnabled(t *testing.T) {
	if got := StepMkdirsEnabled(ExecStep{}); !got {
		t.Errorf("未配置 mkdirs 应默认 true")
	}
	if got := StepMkdirsEnabled(ExecStep{Mkdirs: boolPtr(false)}); got {
		t.Errorf("mkdirs=false 应返回 false")
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

func TestFilesResultLine(t *testing.T) {
	if got := filesResultLine(&UploadResult{State: "success", Local: "a", Remote: "/x/a", Mode: "0644"}); got != "✓ a -> /x/a (0644)" {
		t.Errorf("新建上传行错误: %q", got)
	}
	if got := filesResultLine(&UploadResult{State: "success", Overwritten: true, Local: "a", Remote: "/x/a", Mode: "0644"}); got != "✓ 已覆盖 a -> /x/a (0644)" {
		t.Errorf("覆盖上传行错误: %q", got)
	}
	if got := filesResultLine(&UploadResult{State: "skipped", Local: "a", Remote: "/x/a", Error: "已存在"}); !strings.Contains(got, "跳过") {
		t.Errorf("跳过行错误: %q", got)
	}
	if got := filesResultLine(&UploadResult{State: "error", Local: "a", Remote: "/x/a", Error: "boom"}); !strings.Contains(got, "失败") {
		t.Errorf("失败行错误: %q", got)
	}
}

// expandLocalFiles：文件 -> 单条；文件夹 -> 递归展开为 remote/<相对路径>
func TestExpandLocalFiles(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "sub"), 0o755)
	os.WriteFile(filepath.Join(dir, "a.sh"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, "sub", "b.conf"), []byte("y"), 0o644)

	// 文件
	pairs, err := expandLocalFiles(StepUploadFile{Local: filepath.Join(dir, "a.sh"), Remote: "/opt/app/a.sh"})
	if err != nil {
		t.Fatalf("文件展开失败: %v", err)
	}
	if len(pairs) != 1 || pairs[0].remote != "/opt/app/a.sh" {
		t.Errorf("文件应单条精确映射: %+v", pairs)
	}

	// 文件夹
	pairs, err = expandLocalFiles(StepUploadFile{Local: dir, Remote: "/opt/app"})
	if err != nil {
		t.Fatalf("文件夹展开失败: %v", err)
	}
	got := map[string]string{}
	for _, p := range pairs {
		got[p.remote] = p.local
	}
	if len(got) != 2 {
		t.Errorf("应展开2个文件: %+v", got)
	}
	if got["/opt/app/a.sh"] == "" {
		t.Errorf("缺少 /opt/app/a.sh: %+v", got)
	}
	if got["/opt/app/sub/b.conf"] == "" {
		t.Errorf("缺少 /opt/app/sub/b.conf（保持目录结构）: %+v", got)
	}

	// 本地不存在 -> 报错
	if _, err := expandLocalFiles(StepUploadFile{Local: filepath.Join(dir, "nope"), Remote: "/opt/x"}); err == nil {
		t.Errorf("本地路径不存在应报错")
	}
}

// ---------- 端到端：进程内 SSH + SFTP 上传 ----------

// inProcUploadCfg 构造指向带 SFTP 支持的进程内 SSH 服务器的配置
func inProcUploadCfg(addr string) *Config {
	cfg := inProcSSHCfg(addr)
	cfg.ExecList = []ExecStep{
		{Name: "上传", Type: "files", Target: "push"},
		{Name: "命令", Type: "command", Target: "remote", Timeout: "5s"},
	}
	return cfg
}

// 上传到指定位置（自动创建父目录），可校验内容与权限；随后命令在 workdir 下执行
func TestUploadInProc(t *testing.T) {
	remoteRoot := t.TempDir()
	addr := startInProcSSHServerOpts(t, "Test@12345", inProcOpts{SFTPDir: remoteRoot})
	cfg := inProcUploadCfg(addr)

	local := filepath.Join(t.TempDir(), "deploy.sh")
	os.WriteFile(local, []byte("#!/bin/bash\necho hello-upload\n"), 0o644)
	remote := filepath.Join(remoteRoot, "opt/myapp/deploy.sh")
	cfg.ExecList[0].Files = []StepUploadFile{{Local: local, Remote: remote, Mode: "0755"}}
	cfg.ExecList[1].Script = "bash deploy.sh"
	cfg.ExecList[1].Workdir = filepath.Join(remoteRoot, "opt/myapp")

	_, _, steps, err := trySSH(cfg, "127.0.0.1", "Test@12345", nil, false, false)
	if err != nil {
		t.Fatalf("trySSH 失败: %v", err)
	}
	up := steps[0]
	if up == nil || up.State != "success" {
		t.Fatalf("上传应成功: %+v", up)
	}
	if !strings.Contains(up.Output, "hello-upload") && !strings.Contains(up.Output, "deploy.sh") {
		t.Errorf("上传输出应含文件信息: %q", up.Output)
	}
	if strings.Contains(up.Output, "已覆盖") {
		t.Errorf("首次上传不应标记为覆盖: %q", up.Output)
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

	// workdir 生效：命令在 opt/myapp 下执行 bash deploy.sh
	scr := steps[1]
	if scr == nil || scr.State != "success" {
		t.Fatalf("脚本应在上传目录执行成功: %+v", scr)
	}
	if !strings.Contains(scr.Output, "hello-upload") {
		t.Errorf("脚本输出缺失: %q", scr.Output)
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
	cfg.ExecList[0].Files = []StepUploadFile{{Local: local, Remote: remoteFile}}
	_, _, steps, err := trySSH(cfg, "127.0.0.1", "Test@12345", nil, false, false)
	if err != nil {
		t.Fatalf("trySSH 失败: %v", err)
	}
	if steps[0].State != "success" || !strings.Contains(steps[0].Output, "跳过") {
		t.Fatalf("同名已存在且未开启覆盖应跳过: %+v", steps[0])
	}
	data, _ := os.ReadFile(remoteFile)
	if !strings.Contains(string(data), "OLD-CONTENT") {
		t.Errorf("跳过时远端内容不应被修改: %q", data)
	}

	// 2. 单文件 overwrite=true：覆盖，内容替换为新值
	cfg.ExecList[0].Files = []StepUploadFile{{Local: local, Remote: remoteFile, Overwrite: boolPtr(true)}}
	_, _, steps, err = trySSH(cfg, "127.0.0.1", "Test@12345", nil, false, false)
	if err != nil {
		t.Fatalf("trySSH 失败: %v", err)
	}
	if steps[0].State != "success" || !strings.Contains(steps[0].Output, "已覆盖") {
		t.Fatalf("overwrite=true 应覆盖成功: %+v", steps[0])
	}
	data, _ = os.ReadFile(remoteFile)
	if !strings.Contains(string(data), "NEW-CONTENT") {
		t.Errorf("覆盖后内容应更新: %q", data)
	}

	// 3. 步骤级 overwrite=true 且单文件缺省：覆盖
	os.WriteFile(remoteFile, []byte("OLD-AGAIN\n"), 0o644)
	cfg.ExecList[0].Overwrite = boolPtr(true)
	cfg.ExecList[0].Files = []StepUploadFile{{Local: local, Remote: remoteFile}}
	_, _, steps, err = trySSH(cfg, "127.0.0.1", "Test@12345", nil, false, false)
	if err != nil {
		t.Fatalf("trySSH 失败: %v", err)
	}
	if steps[0].State != "success" || !strings.Contains(steps[0].Output, "已覆盖") {
		t.Fatalf("步骤级覆盖应生效: %+v", steps[0])
	}
}

// 上传失败（本地文件缺失）时，onError=stop 的后续脚本步骤不执行（skipped）
func TestUploadFailBlocksScriptInProc(t *testing.T) {
	remoteRoot := t.TempDir()
	addr := startInProcSSHServerOpts(t, "Test@12345", inProcOpts{SFTPDir: remoteRoot})
	cfg := inProcUploadCfg(addr)

	cfg.ExecList[0].Files = []StepUploadFile{{Local: filepath.Join(t.TempDir(), "not-exist.sh"), Remote: filepath.Join(remoteRoot, "opt/x.sh")}}
	cfg.ExecList[1].Script = "echo should-not-run"

	_, _, steps, err := trySSH(cfg, "127.0.0.1", "Test@12345", nil, false, false)
	if err != nil {
		t.Fatalf("trySSH 失败: %v", err)
	}
	if steps[0].State != "error" {
		t.Fatalf("上传应标记失败: %+v", steps[0])
	}
	if steps[1].State != "skipped" {
		t.Fatalf("上传失败时后续脚本步骤应不执行: %+v", steps[1])
	}
}

// 上传失败但 onError=continue：后续脚本步骤照常执行
func TestUploadFailContinueInProc(t *testing.T) {
	remoteRoot := t.TempDir()
	addr := startInProcSSHServerOpts(t, "Test@12345", inProcOpts{SFTPDir: remoteRoot})
	cfg := inProcUploadCfg(addr)
	cfg.ExecList[0].OnError = "continue"
	cfg.ExecList[0].Files = []StepUploadFile{{Local: filepath.Join(t.TempDir(), "not-exist.sh"), Remote: filepath.Join(remoteRoot, "opt/x.sh")}}
	cfg.ExecList[1].Script = "echo still-run"

	_, _, steps, err := trySSH(cfg, "127.0.0.1", "Test@12345", nil, false, false)
	if err != nil {
		t.Fatalf("trySSH 失败: %v", err)
	}
	if steps[0].State != "error" {
		t.Fatalf("上传应标记失败: %+v", steps[0])
	}
	if steps[1].State != "success" {
		t.Fatalf("onError=continue 时脚本应执行: %+v", steps[1])
	}
}

// 只上传不执行脚本（upload 独立于 script）
func TestUploadWithoutScriptInProc(t *testing.T) {
	remoteRoot := t.TempDir()
	addr := startInProcSSHServerOpts(t, "Test@12345", inProcOpts{SFTPDir: remoteRoot})
	cfg := inProcSSHCfg(addr)
	cfg.ExecList = []ExecStep{
		{Name: "上传", Type: "files", Target: "push", Files: []StepUploadFile{{Local: func() string {
			p := filepath.Join(t.TempDir(), "app.conf")
			os.WriteFile(p, []byte("key=value\n"), 0o644)
			return p
		}(), Remote: filepath.Join(remoteRoot, "etc/app.conf")}}},
	}

	_, _, steps, err := trySSH(cfg, "127.0.0.1", "Test@12345", nil, false, false)
	if err != nil {
		t.Fatalf("trySSH 失败: %v", err)
	}
	if len(steps) != 1 || steps[0].State != "success" {
		t.Fatalf("上传应成功: %+v", steps)
	}
	data, err := os.ReadFile(filepath.Join(remoteRoot, "etc/app.conf"))
	if err != nil {
		t.Fatalf("远端文件缺失: %v", err)
	}
	if string(data) != "key=value\n" {
		t.Errorf("远端内容错误: %q", data)
	}
}

// 文件夹递归上传：保持目录结构，远端目标精确到文件
func TestUploadFolderInProc(t *testing.T) {
	remoteRoot := t.TempDir()
	addr := startInProcSSHServerOpts(t, "Test@12345", inProcOpts{SFTPDir: remoteRoot})
	cfg := inProcSSHCfg(addr)

	localDir := filepath.Join(t.TempDir(), "dist")
	os.MkdirAll(filepath.Join(localDir, "conf", "nested"), 0o755)
	os.WriteFile(filepath.Join(localDir, "app.sh"), []byte("#!/bin/bash\necho app\n"), 0o644)
	os.WriteFile(filepath.Join(localDir, "conf", "a.conf"), []byte("a=1\n"), 0o644)
	os.WriteFile(filepath.Join(localDir, "conf", "nested", "b.json"), []byte("{}\n"), 0o644)

	cfg.ExecList = []ExecStep{
		{Name: "上传目录", Type: "files", Target: "push", Files: []StepUploadFile{
			{Local: localDir, Remote: filepath.Join(remoteRoot, "opt/app"), Mode: "0644"},
		}},
	}

	_, _, steps, err := trySSH(cfg, "127.0.0.1", "Test@12345", nil, false, false)
	if err != nil {
		t.Fatalf("trySSH 失败: %v", err)
	}
	if steps[0].State != "success" {
		t.Fatalf("文件夹上传应成功: %+v", steps[0])
	}
	for _, want := range []string{
		"/opt/app/app.sh",
		"/opt/app/conf/a.conf",
		"/opt/app/conf/nested/b.json",
	} {
		p := filepath.Join(remoteRoot, "opt", "app", strings.TrimPrefix(want, "/opt/app/"))
		data, err := os.ReadFile(p)
		if err != nil {
			t.Errorf("远端文件缺失 %s: %v", want, err)
			continue
		}
		if len(data) == 0 {
			t.Errorf("远端文件为空: %s", want)
		}
		if !strings.Contains(steps[0].Output, want) {
			t.Errorf("上传输出应包含精确远端路径 %s: %q", want, steps[0].Output)
		}
	}
	// 输出中不应有目录本身作为文件（“conf ->”会误匹配 “a.conf ->”，用带目录分隔符的精确前缀判断）
	if strings.Contains(steps[0].Output, filepath.Join("dist", "conf")+" ->") || strings.Contains(steps[0].Output, filepath.Join("conf", "nested")+" ->") {
		t.Errorf("不应把目录当文件上传: %q", steps[0].Output)
	}
}

// ---------- pull（下载：远端 -> 本机） ----------

// 单个远端文件拉回本机：内容与权限校验
func TestPullInProc(t *testing.T) {
	remoteRoot := t.TempDir()
	addr := startInProcSSHServerOpts(t, "Test@12345", inProcOpts{SFTPDir: remoteRoot})
	cfg := inProcSSHCfg(addr)

	// 在“远端”放一个文件
	remoteFile := filepath.Join(remoteRoot, "var/log/app.log")
	os.MkdirAll(filepath.Dir(remoteFile), 0o755)
	os.WriteFile(remoteFile, []byte("remote-log-line\n"), 0o644)

	localDir := t.TempDir()
	cfg.ExecList = []ExecStep{
		{Name: "拉取日志", Type: "files", Target: "pull", Files: []StepUploadFile{
			{Local: filepath.Join(localDir, "app.log"), Remote: remoteFile, Mode: "0640"},
		}},
	}

	_, _, steps, err := trySSH(cfg, "127.0.0.1", "Test@12345", nil, false, false)
	if err != nil {
		t.Fatalf("trySSH 失败: %v", err)
	}
	if steps[0].State != "success" {
		t.Fatalf("pull 应成功: %+v", steps[0])
	}
	if !strings.Contains(steps[0].Output, "->") {
		t.Errorf("输出应含传输信息: %q", steps[0].Output)
	}
	data, err := os.ReadFile(filepath.Join(localDir, "app.log"))
	if err != nil {
		t.Fatalf("本机文件不存在: %v", err)
	}
	if string(data) != "remote-log-line\n" {
		t.Errorf("本机内容错误: %q", data)
	}
	info, _ := os.Stat(filepath.Join(localDir, "app.log"))
	if info.Mode().Perm().String() != "-rw-r-----" {
		t.Errorf("权限应为 0640: %v", info.Mode().Perm())
	}
}

// 远端文件夹递归拉取：保持目录结构
func TestPullFolderInProc(t *testing.T) {
	remoteRoot := t.TempDir()
	addr := startInProcSSHServerOpts(t, "Test@12345", inProcOpts{SFTPDir: remoteRoot})
	cfg := inProcSSHCfg(addr)

	// 在“远端”放一个文件夹
	remoteDir := filepath.Join(remoteRoot, "var/log/app")
	os.MkdirAll(filepath.Join(remoteDir, "sub"), 0o755)
	os.WriteFile(filepath.Join(remoteDir, "a.log"), []byte("aaa\n"), 0o644)
	os.WriteFile(filepath.Join(remoteDir, "sub", "b.log"), []byte("bbb\n"), 0o644)

	localDir := t.TempDir()
	cfg.ExecList = []ExecStep{
		{Name: "拉取目录", Type: "files", Target: "pull", Files: []StepUploadFile{
			{Local: localDir, Remote: remoteDir},
		}},
	}

	_, _, steps, err := trySSH(cfg, "127.0.0.1", "Test@12345", nil, false, false)
	if err != nil {
		t.Fatalf("trySSH 失败: %v", err)
	}
	if steps[0].State != "success" {
		t.Fatalf("文件夹 pull 应成功: %+v", steps[0])
	}
	for _, want := range []string{"a.log", filepath.Join("sub", "b.log")} {
		p := filepath.Join(localDir, want)
		data, err := os.ReadFile(p)
		if err != nil {
			t.Errorf("本机文件缺失 %s: %v", want, err)
			continue
		}
		if len(data) == 0 {
			t.Errorf("本机文件为空: %s", want)
		}
	}
}

// pull 覆盖语义：本机已存在默认跳过；overwrite=true 覆盖
func TestPullOverwriteInProc(t *testing.T) {
	remoteRoot := t.TempDir()
	addr := startInProcSSHServerOpts(t, "Test@12345", inProcOpts{SFTPDir: remoteRoot})
	cfg := inProcSSHCfg(addr)

	remoteFile := filepath.Join(remoteRoot, "etc/app.conf")
	os.MkdirAll(filepath.Dir(remoteFile), 0o755)
	os.WriteFile(remoteFile, []byte("REMOTE-CONTENT\n"), 0o644)

	localDir := t.TempDir()
	localFile := filepath.Join(localDir, "app.conf")
	os.WriteFile(localFile, []byte("LOCAL-OLD\n"), 0o644)

	// 1. 默认不覆盖：本机已有则跳过
	cfg.ExecList = []ExecStep{
		{Name: "拉取", Type: "files", Target: "pull", Files: []StepUploadFile{
			{Local: localFile, Remote: remoteFile},
		}},
	}
	_, _, steps, err := trySSH(cfg, "127.0.0.1", "Test@12345", nil, false, false)
	if err != nil {
		t.Fatalf("trySSH 失败: %v", err)
	}
	if steps[0].State != "success" || !strings.Contains(steps[0].Output, "跳过") {
		t.Fatalf("本机已存在且未覆盖应跳过: %+v", steps[0])
	}
	data, _ := os.ReadFile(localFile)
	if string(data) != "LOCAL-OLD\n" {
		t.Errorf("跳过时本机内容不应被改: %q", data)
	}

	// 2. overwrite=true：覆盖
	cfg.ExecList[0].Files = []StepUploadFile{{Local: localFile, Remote: remoteFile, Overwrite: boolPtr(true)}}
	_, _, steps, err = trySSH(cfg, "127.0.0.1", "Test@12345", nil, false, false)
	if err != nil {
		t.Fatalf("trySSH 失败: %v", err)
	}
	if steps[0].State != "success" || !strings.Contains(steps[0].Output, "已覆盖") {
		t.Fatalf("overwrite=true 应覆盖: %+v", steps[0])
	}
	data, _ = os.ReadFile(localFile)
	if string(data) != "REMOTE-CONTENT\n" {
		t.Errorf("覆盖后本机内容应更新: %q", data)
	}
}
