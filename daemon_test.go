package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPIDFileRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "web.pid")
	token := newShutdownToken()
	if err := writePIDFile(path, 12345, token, "0.0.0.0:8080", "web.log"); err != nil {
		t.Fatalf("writePIDFile 失败: %v", err)
	}
	info, err := readPIDFile(path)
	if err != nil {
		t.Fatalf("readPIDFile 失败: %v", err)
	}
	if info.PID != 12345 || info.Token != token || info.Addr != "0.0.0.0:8080" {
		t.Errorf("PID 文件内容错误: %+v", info)
	}
	removePIDFile(path)
	if _, err := readPIDFile(path); !os.IsNotExist(err) {
		t.Errorf("删除后应不存在: %v", err)
	}
}

func TestShutdownTokenUnique(t *testing.T) {
	a, b := newShutdownToken(), newShutdownToken()
	if a == b {
		t.Error("两次生成的 token 不应相同")
	}
	if len(a) != 32 {
		t.Errorf("token 长度应为 32: %q", a)
	}
}

func TestParseAddrPort(t *testing.T) {
	if parseAddrPort("0.0.0.0:8080") != "8080" {
		t.Error("端口解析错误")
	}
	if parseAddrPort("bad") != "" {
		t.Error("无端口地址应返回空")
	}
}

func TestLogTail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "web.log")
	lines := []string{}
	for i := 0; i < 30; i++ {
		lines = append(lines, fmt.Sprintf("line-%02d", i))
	}
	os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644)

	tail, err := logTail(path, 5)
	if err != nil {
		t.Fatalf("logTail 失败: %v", err)
	}
	// 应返回最后5行（line-25..line-29），不含更早的
	if !strings.Contains(tail, "line-29") || strings.Contains(tail, "line-24") || strings.Contains(tail, "line-23") {
		t.Errorf("logTail 应返回最后5行: %q", tail)
	}
	// n 大于总行数时返回全部
	tail2, _ := logTail(path, 100)
	if !strings.Contains(tail2, "line-00") || !strings.Contains(tail2, "line-29") {
		t.Errorf("logTail 大 n 应返回全部: %q", tail2)
	}
}

// shutdown API：前台模式（无 token）已登录 session 可关；非本机拒绝
func TestShutdownHandlerLocalOnly(t *testing.T) {
	s := &webServer{sessions: map[string]time.Time{}, hub: newEventHub()}
	shutdownToken = "" // 前台模式

	// 未登录 + 无 token 的本机请求 -> 403
	rec0 := httptest.NewRecorder()
	req0 := httptest.NewRequest("POST", "/api/shutdown", strings.NewReader(`{}`))
	req0.RemoteAddr = "127.0.0.1:5555"
	s.handleShutdown(rec0, req0)
	if rec0.Code != http.StatusForbidden {
		t.Errorf("未登录且无 token 应 403: %d", rec0.Code)
	}

	// 已登录 session 的本机请求 -> 200
	sid := newSessionID()
	s.sessions[sid] = time.Now().Add(time.Hour)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/shutdown", strings.NewReader(`{}`))
	req.AddCookie(&http.Cookie{Name: "session", Value: sid})
	req.RemoteAddr = "127.0.0.1:5555"
	s.handleShutdown(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("已登录 session 的 shutdown 应 200: %d %s", rec.Code, rec.Body.String())
	}
	// 触发了一次关闭（shutdownCh 异步收到信号，稍等）
	deadline := time.Now().Add(2 * time.Second)
	got := false
	for time.Now().Before(deadline) {
		select {
		case <-shutdownCh:
			got = true
		default:
			time.Sleep(20 * time.Millisecond)
		}
		if got {
			break
		}
	}
	if !got {
		t.Error("shutdownCh 应收到信号")
	}

	// 非本机（即使带 session）-> 403
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("POST", "/api/shutdown", strings.NewReader(`{}`))
	req2.AddCookie(&http.Cookie{Name: "session", Value: sid})
	req2.RemoteAddr = "192.168.1.5:5555"
	s.handleShutdown(rec2, req2)
	if rec2.Code != http.StatusForbidden {
		t.Errorf("非本机 shutdown 应 403: %d", rec2.Code)
	}
}

// 后台模式 token 校验
func TestShutdownHandlerToken(t *testing.T) {
	s := &webServer{sessions: map[string]time.Time{}, hub: newEventHub()}
	shutdownToken = "secret-token"

	// 错误 token -> 403
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/shutdown", strings.NewReader(`{"token":"wrong"}`))
	req.RemoteAddr = "127.0.0.1:5555"
	s.handleShutdown(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("错误 token 应 403: %d", rec.Code)
	}
	// 正确 token -> 200
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("POST", "/api/shutdown", strings.NewReader(`{"token":"secret-token"}`))
	req2.RemoteAddr = "127.0.0.1:5555"
	s.handleShutdown(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Errorf("正确 token 应 200: %d %s", rec2.Code, rec2.Body.String())
	}
	shutdownToken = ""
}

// log API：设置页查看尾部 + 下载
func TestLogAPI(t *testing.T) {
	dir := t.TempDir()
	// 直接构造 webServer 指向临时日志
	s := &webServer{
		sessions:   map[string]time.Time{},
		hub:        newEventHub(),
		pidLogFile: filepath.Join(dir, "web.log"),
	}
	os.WriteFile(s.pidLogFile, []byte("line1\nline2\nline3\n"), 0o644)
	h := s.routes()

	// 未登录 -> 401
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/log", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("未登录 log 应 401: %d", rec.Code)
	}
}

// 配置里 httpShutdownRequest 构造（本机+token）
func TestHTTPShutdownRequest(t *testing.T) {
	// 用一个本地 httptest server 模拟 shutdown 端点
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		if body["token"] != "tok" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()
	addr := strings.TrimPrefix(ts.URL, "http://")

	if err := httpShutdownRequest(addr, "tok"); err != nil {
		t.Errorf("正确 token 应成功: %v", err)
	}
	if err := httpShutdownRequest(addr, "bad"); err == nil {
		t.Error("错误 token 应失败")
	}
}
