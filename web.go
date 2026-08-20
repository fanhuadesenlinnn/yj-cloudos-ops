package main

import (
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

//go:embed web/index.html
var webHTML []byte

// ---------- 事件推送（SSE） ----------

type sseEvent struct {
	Type string
	Data []byte
}

// eventHub job 事件广播：订阅者按 jobID 分组，发布时非阻塞投递（满则丢弃，客户端会重连拿最新状态）
type eventHub struct {
	mu   sync.Mutex
	subs map[string]map[chan sseEvent]bool
}

func newEventHub() *eventHub {
	return &eventHub{subs: map[string]map[chan sseEvent]bool{}}
}

func (h *eventHub) subscribe(jobID string) chan sseEvent {
	ch := make(chan sseEvent, 32)
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.subs[jobID] == nil {
		h.subs[jobID] = map[chan sseEvent]bool{}
	}
	h.subs[jobID][ch] = true
	return ch
}

func (h *eventHub) unsubscribe(jobID string, ch chan sseEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if m, ok := h.subs[jobID]; ok {
		delete(m, ch)
		if len(m) == 0 {
			delete(h.subs, jobID)
		}
	}
}

func (h *eventHub) publish(jobID, typ string, data any) {
	raw, err := json.Marshal(data)
	if err != nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs[jobID] {
		select {
		case ch <- sseEvent{Type: typ, Data: raw}:
		default: // 订阅者积压则丢弃，客户端重连后从 job 状态恢复
		}
	}
}

// ---------- 运行任务 ----------

// Job 一次运行的完整状态（结果保留在内存，重启即清空）
type Job struct {
	ID         string    `json:"id"`
	Profile    string    `json:"profile"`
	Status     string    `json:"status"` // running / done / failed
	Error      string    `json:"error"`
	StartedAt  time.Time `json:"startedAt"`
	FinishedAt time.Time `json:"finishedAt"`
	Done       int       `json:"done"`
	Total      int       `json:"total"`
	Progress   string    `json:"progress"`
	ExcelFile  string    `json:"excelFile"`
	VMs        []*VM     `json:"-"`
}

func jobSummary(j *Job) map[string]any {
	if j == nil {
		return nil
	}
	excel := ""
	if j.ExcelFile != "" {
		excel = filepath.Base(j.ExcelFile)
	}
	return map[string]any{
		"id": j.ID, "profile": j.Profile, "status": j.Status, "error": j.Error,
		"startedAt": j.StartedAt, "finishedAt": j.FinishedAt,
		"done": j.Done, "total": j.Total, "progress": j.Progress,
		"excelFile": excel,
	}
}

// ---------- Web 服务 ----------

type webServer struct {
	mu        sync.Mutex
	settings  *Settings
	settingsPath string
	sessions  map[string]time.Time // sessionID -> 过期时间
	currentJob *Job
	history   []*Job // 最近 N 次，重启清空
	hub       *eventHub
}

// sessionTTL 会话有效期
const sessionTTL = 12 * time.Hour

func runWeb(addr, configsDir, settingsPath string) {
	// 设置（不存在则创建默认 admin/admin）；命令行 -web-configs 优先于 settings 里的配置目录
	st, err := loadSettings(settingsPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "加载设置失败: %v\n", err)
		os.Exit(1)
	}
	if configsDir != "" {
		st.ConfigsDir = configsDir
		if err := saveSettings(settingsPath, st); err != nil {
			fmt.Fprintf(os.Stderr, "保存设置失败: %v\n", err)
			os.Exit(1)
		}
	}
	// 配置目录不存在则创建
	if err := os.MkdirAll(st.ConfigsDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "创建配置目录 %s 失败: %v\n", st.ConfigsDir, err)
		os.Exit(1)
	}
	// Web 模式：同名多项目不再 stdin 交互，按预选 ID 匹配或报错提示页面选择
	projectChooser = func(matches []*Project) (*Project, error) {
		if webProjectID != "" {
			for _, p := range matches {
				if p.ID == webProjectID {
					return p, nil
				}
			}
			return nil, fmt.Errorf("预选项目ID %s 不在同名候选中", webProjectID)
		}
		return nil, fmt.Errorf("发现 %d 个同名项目，请在页面上选择", len(matches))
	}

	s := &webServer{
		settings:     st,
		settingsPath: settingsPath,
		sessions:     map[string]time.Time{},
		hub:          newEventHub(),
	}

	handler := s.routes()

	fmt.Fprintf(os.Stderr, "yj-cloudos-ops Web 模式启动\n")
	fmt.Fprintf(os.Stderr, "  监听地址: http://%s\n", addr)
	fmt.Fprintf(os.Stderr, "  设置文件: %s\n", settingsPath)
	fmt.Fprintf(os.Stderr, "  配置目录: %s\n", st.ConfigsDir)
	fmt.Fprintf(os.Stderr, "  登录账号: %s / admin（默认，请尽快修改）\n", st.Auth.Username)
	if err := http.ListenAndServe(addr, handler); err != nil {
		fmt.Fprintf(os.Stderr, "HTTP 服务启动失败: %v\n", err)
		os.Exit(1)
	}
}

