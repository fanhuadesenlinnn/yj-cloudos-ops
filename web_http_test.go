package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newTestWebServer 构造测试用 Web 服务（临时设置文件与配置目录）
func newTestWebServer(t *testing.T) (*webServer, http.Handler, string) {
	t.Helper()
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.yaml")
	cfgDir := filepath.Join(dir, "configs")
	os.MkdirAll(cfgDir, 0o755)
	st, err := loadSettings(settingsPath)
	if err != nil {
		t.Fatalf("loadSettings 失败: %v", err)
	}
	st.ConfigsDir = cfgDir
	s := &webServer{
		settings:     st,
		settingsPath: settingsPath,
		sessions:     map[string]time.Time{},
		hub:          newEventHub(),
	}
	return s, s.routes(), cfgDir
}

// login 登录并返回带 cookie 的请求工厂
func doLogin(t *testing.T, h http.Handler) *http.Cookie {
	t.Helper()
	body := bytes.NewBufferString(`{"username":"admin","password":"admin"}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/login", body)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("登录失败: %d %s", rec.Code, rec.Body.String())
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == "session" {
			return c
		}
	}
	t.Fatal("未返回 session cookie")
	return nil
}

func doJSON(t *testing.T, h http.Handler, cookie *http.Cookie, method, path, payload string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var body io.Reader
	if payload != "" {
		body = bytes.NewBufferString(payload)
	}
	req := httptest.NewRequest(method, path, body)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var m map[string]any
	if rec.Body.Len() > 0 {
		json.Unmarshal(rec.Body.Bytes(), &m)
	}
	return rec, m
}

func TestWebAuthFlow(t *testing.T) {
	_, h, _ := newTestWebServer(t)

	// 未登录访问 API -> 401
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/configs", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("未登录应 401: %d", rec.Code)
	}

	// 错误密码 -> 401
	rec, _ = doJSON(t, h, nil, "POST", "/api/login", `{"username":"admin","password":"wrong"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("错误密码应 401: %d", rec.Code)
	}

	// 正确登录
	cookie := doLogin(t, h)
	rec, _ = doJSON(t, h, cookie, "GET", "/api/configs", "")
	if rec.Code != http.StatusOK {
		t.Errorf("登录后应 200: %d", rec.Code)
	}

	// 静态页可访问（无 cookie）
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("GET", "/", nil)
	h.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK || !strings.Contains(rec2.Body.String(), "yj-cloudos-ops") {
		t.Error("静态页应返回页面内容")
	}
}

func TestWebConfigCRUD(t *testing.T) {
	_, h, cfgDir := newTestWebServer(t)
	cookie := doLogin(t, h)

	// 初始为空
	rec, m := doJSON(t, h, cookie, "GET", "/api/configs", "")
	if rec.Code != 200 || len(m) != 0 {
		t.Errorf("初始配置列表应为空: %d %v", rec.Code, m)
	}

	// 创建配置（含描述）
	yml := "endpoint: https://127.0.0.1:30990\naccessKeyId: ak\naccessKeySecret: sk\nregionId: cn-beijing\nproject:\n  names: [\"test\"]\n"
	rec, m = doJSON(t, h, cookie, "POST", "/api/configs",
		fmt.Sprintf(`{"name":"生产环境","desc":"生产巡检","yaml":%s}`, jsonStr(yml)))
	if rec.Code != 200 {
		t.Fatalf("创建配置失败: %d %s", rec.Code, rec.Body.String())
	}
	// 文件已落盘且含描述注释
	data, _ := os.ReadFile(filepath.Join(cfgDir, "生产环境.yaml"))
	if !strings.HasPrefix(string(data), "# 描述: 生产巡检") {
		t.Errorf("文件头应有描述注释: %q", string(data[:30]))
	}

	// 列表
	rec, _ = doJSON(t, h, cookie, "GET", "/api/configs", "")
	var list []map[string]any
	json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list) != 1 || list[0]["name"] != "生产环境" || list[0]["desc"] != "生产巡检" {
		t.Errorf("列表错误: %v", list)
	}

	// 查看详情（yaml 不含描述行）
	rec, m = doJSON(t, h, cookie, "GET", "/api/configs/生产环境", "")
	if rec.Code != 200 || strings.Contains(fmt.Sprint(m["yaml"]), "描述") {
		t.Errorf("详情 yaml 不应含描述行: %v", m)
	}

	// 非法 YAML -> 400
	rec, _ = doJSON(t, h, cookie, "POST", "/api/configs", `{"name":"坏配置","yaml":"a: [b"}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("非法 YAML 应 400: %d", rec.Code)
	}

	// 非法配置名 -> 400
	rec, _ = doJSON(t, h, cookie, "POST", "/api/configs", `{"name":"../evil","yaml":"endpoint: x"}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("非法配置名应 400: %d", rec.Code)
	}

	// 复制
	rec, _ = doJSON(t, h, cookie, "POST", "/api/configs/生产环境", `{"newName":"生产环境-副本"}`)
	if rec.Code != 200 {
		t.Errorf("复制失败: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(cfgDir, "生产环境-副本.yaml")); err != nil {
		t.Error("副本文件应存在")
	}

	// 删除
	rec, _ = doJSON(t, h, cookie, "DELETE", "/api/configs/生产环境-副本", "")
	if rec.Code != 200 {
		t.Errorf("删除失败: %d", rec.Code)
	}
	if _, err := os.Stat(filepath.Join(cfgDir, "生产环境-副本.yaml")); !os.IsNotExist(err) {
		t.Error("副本文件应已删除")
	}
}

func TestWebRunConflict(t *testing.T) {
	_, h, _ := newTestWebServer(t)
	cookie := doLogin(t, h)

	// 运行不存在的配置 -> 404
	rec, _ := doJSON(t, h, cookie, "POST", "/api/run", `{"profile":"不存在"}`)
	if rec.Code != http.StatusNotFound {
		t.Errorf("不存在配置应 404: %d", rec.Code)
	}

	// 配置存在但平台不可达：应失败（job 状态 failed）而非 panic
	yml := "endpoint: https://127.0.0.1:1\naccessKeyId: ak\naccessKeySecret: sk\nregionId: cn-beijing\nproject:\n  names: [\"x\"]\n"
	doJSON(t, h, cookie, "POST", "/api/configs", fmt.Sprintf(`{"name":"离线","yaml":%s}`, jsonStr(yml)))
	rec, m := doJSON(t, h, cookie, "POST", "/api/run", `{"profile":"离线"}`)
	if rec.Code != 200 {
		t.Fatalf("启动运行应 200: %d %s", rec.Code, rec.Body.String())
	}
	jobID := fmt.Sprint(m["id"])
	// 等待完成（平台不可达会快速失败）
	deadline := time.Now().Add(10 * time.Second)
	var job map[string]any
	var summary map[string]any
	for time.Now().Before(deadline) {
		rec, job = doJSON(t, h, cookie, "GET", "/api/result?job="+jobID, "")
		if rec.Code == 200 {
			summary, _ = job["summary"].(map[string]any)
			if summary != nil && summary["status"] != "running" {
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if summary == nil || summary["status"] == "running" {
		t.Fatal("任务应已结束")
	}
	if summary["status"] != "failed" {
		t.Errorf("平台不可达任务应失败: %v", summary)
	}
	if summary["error"] == nil || fmt.Sprint(summary["error"]) == "" {
		t.Error("失败任务应带错误信息")
	}
	// 历史里有记录
	rec, _ = doJSON(t, h, cookie, "GET", "/api/history", "")
	var hist []map[string]any
	json.Unmarshal(rec.Body.Bytes(), &hist)
	if len(hist) != 1 {
		t.Errorf("历史应有 1 条: %v", hist)
	}
}

func TestWebSettingsUpdate(t *testing.T) {
	s, h, _ := newTestWebServer(t)
	cookie := doLogin(t, h)

	// 修改密码
	rec, _ := doJSON(t, h, cookie, "POST", "/api/settings", `{"password":"newpass123"}`)
	if rec.Code != 200 {
		t.Fatalf("修改密码失败: %d %s", rec.Code, rec.Body.String())
	}
	if !s.settings.checkPassword("admin", "newpass123") {
		t.Error("新密码应生效")
	}
	if s.settings.checkPassword("admin", "admin") {
		t.Error("旧密码应失效")
	}
	// 短密码 -> 400
	rec, _ = doJSON(t, h, cookie, "POST", "/api/settings", `{"password":"ab"}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("短密码应 400: %d", rec.Code)
	}
	// GET 不应暴露哈希
	rec, m := doJSON(t, h, cookie, "GET", "/api/settings", "")
	if rec.Code != 200 {
		t.Fatalf("GET settings 失败: %d", rec.Code)
	}
	for k := range m {
		if strings.Contains(k, "hash") || strings.Contains(k, "salt") {
			t.Errorf("设置接口不应暴露哈希/盐: %v", m)
		}
	}
}

func jsonStr(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
