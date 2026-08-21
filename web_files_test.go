package main

import (
	"archive/zip"
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
	var body struct {
		Dir   string           `json:"dir"`
		Items []map[string]any `json:"items"`
	}
	json.Unmarshal(rec2.Body.Bytes(), &body)
	if body.Dir != "" {
		t.Errorf("根目录 dir 应为空: %q", body.Dir)
	}
	if len(body.Items) != 2 {
		t.Fatalf("列表应有 2 个文件: %v", body.Items)
	}
	byName := map[string]map[string]any{}
	for _, f := range body.Items {
		byName[fmt.Sprint(f["name"])] = f
	}
	if byName["app.tar.gz"]["ref"] != "files/app.tar.gz" {
		t.Errorf("引用路径错误: %v", byName["app.tar.gz"]["ref"])
	}
	if byName["deploy.sh"]["size"] != float64(len("#!/bin/sh\necho hi\n")) {
		t.Errorf("大小错误: %v", byName["deploy.sh"]["size"])
	}
	if byName["app.tar.gz"]["isDir"] != false {
		t.Errorf("文件 isDir 应为 false: %v", byName["app.tar.gz"]["isDir"])
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

// 文件夹：子目录浏览 / 上传到子目录 / 文件夹打包下载(zip) / 递归删除
func TestFileFolderBrowseUploadZipDelete(t *testing.T) {
	s, h, _ := newTestWebServer(t)
	cookie := doLogin(t, h)
	filesDir := s.settings.FilesDir

	// 上传文件夹：每个文件对应一个 path 字段（webkitRelativePath，含所选文件夹名），
	// 按顺序一一对应，后端保持目录结构
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for _, pair := range [][2]string{{"清单.xlsx", "xlsx-content"}, {"主机_1.2.3.4_步骤.log", "log-content"}} {
		fw, _ := mw.CreateFormFile("file", pair[0])
		fw.Write([]byte(pair[1]))
	}
	pathVals := []string{"out/results/清单.xlsx", "out/scriptdir/主机_1.2.3.4_步骤.log"}
	for _, p := range pathVals {
		mw.WriteField("path", p)
	}
	mw.Close()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/files", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.AddCookie(cookie)
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("上传文件夹失败: %d %s", rec.Code, rec.Body.String())
	}
	for _, p := range []string{"out/results/清单.xlsx", "out/scriptdir/主机_1.2.3.4_步骤.log"} {
		if _, err := os.Stat(filepath.Join(filesDir, filepath.FromSlash(p))); err != nil {
			t.Errorf("子目录文件 %s 未落盘: %v", p, err)
		}
	}

	// 浏览根目录：应显示 out 文件夹（isDir=true）
	rec2, _ := doJSON(t, h, cookie, "GET", "/api/files", "")
	var root struct {
		Items []map[string]any `json:"items"`
	}
	json.Unmarshal(rec2.Body.Bytes(), &root)
	if len(root.Items) != 1 || root.Items[0]["name"] != "out" || root.Items[0]["isDir"] != true {
		t.Fatalf("根目录应显示 out 文件夹: %v", root.Items)
	}
	if root.Items[0]["ref"] != "files/out/" {
		t.Errorf("文件夹引用路径应以 / 结尾: %v", root.Items[0]["ref"])
	}

	// 进入子目录浏览
	rec3, _ := doJSON(t, h, cookie, "GET", "/api/files?path=out", "")
	var sub struct {
		Dir   string           `json:"dir"`
		Items []map[string]any `json:"items"`
	}
	json.Unmarshal(rec3.Body.Bytes(), &sub)
	if sub.Dir != "out" || len(sub.Items) != 2 {
		t.Fatalf("out 下应有 2 个文件夹: %v", sub.Items)
	}
	for _, it := range sub.Items {
		if it["isDir"] != true {
			t.Errorf("out 下应为文件夹: %v", it)
		}
	}

	// 浏览不存在的目录 -> 404
	rec4, _ := doJSON(t, h, cookie, "GET", "/api/files?path=nope", "")
	if rec4.Code != http.StatusNotFound {
		t.Errorf("浏览不存在目录应 404: %d", rec4.Code)
	}

	// 文件夹打包下载：zip 内容含 out/results/清单.xlsx 与 out/scriptdir/主机_1.2.3.4_步骤.log
	rec5 := httptest.NewRecorder()
	req5 := httptest.NewRequest("GET", "/api/files/out", nil)
	req5.AddCookie(cookie)
	h.ServeHTTP(rec5, req5)
	if rec5.Code != 200 {
		t.Fatalf("文件夹下载失败: %d", rec5.Code)
	}
	if ct := rec5.Header().Get("Content-Type"); ct != "application/zip" {
		t.Errorf("文件夹下载应为 zip: %q", ct)
	}
	zr, err := zip.NewReader(bytes.NewReader(rec5.Body.Bytes()), int64(rec5.Body.Len()))
	if err != nil {
		t.Fatalf("解析 zip 失败: %v", err)
	}
	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
	}
	if !names["out/results/清单.xlsx"] || !names["out/scriptdir/主机_1.2.3.4_步骤.log"] {
		t.Errorf("zip 条目应保持目录结构: %v", names)
	}

	// 删除文件夹（递归）
	rec6, _ := doJSON(t, h, cookie, "DELETE", "/api/files/out", "")
	if rec6.Code != 200 {
		t.Fatalf("删除文件夹失败: %d", rec6.Code)
	}
	if _, err := os.Stat(filepath.Join(filesDir, "out")); !os.IsNotExist(err) {
		t.Error("删除后文件夹应不存在（递归删除）")
	}
}

// 路径穿越：上传时 path 字段带 .. 应被拒绝（不落盘、不逃逸目录）
// （文件名本身会被 Go 标准库净化成 basename，目录结构靠 path 字段传递，故对 path 严格校验）
func TestFileUploadTraversalRejected(t *testing.T) {
	s, h, _ := newTestWebServer(t)
	cookie := doLogin(t, h)

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "evil.txt")
	fw.Write([]byte("x"))
	mw.WriteField("path", "../../evil.txt") // 前端传的相对路径带穿越
	mw.Close()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/files", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.AddCookie(cookie)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("路径穿越上传应 400: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(s.settings.FilesDir), "evil.txt")); err == nil {
		t.Error("文件逃逸出了 filesDir")
	}
	if _, err := os.Stat(filepath.Join(s.settings.FilesDir, "evil.txt")); err == nil {
		t.Error("穿越请求的文件不应落盘")
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