// routes 注册全部路由（拆出便于测试）
func (s *webServer) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleStatic)
	mux.HandleFunc("/api/login", s.handleLogin)
	mux.HandleFunc("/api/logout", s.handleLogout)
	mux.HandleFunc("/api/settings", s.auth(s.handleSettings))
	mux.HandleFunc("/api/configs", s.auth(s.handleConfigs))
	mux.HandleFunc("/api/configs/", s.auth(s.handleConfigItem))
	mux.HandleFunc("/api/projects", s.auth(s.handleProjects))
	mux.HandleFunc("/api/run", s.auth(s.handleRun))
	mux.HandleFunc("/api/events", s.auth(s.handleEvents))
	mux.HandleFunc("/api/history", s.auth(s.handleHistory))
	mux.HandleFunc("/api/result", s.auth(s.handleResult))
	mux.HandleFunc("/api/export", s.auth(s.handleExport))
	return mux
}

// ---------- 静态页 + 鉴权 ----------

func (s *webServer) handleStatic(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(webHTML)
}

// auth 鉴权中间件：除 /api/login 外均需有效 session
func (s *webServer) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie("session")
		if err != nil {
			http.Error(w, "未登录", http.StatusUnauthorized)
			return
		}
		s.mu.Lock()
		exp, ok := s.sessions[c.Value]
		if ok && time.Now().After(exp) {
			delete(s.sessions, c.Value)
			ok = false
		}
		s.mu.Unlock()
		if !ok {
			http.Error(w, "会话已过期，请重新登录", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func (s *webServer) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "仅支持 POST", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "请求格式错误", http.StatusBadRequest)
		return
	}
	if !s.settings.checkPassword(req.Username, req.Password) {
		http.Error(w, "账号或密码错误", http.StatusUnauthorized)
		return
	}
	id := newSessionID()
	s.mu.Lock()
	s.sessions[id] = time.Now().Add(sessionTTL)
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "session", Value: id, Path: "/", HttpOnly: true, MaxAge: int(sessionTTL.Seconds())})
	jsonResponse(w, map[string]any{"ok": true})
}

func (s *webServer) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie("session"); err == nil {
		s.mu.Lock()
		delete(s.sessions, c.Value)
		s.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: "session", Value: "", Path: "/", MaxAge: -1})
	jsonResponse(w, map[string]any{"ok": true})
}

// ---------- 设置 API ----------

