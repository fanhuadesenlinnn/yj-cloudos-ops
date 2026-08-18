package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
)

var (
	version      = "dev"
	configPath   = flag.String("c", "config.yaml", "YAML 配置文件路径")
	showVer      = flag.Bool("v", false, "显示版本号")
	listRegions  = flag.Bool("list-regions", false, "列出账号可见的区域ID（ProductCode=VM），用于填写 regionId")
	listProjects = flag.Bool("list-projects", false, "列出账号可见的项目，用于填写 project.name")
)

func main() {
	flag.Parse()

	if *showVer {
		fmt.Printf("yj-cloudos-ops %s\n", version)
		os.Exit(0)
	}

	cfg, err := loadConfig(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "加载配置失败: %v\n", err)
		os.Exit(1)
	}
	client := newClient(cfg)

	if *listRegions {
		if err := printRegions(client); err != nil {
			fmt.Fprintf(os.Stderr, "查询区域失败: %v\n", err)
			os.Exit(1)
		}
		os.Exit(0)
	}

	if *listProjects {
		if err := printProjects(client); err != nil {
			fmt.Fprintf(os.Stderr, "查询项目失败: %v\n", err)
			os.Exit(1)
		}
		os.Exit(0)
	}

	// 1. 解析项目（支持多项目/全部项目，同名多项目交互选择）
	projects, allMode, err := resolveProjects(client, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "解析项目失败: %v\n", err)
		os.Exit(1)
	}
	if allMode {
		fmt.Fprintf(os.Stderr, "模式: 检查全部项目\n")
	}

	// 2. 拉取虚拟机
	vms, err := collectVMs(client, cfg, projects, allMode)
	if err != nil {
		fmt.Fprintf(os.Stderr, "获取虚拟机失败: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "共 %d 台虚拟机\n", len(vms))
	if len(vms) == 0 {
		os.Exit(0)
	}

	// 3. SSH 登录测试 + 服务器运行状态采集（并发，进度输出到 stderr）
	runSSHTests(cfg, vms)

	// 4. 屏幕输出 + 可选导出
	if err := outputTable(cfg, vms); err != nil {
		fmt.Fprintf(os.Stderr, "屏幕输出失败: %v\n", err)
	}
	if cfg.Output.CSVPath != "" {
		if err := exportCSV(cfg, vms); err != nil {
			fmt.Fprintf(os.Stderr, "导出CSV失败: %v\n", err)
		} else {
			fmt.Fprintf(os.Stderr, "已导出CSV: %s\n", cfg.Output.CSVPath)
		}
	}
	if cfg.Output.ExcelPath != "" {
		if err := exportExcel(cfg, vms); err != nil {
			fmt.Fprintf(os.Stderr, "导出Excel失败: %v\n", err)
		} else {
			fmt.Fprintf(os.Stderr, "已导出Excel: %s\n", cfg.Output.ExcelPath)
		}
	}
}

// collectVMs 拉取全部主机，按项目过滤（allMode 时不过滤），并补充密码、MAC、磁盘、项目名信息
func collectVMs(c *Client, cfg *Config, projects []*Project, allMode bool) ([]*VM, error) {
	projectSet := map[string]bool{}
	for _, p := range projects {
		projectSet[p.ID] = true
	}

	// 2.1 DescribeEcs 分页拉全量，按项目过滤（接口不支持按项目查询）
	var vms []*VM
	pageSize := cfg.Pagination.PageSize
	page := 1
	for {
		resp, err := c.describeEcs(cfg.RegionID, page, pageSize)
		if err != nil {
			return nil, fmt.Errorf("DescribeEcs 第%d页失败: %w", page, err)
		}
		for _, item := range resp.List {
			if !allMode && !projectSet[item.ProjectID] {
				continue
			}
			vms = append(vms, &VM{
				ID:        item.InstanceID,
				Name:      item.InstanceName,
				IP:        item.IP,
				EIP:       item.EipAddr,
				Status:    item.Status,
				SpecCode:  item.InstanceCode,
				SpecName:  item.InstanceCodeName,
				CPU:       toInt(item.InstanceCPU),
				Memory:    toInt(item.InstanceMemory),
				SysDiskID: item.SysDiskID,
				SysDisk: Disk{
					ID:       item.SysDiskID,
					Size:     toInt(item.SysDiskSize),
					SpecCode: item.SysDiskCode,
					Type:     "SYSTEM_DISK",
				},
				ProjectID: item.ProjectID,
			})
		}
		if resp.TotalPages <= 0 || page >= resp.TotalPages {
			break
		}
		page++
	}

	// 2.2 GetEcsPassword 逐台取 root 初始密码
	for i, vm := range vms {
		pw, err := c.getEcsPassword(cfg.RegionID, vm.ID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "警告: 获取 %s 密码失败: %v\n", vm.Name, err)
			continue
		}
		vms[i].Password = pw
	}

	// 2.3 DescribeEnis 全量拉网卡，按 vmId 取 MAC（尽力而为，接口返回无 macAddr 则留空）
	eniByVM := map[string][]string{}
	eniPage := 1
	for {
		resp, err := c.describeEnis(cfg.RegionID, eniPage, pageSize)
		if err != nil {
			fmt.Fprintf(os.Stderr, "警告: DescribeEnis 第%d页失败: %v（MAC 可能缺失）\n", eniPage, err)
			break
		}
		for _, e := range resp.List {
			if e.VmID == "" {
				continue
			}
			mac := toStr(e.MacAddr)
			if mac != "" {
				eniByVM[e.VmID] = append(eniByVM[e.VmID], mac)
			}
		}
		if resp.TotalPages <= 0 || eniPage >= resp.TotalPages {
			break
		}
		eniPage++
	}
	for i, vm := range vms {
		if macs, ok := eniByVM[vm.ID]; ok && len(macs) > 0 {
			vms[i].MAC = strings.Join(macs, "; ")
		}
	}

	// 2.4 DescribeDisks 全量拉云硬盘，按 attachInfos.instanceId 匹配数据盘；同时收集 projectId->项目名称 映射
	projectNameByID := map[string]string{}
	for _, p := range projects {
		projectNameByID[p.ID] = p.Name
	}
	diskByVM := map[string][]Disk{}
	diskPage := 1
	for {
		resp, err := c.describeDisks(cfg.RegionID, diskPage, pageSize)
		if err != nil {
			return nil, fmt.Errorf("DescribeDisks 第%d页失败: %w", diskPage, err)
		}
		for _, r := range resp.Records {
			if r.ProjectID != "" && r.ProjectName != "" {
				if _, ok := projectNameByID[r.ProjectID]; !ok {
					projectNameByID[r.ProjectID] = r.ProjectName
				}
			}
			if r.DiskType == "SYSTEM_DISK" {
				continue
			}
			for _, ai := range r.AttachInfos {
				if ai.InstanceID == "" {
					continue
				}
				diskByVM[ai.InstanceID] = append(diskByVM[ai.InstanceID], Disk{
					ID:       r.DiskID,
					Name:     r.DiskName,
					Size:     toInt(r.DiskSize),
					Type:     r.DiskType,
					SpecCode: r.SpecificationCode,
					SpecName: r.SpecificationName,
					Status:   r.Status,
				})
			}
		}
		if resp.Pages <= 0 || diskPage >= resp.Pages {
			break
		}
		diskPage++
	}
	for i, vm := range vms {
		vms[i].DataDisks = diskByVM[vm.ID]
		vms[i].ProjectName = projectNameByID[vm.ProjectID]
		if vms[i].ProjectName == "" {
			vms[i].ProjectName = vm.ProjectID // 未知项目名时显示项目ID
		}
	}

	return vms, nil
}
