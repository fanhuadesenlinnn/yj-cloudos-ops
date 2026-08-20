package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// resolveProjects 解析项目列表（支持多项目与全部项目）
// 返回 projects（已去重的项目列表）与 allMode（是否检查全部项目）
func resolveProjects(c *Client, cfg *Config) ([]*Project, bool, error) {
	names := cfg.Project.Names
	if len(names) == 0 && cfg.Project.Name != "" {
		names = []string{cfg.Project.Name}
	}
	// 全部项目模式
	for _, n := range names {
		if n == "*" || n == "all" || n == "ALL" {
			return nil, true, nil
		}
	}

	seen := map[string]bool{}
	var projects []*Project
	for _, name := range names {
		p, err := resolveProject(c, cfg, name)
		if err != nil {
			return nil, false, err
		}
		if seen[p.ID] {
			continue
		}
		seen[p.ID] = true
		projects = append(projects, p)
		fmt.Fprintf(os.Stderr, "目标项目: %s (ID=%s)\n", p.Name, p.ID)
	}
	return projects, false, nil
}

// resolveProject 解析单个项目：
// 1. 先全量拉 GetProjectList 做精确名称匹配（同名多项目交互选择）
// 2. 若 GetProjectList 未返回该项目，兑底从 DescribeDisks 数据中的 projectName 反查
func resolveProject(c *Client, cfg *Config, name string) (*Project, error) {
	projects, err := c.getProjectList()
	if err != nil {
		return nil, err
	}
	p, found, err := exactMatchOne(projects, name)
	if err != nil {
		return nil, err
	}
	if found {
		return p, nil
	}

	// 兑底：从云硬盘数据反查项目
	fmt.Fprintf(os.Stderr, "GetProjectList 中未找到项目 %q，尝试从云硬盘数据反查...\n", name)
	diskProjects, err := c.diskProjectCatalog(cfg)
	if err != nil {
		return nil, fmt.Errorf("云硬盘数据反查失败: %w", err)
	}
	p, found, err = exactMatchOne(diskProjects, name)
	if err != nil {
		return nil, err
	}
	if found {
		fmt.Fprintf(os.Stderr, "已通过云硬盘数据解析到项目: %s (ID=%s)\n", p.Name, p.ID)
		return p, nil
	}

	// 都找不到：报错并列出已知项目帮助用户排查
	known := make([]string, 0, len(projects))
	for _, pp := range projects {
		known = append(known, fmt.Sprintf("%s(%s)", pp.Name, pp.ID))
	}
	for _, pp := range diskProjects {
		known = append(known, fmt.Sprintf("%s(%s)", pp.Name, pp.ID))
	}
	return nil, fmt.Errorf("未找到名称为 %q 的项目。当前可识别项目: %v", name, known)
}

// projectChooser 同名多项目选择器：CLI 默认屏幕交互（stdin），Web 模式替换为按预选 ID 匹配。
// 运行前由调用方设置（Web 模式设置 webProjectID），用后即清。
var projectChooser = chooseProject

// webProjectID Web 模式下用户在同名项目中预选的 ID（run 请求携带）；chooser 按此匹配。
var webProjectID string

// exactMatchOne 精确名称匹配：0 个 -> found=false；1 个直接返回；多个同名按选择器决策
func exactMatchOne(projects []*Project, name string) (*Project, bool, error) {
	var matches []*Project
	for _, p := range projects {
		if p.Name == name {
			matches = append(matches, p)
		}
	}
	switch len(matches) {
	case 0:
		return nil, false, nil
	case 1:
		return matches[0], true, nil
	default:
		p, err := projectChooser(matches)
		if err != nil {
			return nil, false, err
		}
		return p, true, nil
	}
}

// chooseProject 同名多项目时在屏幕列出（名称+创建时间+ID），让用户输入序号选择
func chooseProject(matches []*Project) (*Project, error) {
	fmt.Printf("发现 %d 个同名项目，请选择（Ctrl+C 取消）:\n", len(matches))
	for i, p := range matches {
		createTime := p.CreateTime
		if createTime == "" {
			createTime = "未知"
		}
		fmt.Printf("  [%d] 项目: %s | 创建时间: %s | 项目ID: %s | 类型: %s | 启用: %s | 描述: %s\n",
			i+1, p.Name, createTime, p.ID, orDash(p.TypeName), enabledStr(p.Enabled), orDash(p.Description))
	}
	reader := bufio.NewReader(os.Stdin)
	for {
		fmt.Print("请输入序号: ")
		line, err := reader.ReadString('\n')
		if err != nil {
			return nil, fmt.Errorf("读取输入失败: %w", err)
		}
		n, err := strconv.Atoi(strings.TrimSpace(line))
		if err == nil && n >= 1 && n <= len(matches) {
			return matches[n-1], nil
		}
		fmt.Println("输入无效，请输入 1-" + strconv.Itoa(len(matches)) + " 之间的数字")
	}
}

func enabledStr(v int) string {
	if v == 1 {
		return "是"
	}
	return "否"
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
