package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// resolveProject 通过 GetProjectList 按名称解析项目
// 0 个匹配 -> 报错；1 个匹配 -> 直接使用；多个同名 -> 交互式让用户选择
func resolveProject(c *Client, cfg *Config) (*Project, error) {
	projects, err := c.getProjectList(cfg.Project.Name)
	if err != nil {
		return nil, err
	}

	var matches []*Project
	for _, p := range projects {
		if p.Name == cfg.Project.Name { // 精确匹配
			matches = append(matches, p)
		}
	}

	switch len(matches) {
	case 0:
		names := make([]string, 0, len(projects))
		for _, p := range projects {
			names = append(names, p.Name)
		}
		return nil, fmt.Errorf("未找到名称为 %q 的项目（模糊搜索结果: %v）", cfg.Project.Name, names)
	case 1:
		return matches[0], nil
	default:
		return chooseProject(matches)
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
