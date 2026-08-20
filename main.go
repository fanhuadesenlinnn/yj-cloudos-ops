package main

import (
	"flag"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

var reDiskSize = regexp.MustCompile(`^([0-9.]+)\s*([TtGgMm])?$`)

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
	fmt.Fprintf(os.Stderr, "共 %d 台服务器\n", len(vms))
	if len(vms) == 0 {
		os.Exit(0)
	}

	// 阶段一：流水线中 target=local 且 run=once 的步骤只跑一次（如本地构建/打包），
	// 结果供每台机器复用；某步失败且 onError=stop 则全局终止，不再执行远端步骤。
	for i, step := range cfg.EffectiveSteps() {
		if !StepIsLocal(step) || StepRunMode(step) != "once" {
			continue
		}
		fmt.Fprintf(os.Stderr, "[流水线] 本地步骤: %s\n", StepName(step, i))
	}
	onceResults, globalStopped := runPipelineOnce(cfg)
	for i, res := range onceResults {
		if res == nil {
			continue
		}
		fmt.Fprintf(os.Stderr, "[流水线] 本地步骤完成: %s -> %s（%s）\n", StepName(cfg.EffectiveSteps()[i], i), stepResultLabel(res), res.Duration)
	}

	// 3. SSH 登录测试 + 流水线步骤执行（并发，进度输出到 stderr）
	runSSHTests(cfg, vms, onceResults, globalStopped)

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

// collectVMs 拉取全部服务器（按 resource.type 支持 ECS/BMS/全部），按项目过滤，补充密码、MAC、磁盘、项目名
func collectVMs(c *Client, cfg *Config, projects []*Project, allMode bool) ([]*VM, error) {
	projectSet := map[string]bool{}
	for _, p := range projects {
		projectSet[p.ID] = true
	}
	pageSize := cfg.Pagination.PageSize

	// 2.1 按资源类型拉取 ECS / 裸金属
	var vms []*VM
	switch cfg.Resource.Type {
	case "bms":
		bms, err := collectBMS(c, cfg, projectSet, allMode, pageSize)
		if err != nil {
			return nil, err
		}
		vms = append(vms, bms...)
	case "all":
		ecs, err := collectECS(c, cfg, projectSet, allMode, pageSize)
		if err != nil {
			return nil, err
		}
		bms, err := collectBMS(c, cfg, projectSet, allMode, pageSize)
		if err != nil {
			return nil, err
		}
		vms = append(vms, ecs...)
		vms = append(vms, bms...)
	default: // ecs
		ecs, err := collectECS(c, cfg, projectSet, allMode, pageSize)
		if err != nil {
			return nil, err
		}
		vms = append(vms, ecs...)
	}
	if len(vms) == 0 {
		return vms, nil
	}

	// 2.2 DescribeEnis 全量拉网卡，按 vmId/eniId 取 MAC（尽力而为，接口返回无 macAddr 则留空）
	eniByVM := map[string][]string{}
	eniByID := map[string]string{}
	eniPage := 1
	for {
		resp, err := c.describeEnis(cfg.RegionID, eniPage, pageSize)
		if err != nil {
			fmt.Fprintf(os.Stderr, "警告: DescribeEnis 第%d页失败: %v（MAC 可能缺失）\n", eniPage, err)
			break
		}
		for _, e := range resp.List {
			mac := toStr(e.MacAddr)
			if mac == "" {
				continue
			}
			if e.VmID != "" {
				eniByVM[e.VmID] = append(eniByVM[e.VmID], mac)
			}
			if e.InstanceID != "" {
				eniByID[e.InstanceID] = mac
			}
		}
		if resp.TotalPages <= 0 || eniPage >= resp.TotalPages {
			break
		}
		eniPage++
	}
	for i, vm := range vms {
		var macs []string
		if m, ok := eniByVM[vm.ID]; ok {
			macs = append(macs, m...)
		}
		for _, eid := range vm.EniIDs { // 裸金属网卡
			if m, ok := eniByID[eid]; ok {
				macs = append(macs, m)
			}
		}
		if len(macs) > 0 {
			vms[i].MAC = strings.Join(dedupe(macs), "; ")
		}
	}

	// 2.3 DescribeDisks 全量拉云硬盘，按 attachInfos.instanceId 匹配数据盘；同时收集 projectId->项目名称 映射
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
		if vm.Type != "裸金属" { // 裸金属的本地盘已在 DetailBms 填充
			vms[i].DataDisks = diskByVM[vm.ID]
		}
	}

	// 2.4 用 GetProjectList 补全项目名（all 模式/无磁盘项目也能显示名称）
	if pl, err := c.getProjectList(); err == nil {
		for _, p := range pl {
			if _, ok := projectNameByID[p.ID]; !ok {
				projectNameByID[p.ID] = p.Name
			}
		}
	}
	for i, vm := range vms {
		vms[i].ProjectName = projectNameByID[vm.ProjectID]
		if vms[i].ProjectName == "" {
			vms[i].ProjectName = vm.ProjectID // 未知项目名时显示项目ID
		}
	}

	return vms, nil
}

// collectECS 拉取弹性云主机列表并按项目过滤，逐台取密码
func collectECS(c *Client, cfg *Config, projectSet map[string]bool, allMode bool, pageSize int) ([]*VM, error) {
	var vms []*VM
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
			vm := &VM{
				ID:        item.InstanceID,
				Name:      item.InstanceName,
				Type:      "虚拟机",
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
			}
			vms = append(vms, vm)
		}
		if resp.TotalPages <= 0 || page >= resp.TotalPages {
			break
		}
		page++
	}
	// 并发获取密码（带进度提示）
	fetchPasswords(c, cfg, vms, "获取虚拟机密码", func(id string) (string, error) {
		return c.getEcsPassword(cfg.RegionID, id)
	})
	return vms, nil
}

