package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// -init 生成的配置与 config.example.yaml 内容一致（go:embed 单点维护）
func TestExampleConfigEmbedded(t *testing.T) {
	if !strings.Contains(exampleConfigYAML, "execList:") {
		t.Error("示例配置应包含 execList 流水线部分")
	}
	if !strings.Contains(exampleConfigYAML, "endpoint:") {
		t.Error("示例配置应包含 endpoint")
	}
	// 完整示例应覆盖各模块用法（files/command/services/status 都在注释里）
	for _, mod := range []string{"files", "push", "pull", "command", "services", "status", "filter", "output"} {
		if !strings.Contains(exampleConfigYAML, mod) {
			t.Errorf("示例配置缺少模块 %q 说明", mod)
		}
	}
}

func TestInitExampleConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "demo.yaml") // 子目录自动创建

	if err := initExampleConfig(path); err != nil {
		t.Fatalf("initExampleConfig 失败: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("生成文件不存在: %v", err)
	}
	if string(data) != exampleConfigYAML {
		t.Error("生成内容应与 embed 的示例一致")
	}

	// 已存在时不覆盖
	if err := initExampleConfig(path); err == nil {
		t.Fatal("文件已存在应报错")
	}
	if data2, _ := os.ReadFile(path); string(data2) != exampleConfigYAML {
		t.Error("已存在文件不应被修改")
	}
}

// 前端帮助页应包含各模块说明与 demo（防手滑删改）
func TestWebHelpAndDemoPresent(t *testing.T) {
	html := string(webHTML)
	// 帮助页签与渲染
	for _, s := range []string{"帮助", "renderHelp", "HELP_MD", "### files 模块", "### command 模块", "注意事项"} {
		if !strings.Contains(html, s) {
			t.Errorf("前端帮助缺少 %q", s)
		}
	}
	// 精简版 demo 应包含核心字段
	for _, s := range []string{"DEMO_YAML", "endpoint:", "accessKeyId:", "execList:", "type: files", "type: command", "type: services", "type: status", "output:", "dir: \"results\""} {
		if !strings.Contains(html, s) {
			t.Errorf("前端 demo 缺少 %q", s)
		}
	}
	// 新建配置应自动填充 demo（newConfig 里赋值给 cfg-yaml）
	if !strings.Contains(html, "$('cfg-yaml').value = DEMO_YAML") {
		t.Error("新建配置应引用 DEMO_YAML")
	}
}