func (s *webServer) handleSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		jsonResponse(w, map[string]any{
			"username":    s.settings.Auth.Username,
			"configsDir":  s.settings.ConfigsDir,
			"historySize": s.settings.HistorySize,
			"defaultPassword": s.settings.Auth.Username == "admin",
		})
	case http.MethodPost:
		var req struct {
			Username    string `json:"username"`
			Password    string `json:"password"`
			ConfigsDir  string `json:"configsDir"`
			HistorySize int    `json:"historySize"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "请求格式错误", http.StatusBadRequest)
			return
		}
		if req.Username != "" {
			s.settings.Auth.Username = req.Username
		}
		if req.Password != "" {
			if len(req.Password) < 4 {
				http.Error(w, "密码至少 4 位", http.StatusBadRequest)
				return
			}
			s.settings.setPassword(s.settings.Auth.Username, req.Password)
		}
		if req.ConfigsDir != "" {
			if err := os.MkdirAll(req.ConfigsDir, 0o755); err != nil {
				http.Error(w, "配置目录创建失败: "+err.Error(), http.StatusBadRequest)
				return
			}
			s.settings.ConfigsDir = req.ConfigsDir
		}
		if req.HistorySize > 0 {
			s.settings.HistorySize = req.HistorySize
		}
		if err := saveSettings(s.settingsPath, s.settings); err != nil {
			http.Error(w, "保存设置失败: "+err.Error(), http.StatusInternalServerError)
			return
		}
		jsonResponse(w, map[string]any{"ok": true})
	default:
		http.Error(w, "不支持的方法", http.StatusMethodNotAllowed)
	}
}

// ---------- 配置管理 API ----------

var validConfigName = regexp.MustCompile(`^[\p{Han}\w\-]+$`)

func (s *webServer) configPath(name string) string {
	return filepath.Join(s.settings.ConfigsDir, name+".yaml")
}

// splitDesc 从 YAML 文本头部提取 "# 描述: xxx" 注释，返回描述与去掉该行的 YAML 内容
func splitDesc(yamlText string) (desc, rest string) {
	lines := strings.Split(yamlText, "\n")
	var keep []string
	for i, ln := range lines {
		if i == 0 && strings.HasPrefix(ln, "# 描述:") {
			desc = strings.TrimSpace(strings.TrimPrefix(ln, "# 描述:"))
			continue
		}
		keep = append(keep, ln)
	}
	return desc, strings.TrimLeft(strings.Join(keep, "\n"), "\n")
}

// handleConfigs 列表 + 新建/保存
func (s *webServer) handleConfigs(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		metas, err := listProfiles(s.settings.ConfigsDir)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonResponse(w, metas)
	case http.MethodPost:
		var req struct {
			Name string `json:"name"`
			Desc string `json:"desc"`
			YAML string `json:"yaml"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "请求格式错误", http.StatusBadRequest)
			return
		}
		req.Name = strings.TrimSpace(req.Name)
		if !validConfigName.MatchString(req.Name) {
			http.Error(w, "配置名只能包含中文/字母/数字/下划线/中划线", http.StatusBadRequest)
			return
		}
		// 从 YAML 内容提取描述（内容里可能已有 # 描述: 行），req.Desc 优先
		desc, rest := splitDesc(req.YAML)
		if req.Desc != "" {
			desc = req.Desc
		}
		if strings.TrimSpace(rest) == "" {
			http.Error(w, "YAML 内容为空", http.StatusBadRequest)
			return
		}
		// 语法校验（必填项留到运行时 loadConfig 校验）
		var probe map[string]any
		if err := yaml.Unmarshal([]byte(rest), &probe); err != nil {
			http.Error(w, "YAML 语法错误: "+err.Error(), http.StatusBadRequest)
			return
		}
		content := rest
		if desc != "" {
			content = "# 描述: " + desc + "\n" + rest
		}
		if err := os.WriteFile(s.configPath(req.Name), []byte(content), 0o644); err != nil {
			http.Error(w, "保存配置失败: "+err.Error(), http.StatusInternalServerError)
			return
		}
		jsonResponse(w, map[string]any{"ok": true, "name": req.Name, "desc": desc})
	default:
		http.Error(w, "不支持的方法", http.StatusMethodNotAllowed)
	}
}

