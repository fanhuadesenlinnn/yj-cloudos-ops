package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFilesDirDefault(t *testing.T) {
	s := &Settings{}
	s.applyDefaults()
	if s.FilesDir != "files" {
		t.Errorf("默认文件目录应为 files: %q", s.FilesDir)
	}
}

// 上传 -> 列表 -> 下载 -> 删除 全流程
func TestFileUploadListDownloadDelete(t *testing.T) {
	s, h, _ := newTestWebServer(t)
	cookie := doLogin(t, h)

	// 上传两个文件（multipart）
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for _, pair := range [][2]string{{"app.tar.gz", "tarball-content"}, {"deploy.sh", "#!/bin/sh\necho hi\n"}} {
		fw, _ := mw.CreateFormFile("file", pair[0])
		fw.Write([]byte(pair[1]))
	}
	mw.Close()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/files", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.AddCookie(cookie)
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("上传失败: %d %s", rec.Code, rec.Body.String())
	}
	// 文件已落盘
	filesDir := s.settings.FilesDir
	for _, n := range []string{"app.tar.gz", "deploy.sh"} {
		if _, err := os.Stat(filepath.Join(filesDir, n)); err != nil {
			t.Errorf("文件 %s 未落盘: %v", n, err)
		}
	}

	// 列表
	rec2, _ := doJSON(t, h, cookie, "GET", "/api/files", "")
	var list []map[string]any
	json.Unmarshal(rec2.Body.Bytes(), &list)
	if len(list) != 2 {
		t.Fatalf("列表应有 2 个文件: %v", list)
	}
	byName := map[string]map[string]any{}
	for _, f := range list {
		byName[fmt.Sprint(f["name"])] = f
	}
	if byName["app.tar.gz"]["ref"] != "files/app.tar.gz" {
		t.Errorf("引用路径错误: %v", byName["app.tar.gz"]["ref"])
	}
	if byName["deploy.sh"]["size"] != float64(len("#!/bin/sh\necho hi\n")) {
		t.Errorf("大小错误: %v", byName["deploy.sh"]["size"])
	}

	// 下载
	rec3 := httptest.NewRecorder()
	req3 := httptest.NewRequest("GET", "/api/files/app.tar.gz", nil)
	req3.AddCookie(cookie)
	h.ServeHTTP(rec3, req3)
	if rec3.Code != 200 || rec3.Body.String() != "tarball-content" {
		t.Errorf("下载内容错误: %d %q", rec3.Code, rec3.Body.String())
	}

	// 删除
	rec4, _ := doJSON(t, h, cookie, "DELETE", "/api/files/app.tar.gz", "")
	if rec4.Code != 200 {
		t.Fatalf("删除失败: %d", rec4.Code)
	}
	if _, err := os.Stat(filepath.Join(filesDir, "app.tar.gz")); !os.IsNotExist(err) {
		t.Error("删除后文件应不存在")
	}
	// 删除不存在的 -> 404
	rec5, _ := doJSON(t, h, cookie, "DELETE", "/api/files/app.tar.gz", "")
	if rec5.Code != http.StatusNotFound {
		t.Errorf("删除不存在文件应 404: %d", rec5.Code)
	}
}

// 路径穿越防护：文件名带路径分隔符时，最终落盘文件必须在 filesDir 内（不逃逸目录）
// （Go 标准库 multipart 解析会净化文件名 basename，代码里另有防护双保险）
func TestFilePathTraversal(t *testing.T) {
	s, h, _ := newTestWebServer(t)
	cookie := doLogin(t, h)

	// 上传文件名带路径
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "../../etc/evil.txt")
	fw.Write([]byte("x"))
	mw.Close()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/files", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.AddCookie(cookie)
	h.ServeHTTP(rec, req)
	// 文件必须落在 filesDir 内（文件名被净化为 basename）
	res, err := os.Stat(filepath.Join(s.settings.FilesDir, "evil.txt"))
	if err != nil {
		t.Fatalf("净化后的文件应落在 filesDir 内: %v", err)
	}
	if res.IsDir() {
		t.Error("落盘文件不应是目录")
	}
	// 父目录（filesDir 的上级）不应出现 evil.txt（未逃逸）
	if _, err := os.Stat(filepath.Join(filepath.Dir(s.settings.FilesDir), "evil.txt")); err == nil {
		t.Error("文件逃逸出了 filesDir")
	}

	// 删除路径穿越：ServeMux 在路由前清理 ../ 并 307 重定向（不落到文件 handler，天然防护）
	rec2, _ := doJSON(t, h, cookie, "DELETE", "/api/files/../settings.yaml", "")
	if rec2.Code != http.StatusTemporaryRedirect {
		t.Errorf("删除路径穿越应被 ServeMux 重定向拦截: %d", rec2.Code)
	}
}

// 设置接口应能修改 filesDir 并创建目录
func TestSettingsFilesDir(t *testing.T) {
	s, h, _ := newTestWebServer(t)
	cookie := doLogin(t, h)
	newDir := filepath.Join(t.TempDir(), "myfiles")

	rec, _ := doJSON(t, h, cookie, "POST", "/api/settings", fmt.Sprintf(`{"filesDir":%s}`, jsonStr(newDir)))
	if rec.Code != 200 {
		t.Fatalf("修改 filesDir 失败: %d %s", rec.Code, rec.Body.String())
	}
	if s.settings.FilesDir != newDir {
		t.Errorf("filesDir 未更新: %q", s.settings.FilesDir)
	}
	if _, err := os.Stat(newDir); err != nil {
		t.Errorf("新目录应已创建: %v", err)
	}
	// GET 返回 filesDir
	_, m := doJSON(t, h, cookie, "GET", "/api/settings", "")
	if m["filesDir"] != newDir {
		t.Errorf("GET settings 应返回 filesDir: %v", m)
	}
	if !strings.Contains(fmt.Sprint(m["filesDir"]), "myfiles") {
		t.Errorf("filesDir 值错误: %v", m)
	}
}
