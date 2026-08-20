package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 验证自动命名 + Excel 导出完整链路（profile + output.dir -> <配置名>_<时间戳>.xlsx）
func TestExportExcelAutoName(t *testing.T) {
	dir := t.TempDir()
	vms := []*VM{
		{Name: "web-01", Type: "虚拟机", IP: "10.0.0.1", ProjectName: "test", SSHResult: "✓ 成功"},
		{Name: "db-01", Type: "虚拟机", IP: "10.0.0.2", ProjectName: "test", SSHResult: "✗ 认证失败"},
	}
	cfg := &Config{}
	cfg.Output.Dir = filepath.Join(dir, "results")

	path := autoExcelPath("生产环境", cfg)
	if path == "" {
		t.Fatal("应生成导出路径")
	}
	if err := exportExcel(path, vms); err != nil {
		t.Fatalf("exportExcel 失败: %v", err)
	}
	// 文件存在且大小>0
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("导出文件不存在: %v", err)
	}
	if info.Size() == 0 {
		t.Error("导出文件为空")
	}
	// 文件名符合 <配置名>_<时间戳>.xlsx
	base := filepath.Base(path)
	if !strings.HasPrefix(base, "生产环境_") || !strings.HasSuffix(base, ".xlsx") {
		t.Errorf("文件名格式错误: %s", base)
	}
}