// handleConfigItem 单配置：查看 / 复制 / 删除
func (s *webServer) handleConfigItem(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/api/configs/")
	name = strings.TrimSuffix(name, "/")
	if !validConfigName.MatchString(name) {
		http.Error(w, "配置名非法", http.StatusBadRequest)
		return
	}
	path := s.configPath(name)
	if !fileExists(path) {
		http.Error(w, "配置不存在", http.StatusNotFound)
		return
	}
	switch r.Method {
	case http.MethodGet:
		data, err := os.ReadFile(path)
		if err != nil {
			http.Error(w, "读取配置失败: "+err.Error(), http.StatusInternalServerError)
			return
		}
		desc, yamlText := splitDesc(string(data))
		jsonResponse(w, map[string]any{"name": name, "desc": desc, "yaml": yamlText})
	case http.MethodPost:
		// 复制：body {newName}
		var req struct {
			NewName string `json:"newName"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "请求格式错误", http.StatusBadRequest)
			return
		}
		if !validConfigName.MatchString(req.NewName) {
			http.Error(w, "新配置名非法", http.StatusBadRequest)
			return
		}
		if fileExists(s.configPath(req.NewName)) {
			http.Error(w, "配置已存在: "+req.NewName, http.StatusConflict)
			return
		}
		data, err := os.ReadFile(path)
		if err != nil {
			http.Error(w, "读取配置失败: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if err := os.WriteFile(s.configPath(req.NewName), data, 0o644); err != nil {
			http.Error(w, "复制失败: "+err.Error(), http.StatusInternalServerError)
			return
		}
		jsonResponse(w, map[string]any{"ok": true, "name": req.NewName})
	case http.MethodDelete:
		if err := os.Remove(path); err != nil {
			http.Error(w, "删除失败: "+err.Error(), http.StatusInternalServerError)
			return
		}
		jsonResponse(w, map[string]any{"ok": true})
	default:
		http.Error(w, "不支持的方法", http.StatusMethodNotAllowed)
	}
}

// profileMeta 配置列表项
type profileMeta struct {
	Name     string    `json:"name"`
	Desc     string    `json:"desc"`
	ModTime  time.Time `json:"modTime"`
	Size     int64     `json:"size"`
}

func listProfiles(dir string) ([]profileMeta, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var metas []profileMeta
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".yaml")
		info, err := e.Info()
		if err != nil {
			continue
		}
		desc := ""
		if data, err := os.ReadFile(filepath.Join(dir, e.Name())); err == nil {
			desc, _ = splitDesc(string(data))
		}
		metas = append(metas, profileMeta{Name: name, Desc: desc, ModTime: info.ModTime(), Size: info.Size()})
	}
	return metas, nil
}

// ---------- 项目候选 API ----------

func (s *webServer) handleProjects(w http.ResponseWriter, r *http.Request) {
	profile := r.URL.Query().Get("profile")
	if profile == "" || !validConfigName.MatchString(profile) {
		http.Error(w, "缺少 profile 参数", http.StatusBadRequest)
		return
	}
	path := s.configPath(profile)
	if !fileExists(path) {
		http.Error(w, "配置不存在", http.StatusNotFound)
		return
	}
	cfg, err := loadConfig(path)
	if err != nil {
		http.Error(w, "配置校验失败: "+err.Error(), http.StatusBadRequest)
		return
	}
	client := newClient(cfg)
	projects, err := client.getProjectList()
	if err != nil {
		http.Error(w, "获取项目列表失败: "+err.Error(), http.StatusInternalServerError)
		return
	}
	names := cfg.Project.Names
	if len(names) == 0 && cfg.Project.Name != "" {
		names = []string{cfg.Project.Name}
	}
	type cand struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		CreateTime string `json:"createTime"`
	}
	var groups []map[string]any
	for _, n := range names {
		if n == "*" || n == "all" || n == "ALL" {
			groups = append(groups, map[string]any{"name": n, "mode": "all"})
			continue
		}
		var cands []cand
		for _, p := range projects {
			if p.Name == n {
				cands = append(cands, cand{ID: p.ID, Name: p.Name, CreateTime: p.CreateTime})
			}
		}
		groups = append(groups, map[string]any{"name": n, "mode": "named", "candidates": cands})
	}
	jsonResponse(w, groups)
}

// ---------- 运行 API ----------

func (s *webServer) handleRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "仅支持 POST", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Profile   string `json:"profile"`
		ProjectID string `json:"projectId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "请求格式错误", http.StatusBadRequest)
		return
	}
	if !validConfigName.MatchString(req.Profile) {
		http.Error(w, "配置名非法", http.StatusBadRequest)
		return
	}
	if !fileExists(s.configPath(req.Profile)) {
		http.Error(w, "配置不存在: "+req.Profile, http.StatusNotFound)
		return
	}
	s.mu.Lock()
	if s.currentJob != nil && s.currentJob.Status == "running" {
		s.mu.Unlock()
		http.Error(w, "已有任务运行中，请等待完成", http.StatusConflict)
		return
	}
	job := &Job{ID: newSessionID(), Profile: req.Profile, Status: "running", StartedAt: time.Now(), Total: -1}
	s.currentJob = job
	s.mu.Unlock()

	// 同名多项目预选（供 projectChooser 使用；一次一个任务，全局安全）
	if req.ProjectID != "" {
		webProjectID = req.ProjectID
	}
	go s.runJob(job)
	jsonResponse(w, jobSummary(job))
}

// runJob 后台执行一次运行：拉主机 -> 流水线 -> 导出，全程推送 SSE 事件
func (s *webServer) runJob(job *Job) {
	defer func() {
		job.FinishedAt = time.Now()
		if job.Status == "running" {
			job.Status = "done"
		}
		if job.Error != "" {
			job.Status = "failed"
		}
		webProjectID = ""
		s.mu.Lock()
		s.history = append([]*Job{job}, s.history...)
		if len(s.history) > s.settings.HistorySize {
			s.history = s.history[:s.settings.HistorySize]
		}
		if s.currentJob == job {
			s.currentJob = nil
		}
		s.mu.Unlock()
		s.hub.publish(job.ID, "finish", jobSummary(job))
	}()

	cfg, err := loadConfig(s.configPath(job.Profile))
	if err != nil {
		job.Error = "配置校验失败: " + err.Error()
		return
	}
	client := newClient(cfg)
	projects, allMode, err := resolveProjects(client, cfg)
	if err != nil {
		job.Error = "解析项目失败: " + err.Error()
		return
	}
	vms, err := collectVMs(client, cfg, projects, allMode)
	if err != nil {
		job.Error = "获取服务器失败: " + err.Error()
		return
	}
	if len(vms) == 0 {
		job.Error = "无匹配服务器（可能被 IP 筛选全部过滤或项目无主机）"
		return
	}
	job.Total = len(vms)
	s.hub.publish(job.ID, "log", map[string]any{"line": fmt.Sprintf("共 %d 台服务器", len(vms))})

	onceResults, globalStopped := runPipelineOnce(cfg)
	prog := newProgressMgr(len(vms), func(p *progressMgr) {
		job.Done = p.done
		job.Total = p.total
		job.Progress = p.line()
		s.hub.publish(job.ID, "progress", map[string]any{
			"done": job.Done, "total": job.Total, "pct": pctPercent(job.Done, job.Total), "line": job.Progress,
		})
	})
	runSSHTests(cfg, vms, onceResults, globalStopped, prog, func(vm *VM) {
		s.hub.publish(job.ID, "log", map[string]any{"line": fmt.Sprintf("%s (%s) %s", vm.Name, vm.IP, vm.SSHResult)})
	})
	job.VMs = vms

	// 导出 Excel（output.dir 配置了才导出，文件名自动生成）
	if excelPath := autoExcelPath(job.Profile, cfg); excelPath != "" {
		if err := exportExcel(excelPath, vms); err != nil {
			job.Error = "导出Excel失败: " + err.Error()
			return
		}
		job.ExcelFile = excelPath
	}
}

func pctPercent(done, total int) int {
	if total <= 0 {
		return 0
	}
	return done * 100 / total
}

// ---------- SSE 事件流 ----------

func (s *webServer) handleEvents(w http.ResponseWriter, r *http.Request) {
	jobID := r.URL.Query().Get("job")
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "不支持 SSE", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	ch := s.hub.subscribe(jobID)
	defer s.hub.unsubscribe(jobID, ch)

	// 连接即推当前快照，避免错过事件
	s.mu.Lock()
	var j *Job
	if s.currentJob != nil && s.currentJob.ID == jobID {
		j = s.currentJob
	} else {
		for _, h := range s.history {
			if h.ID == jobID {
				j = h
				break
			}
		}
	}
	s.mu.Unlock()
	if j != nil {
		if data, err := json.Marshal(jobSummary(j)); err == nil {
			fmt.Fprintf(w, "event: snapshot\ndata: %s\n\n", data)
			flusher.Flush()
		}
	}

	for {
		select {
		case ev := <-ch:
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, ev.Data)
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

// ---------- 历史 / 结果 / 导出 API ----------

func (s *webServer) handleHistory(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	items := make([]map[string]any, 0, len(s.history))
	for _, j := range s.history {
		items = append(items, jobSummary(j))
	}
	s.mu.Unlock()
	jsonResponse(w, items)
}

func (s *webServer) findJob(id string) *Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.currentJob != nil && s.currentJob.ID == id {
		return s.currentJob
	}
	for _, j := range s.history {
		if j.ID == id {
			return j
		}
	}
	return nil
}

func (s *webServer) handleResult(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("job")
	job := s.findJob(id)
	if job == nil {
		http.Error(w, "任务不存在", http.StatusNotFound)
		return
	}
	jsonResponse(w, map[string]any{
		"summary":   jobSummary(job),
		"vms":       vmsToViews(job.VMs),
		"excelPath": job.ExcelFile,
	})
}

func (s *webServer) handleExport(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("job")
	job := s.findJob(id)
	if job == nil {
		http.Error(w, "任务不存在", http.StatusNotFound)
		return
	}
	if job.ExcelFile == "" || !fileExists(job.ExcelFile) {
		http.Error(w, "该任务无导出文件（配置未设置 output.dir 或运行失败）", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Disposition", `attachment; filename="`+filepath.Base(job.ExcelFile)+`"`)
	http.ServeFile(w, r, job.ExcelFile)
}

// ---------- 工具 ----------

func newSessionID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func jsonResponse(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(v)
}

// ---------- 结果视图（页面表格 JSON，字段精简） ----------

type stepView struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Target   string `json:"target"`
	State    string `json:"state"`
	Label    string `json:"label"`
	ExitCode int    `json:"exitCode"`
	Duration string `json:"duration"`
	Error    string `json:"error"`
	Output   string `json:"output"`
}

type serviceView struct {
	Name  string `json:"name"`
	State string `json:"state"`
	Label string `json:"label"`
}

type vmView struct {
	Name        string        `json:"name"`
	Type        string        `json:"type"`
	IP          string        `json:"ip"`
	EIP         string        `json:"eip"`
	MAC         string        `json:"mac"`
	Status      string        `json:"status"`
	Spec        string        `json:"spec"`
	SysDisk     string        `json:"sysDisk"`
	DataDisk    string        `json:"dataDisk"`
	Project     string        `json:"project"`
	Password    string        `json:"password"`
	SSHResult   string        `json:"sshResult"`
	StatusLine  string        `json:"statusLine"`
	Services    []serviceView `json:"services"`
	Steps       []stepView    `json:"steps"`
	PipelineStr string        `json:"pipelineStr"`
}

// vmsToViews 构造页面展示用的精简视图（复用屏幕/Excel 的格式化函数）
func vmsToViews(vms []*VM) []vmView {
	views := make([]vmView, 0, len(vms))
	for _, vm := range vms {
		v := vmView{
			Name: vm.Name, Type: vm.Type, IP: vm.IP, EIP: vm.EIP, MAC: vm.MAC,
			Status: vm.Status, Spec: specStr(vm), SysDisk: sysDiskStr(vm),
			DataDisk: dataDiskStr(vm), Project: vm.ProjectName,
			Password: vm.Password, SSHResult: vm.SSHResult,
			StatusLine: statusStr(vm), PipelineStr: pipelineStr(vm),
		}
		for _, svc := range vm.Services {
			v.Services = append(v.Services, serviceView{Name: svc.Name, State: svc.State, Label: serviceStateLabel(svc.State)})
		}
		for _, s := range vm.ExecSteps {
			if s == nil {
				continue
			}
			v.Steps = append(v.Steps, stepView{
				Name: s.Name, Type: s.Type, Target: s.Target, State: s.State,
				Label: stepResultLabel(s), ExitCode: s.ExitCode, Duration: s.Duration,
				Error: s.Error, Output: s.Output,
			})
		}
		views = append(views, v)
	}
	return views
}