// fetchPasswords 并发获取密码，带进度提示
func fetchPasswords(c *Client, cfg *Config, vms []*VM, label string, get func(id string) (string, error)) {
	total := len(vms)
	if total == 0 {
		return
	}
	jobs := make(chan int)
	var wg sync.WaitGroup
	var done int64
	workers := cfg.HTTPWorkers()
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range jobs {
				pw, err := get(vms[idx].ID)
				if err != nil {
					fmt.Fprintf(os.Stderr, "\n警告: 获取 %s 密码失败: %v\n", vms[idx].Name, err)
				} else {
					vms[idx].Password = pw
				}
				n := atomic.AddInt64(&done, 1)
				fmt.Fprintf(os.Stderr, "\r%s: %d/%d", label, n, total)
			}
		}()
	}
	for i := range vms {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	fmt.Fprintln(os.Stderr)
}

// fetchBmsDetails 并发获取裸金属详情（系统盘/数据盘）
func fetchBmsDetails(c *Client, cfg *Config, vms []*VM) {
	total := len(vms)
	if total == 0 {
		return
	}
	jobs := make(chan int)
	var wg sync.WaitGroup
	var done int64
	workers := cfg.HTTPWorkers()
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range jobs {
				vm := vms[idx]
				detail, err := c.detailBms(cfg.RegionID, vm.ID)
				if err != nil {
					fmt.Fprintf(os.Stderr, "\n警告: 获取 %s 详情失败: %v\n", vm.Name, err)
				} else {
					if detail.SysDisk != "" {
						vms[idx].SysDiskDesc = detail.SysDisk
					}
					if detail.DataDisk != "" {
						vms[idx].DataDiskDesc = detail.DataDisk
					}
					for _, dd := range detail.DataDisks {
						desc, size := bmsDiskSize(dd.Size)
						vms[idx].DataDisks = append(vms[idx].DataDisks, Disk{
							Size:     size,
							SizeDesc: desc,
							Type:     dd.Type,
							SpecCode: dd.SpecificationCode,
							SpecName: dd.SpecificationName,
						})
					}
				}
				n := atomic.AddInt64(&done, 1)
				fmt.Fprintf(os.Stderr, "\r%s: %d/%d", "获取裸金属详情", n, total)
			}
		}()
	}
	for i := range vms {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	fmt.Fprintln(os.Stderr)
}

// collectBMS 拉取裸金属列表并按项目过滤，逐台取密码与详情（结构化数据盘）
func collectBMS(c *Client, cfg *Config, projectSet map[string]bool, allMode bool, pageSize int) ([]*VM, error) {
	var vms []*VM
	page := 1
	for {
		resp, err := c.describeBms(cfg.RegionID, page, pageSize)
		if err != nil {
			return nil, fmt.Errorf("DescribeBms 第%d页失败: %w", page, err)
		}
		for _, item := range resp.List {
			if !allMode && !projectSet[item.ProjectID] {
				continue
			}
			vm := &VM{
				ID:           item.InstanceID,
				Name:         item.InstanceName,
				Type:         "裸金属",
				IP:           item.IP,
				EIP:          item.EipAddr,
				Status:       item.Status,
				SpecCode:     item.InstanceCode,
				SpecName:     item.InstanceCodeName,
				CPU:          toInt(item.InstanceCPU),
				Memory:       toInt(item.InstanceMemory),
				SysDiskDesc:  item.SysDisk,
				DataDiskDesc: item.DataDisk,
				ProjectID:    item.ProjectID,
			}
			for _, ni := range item.NetworkInterfaces {
				if ni.EniID != "" {
					vm.EniIDs = append(vm.EniIDs, ni.EniID)
				}
			}
			vms = append(vms, vm)
		}
		if resp.TotalPages <= 0 || page >= resp.TotalPages {
			break
		}
		page++
	}
	// 并发获取密码与详情（带进度提示）
	fetchPasswords(c, cfg, vms, "获取裸金属密码", func(id string) (string, error) {
		return c.getBmsPassword(cfg.RegionID, id)
	})
	fetchBmsDetails(c, cfg, vms)
	return vms, nil
}

// bmsDiskSize 解析裸金属数据盘容量描述（"100"->"100G", "1.92T" 原样保留）
func bmsDiskSize(v any) (desc string, size int) {
	s := strings.TrimSpace(toStr(v))
	if s == "" {
		return "", 0
	}
	if n, err := strconv.ParseFloat(s, 64); err == nil {
		return fmt.Sprintf("%.0fG", n), int(n)
	}
	if m := reDiskSize.FindStringSubmatch(s); m != nil {
		if f, err := strconv.ParseFloat(m[1], 64); err == nil {
			mult := 1.0
			switch strings.ToUpper(m[2]) {
			case "T":
				mult = 1024
			case "M":
				mult = 1.0 / 1024
			}
			return s, int(f * mult)
		}
	}
	return s, 0
}

// dedupe 字符串去重（保持顺序）
func dedupe(items []string) []string {
	seen := map[string]bool{}
	out := items[:0]
	for _, it := range items {
		if !seen[it] {
			seen[it] = true
			out = append(out, it)
		}
	}
	return out
}
