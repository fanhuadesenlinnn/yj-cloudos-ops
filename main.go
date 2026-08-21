package main

import (
	_ "embed"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var reDiskSize = regexp.MustCompile(`^([0-9.]+)\s*([TtGgMm])?$`)

//go:embed config.example.yaml
var exampleConfigYAML string

var (
	version      = "dev"
	configPath   = flag.String("c", "configs/demo.yaml", "YAML 配置文件路径（默认 configs/demo.yaml）")
	showVer      = flag.Bool("v", false, "显示版本号")
	listRegions  = flag.Bool("list-regions", false, "列出账号可见的区域ID（ProductCode=VM），用于填写 regionId")
	listProjects = flag.Bool("list-projects", false, "列出账号可见的项目，用于填写 project.name")
	initConfig   = flag.Bool("init", false, "生成一份带注释的示例配置文件到 configs 目录（默认 configs/demo.yaml）")
	webMode      = flag.Bool("web", false, "启动 Web 模式（浏览器管理配置/运行/导出）")
	webAddr      = flag.String("web-addr", "", "Web 监听地址（默认取 settings.yaml 中的 addr，再缺省 0.0.0.0:8080）")
	webConfigs   = flag.String("web-configs", "", "Web 配置目录（默认取 settings 里的 configsDir）")
	webSettings  = flag.String("web-settings", "settings.yaml", "Web 设置文件路径")
	daemonMode   = flag.Bool("daemon", false, "后台运行（与 -web 搭配）：脱离终端，命令行窗口可关闭，日志写 web.log")
	stopMode     = flag.Bool("stop", false, "停止后台运行的 Web 实例（读取 web.pid 优雅退出）")
	serviceMode  = flag.String("service", "", "Windows 服务管理（仅 Windows）: install 安装 / uninstall 卸载 / run 以服务运行")
)

func main() {
	flag.Parse()

	if *showVer {
		fmt.Printf("yj-cloudos-ops %s\n", version)
		os.Exit(0)
	}

	// -stop：停止后台 Web 实例（读 web.pid 优雅退出）
	if *stopMode {
		stopWebDaemon(*webSettings)
		return
	}

	// Windows 服务管理（-service install / uninstall / run）
	if *serviceMode != "" {
		// install / uninstall 需要管理员权限：非管理员时自动 UAC 提权重启
		if *serviceMode == "install" || *serviceMode == "uninstall" {
			relaunched, err := ensureAdmin()
			if err != nil {
				fmt.Fprintf(os.Stderr, "%v\n", err)
				os.Exit(1)
			}
			if relaunched {
				fmt.Fprintf(os.Stderr, "正在请求管理员权限（UAC 弹窗），请在弹出窗口中点击「是」...\n")
				os.Exit(0)
			}
		}
		switch *serviceMode {
		case "install":
			if err := installService(*webSettings); err != nil {
				fmt.Fprintf(os.Stderr, "安装服务失败: %v\n", err)
				os.Exit(1)
			}
			os.Exit(0)
		case "uninstall":
			if err := uninstallService(); err != nil {
				fmt.Fprintf(os.Stderr, "卸载服务失败: %v\n", err)
				os.Exit(1)
			}
			os.Exit(0)
		case "run":
			// 由服务控制管理器拉起（net start），直接跑服务主体
			if err := runWebAsService(*webSettings); err != nil {
				fmt.Fprintf(os.Stderr, "服务运行失败: %v\n", err)
				os.Exit(1)
			}
			os.Exit(0)
		default:
			fmt.Fprintf(os.Stderr, "-service 取值非法: %q（支持 install / uninstall / run）\n", *serviceMode)
			os.Exit(1)
		}
	}

	// -daemon 必须与 -web 搭配使用（-daemon 只是后台化 Web 的修饰参数，单独用无意义）
	if *daemonMode && !*webMode {
		fmt.Fprintf(os.Stderr, "-daemon 必须与 -web 搭配使用\n\n")
		fmt.Fprintf(os.Stderr, "用法示例:\n")
		fmt.Fprintf(os.Stderr, "  %s -web -daemon              # 后台运行 Web（命令行窗口可关闭，日志写 web.log）\n", filepath.Base(os.Args[0]))
		fmt.Fprintf(os.Stderr, "  %s -web -daemon -web-addr 0.0.0.0:8080\n", filepath.Base(os.Args[0]))
		fmt.Fprintf(os.Stderr, "  %s -stop                      # 停止后台实例（读 web.pid 优雅退出）\n", filepath.Base(os.Args[0]))
		os.Exit(1)
	}

	// Web 模式：浏览器管理配置与运行（CLI 模式保持原样）；-daemon 后台化
	if *webMode {
		// 先加载 settings 获取端口，命令行 -web-addr 优先
		st, err := loadSettings(*webSettings)
		if err != nil {
			fmt.Fprintf(os.Stderr, "加载设置失败: %v\n", err)
			os.Exit(1)
		}
		addr := *webAddr
		if addr == "" {
			addr = st.Addr
		}
		runWeb(addr, *webConfigs, *webSettings, *daemonMode)
		return
	}

	// -init：生成示例配置文件（已存在则不覆盖），默认生成到 configs 目录
	if *initConfig {
		if err := initExampleConfig(*configPath); err != nil {
			fmt.Fprintf(os.Stderr, "生成示例配置失败: %v\n", err)
			os.Exit(1)
		}
		os.Exit(0)
	}

	cfg, err := loadConfig(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "加载配置失败: %v\n", err)
		os.Exit(1)
	}
	// 配置名（导出文件命名用）：取配置文件 basename（configs/生产环境.yaml -> 生产环境）
	profile := strings.TrimSuffix(filepath.Base(*configPath), filepath.Ext(*configPath))
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
		logLine("模式: 检查全部项目")
	}

	// 2. 拉取虚拟机
	vms, err := collectVMs(client, cfg, projects, allMode)
	if err != nil {
		fmt.Fprintf(os.Stderr, "获取虚拟机失败: %v\n", err)
		os.Exit(1)
	}
	logLine(fmt.Sprintf("共 %d 台服务器", len(vms)))
	if len(vms) == 0 {
		os.Exit(0)
	}

	// 阶段一：流水线中 target=local 且 run=once 的步骤只跑一次（如本地构建/打包），
	// 结果供每台机器复用；某步失败且 onError=stop 则全局终止，不再执行远端步骤。
	for i, step := range cfg.EffectiveSteps() {
		if !StepIsLocal(step) || StepRunMode(step) != "once" {
			continue
		}
		logLine("[流水线] 本地步骤: " + StepName(step, i))
	}
	onceResults, globalStopped := runPipelineOnce(cfg)
	for i, res := range onceResults {
		if res == nil {
			continue
		}
		logLine(fmt.Sprintf("[流水线] 本地步骤完成: %s -> %s（%s）", StepName(cfg.EffectiveSteps()[i], i), stepResultLabel(res), res.Duration))
	}

	// 3. SSH 登录测试 + 流水线步骤执行（并发，进度输出到 stderr）
	runSSHTests(cfg, vms, onceResults, globalStopped, nil, nil, false)

	// 4. 屏幕输出 + 可选导出（Excel，文件名自动生成: <配置名>_<时间戳>.xlsx）
	if err := outputTable(cfg, vms); err != nil {
		fmt.Fprintf(os.Stderr, "屏幕输出失败: %v\n", err)
	}
	if excelPath := autoExcelPath(profile, cfg); excelPath != "" {
		if err := exportExcel(excelPath, vms); err != nil {
			logLine(fmt.Sprintf("导出Excel失败: %v", err))
		} else {
			logLine("已导出Excel: " + excelPath)
		}
	}
}

// stopWebDaemon 停止后台 Web 实例：读取 web.pid，平台原生方式通知优雅退出。
func stopWebDaemon(settingsPath string) {
	path := pidFilePath(settingsPath)
	info, err := readPIDFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "未找到后台实例（%s 不存在），可能未运行或已退出\n", path)
		} else {
			fmt.Fprintf(os.Stderr, "读取 %s 失败: %v\n", path, err)
		}
		os.Exit(1)
	}
	if !processAlive(info.PID) {
		fmt.Fprintf(os.Stderr, "后台实例 PID=%d 已不存在（可能是残留的 PID 文件），已清理 %s\n", info.PID, path)
		removePIDFile(path)
		os.Exit(0)
	}
	// 展示实例详情，确认停对对象
	fmt.Fprintf(os.Stderr, "已找到后台实例:\n")
	fmt.Fprintf(os.Stderr, "  进程 PID: %d\n", info.PID)
	if info.Addr != "" {
		fmt.Fprintf(os.Stderr, "  监听地址: http://%s\n", info.Addr)
	}
	if info.LogFile != "" {
		fmt.Fprintf(os.Stderr, "  日志文件: %s\n", info.LogFile)
	}
	fmt.Fprintf(os.Stderr, "正在停止 ...\n")
	if err := requestShutdown(info); err != nil {
		fmt.Fprintf(os.Stderr, "停止失败: %v\n", err)
		os.Exit(1)
	}
	// 等待进程退出（最多 5s）
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && processAlive(info.PID) {
		time.Sleep(100 * time.Millisecond)
	}
	if processAlive(info.PID) {
		fmt.Fprintf(os.Stderr, "已发送退出信号，但进程仍在运行（等待稍后自行退出，或手动结束 PID=%d）\n", info.PID)
		os.Exit(1)
	}
	removePIDFile(path)
	fmt.Fprintf(os.Stderr, "已停止（%s 已清理）\n", path)
}

// initExampleConfig 生成示例配置文件到指定路径；已存在则不覆盖（返回错误提示）。
func initExampleConfig(path string) error {
	if fileExists(path) {
		return fmt.Errorf("文件已存在: %s（为避免覆盖现有配置，请换一个路径）", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(exampleConfigYAML), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "已生成示例配置: %s\n", path)
	fmt.Fprintf(os.Stderr, "请编辑其中的 endpoint / accessKeyId / accessKeySecret / regionId / project 后使用\n")
	return nil
}

// collectVMs 拉取全部服务器（按 resource.type 支持 ECS/BMS/全部），按项目过滤，补充密码、MAC、磁盘、项目名
func collectVMs(c *Client, cfg *Config, projects []*Project, allMode bool) ([]*VM, error) {
	projectSet := map[string]bool{}
	for _, p := range projects {
		projectSet[p.ID] = true
	}
	pageSize := cfg.Pagination.PageSize

	// 2.1 按资源类型拉取 ECS / 裸金属（各自在取密码/详情前做 IP 过滤，省请求）
	var vms []*VM
	var dropped int
	switch cfg.Resource.Type {
	case "bms":
		bms, d, err := collectBMS(c, cfg, projectSet, allMode, pageSize)
		if err != nil {
			return nil, err
		}
		dropped += d
		vms = append(vms, bms...)
	case "all":
		ecs, d, err := collectECS(c, cfg, projectSet, allMode, pageSize)
		if err != nil {
			return nil, err
		}
		dropped += d
		bms, d, err := collectBMS(c, cfg, projectSet, allMode, pageSize)
		if err != nil {
			return nil, err
		}
		dropped += d
		vms = append(vms, ecs...)
		vms = append(vms, bms...)
	default: // ecs
		ecs, d, err := collectECS(c, cfg, projectSet, allMode, pageSize)
		if err != nil {
			return nil, err
		}
		dropped += d
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
			logLine(fmt.Sprintf("警告: DescribeEnis 第%d页失败: %v（MAC 可能缺失）", eniPage, err))
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

	// 2.5 IP 筛选统计（配置了 filter 才提示）
	if filterConfigured(&cfg.Filter) {
		logLine(fmt.Sprintf("IP筛选: 共拉取 %d 台，过滤掉 %d 台，保留 %d 台执行", dropped+len(vms), dropped, len(vms)))
	}

	return vms, nil
}

// collectECS 拉取弹性云主机列表并按项目/IP 过滤，逐台取密码（过滤后的主机不取密码）
func collectECS(c *Client, cfg *Config, projectSet map[string]bool, allMode bool, pageSize int) ([]*VM, int, error) {
	var vms []*VM
	page := 1
	for {
		resp, err := c.describeEcs(cfg.RegionID, page, pageSize)
		if err != nil {
			return nil, 0, fmt.Errorf("DescribeEcs 第%d页失败: %w", page, err)
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
	// IP 筛选：只保留要执行的主机，后续取密码请求只为保留的主机发起
	vms, dropped, err := filterVMs(cfg, vms)
	if err != nil {
		return nil, 0, err
	}
	// 并发获取密码（带进度提示）
	fetchPasswords(c, cfg, vms, "获取虚拟机密码", func(id string) (string, error) {
		return c.getEcsPassword(cfg.RegionID, id)
	})
	return vms, dropped, nil
}

// fetchPasswords 并发获取密码，带进度提示
func fetchPasswords(c *Client, cfg *Config, vms []*VM, label string, get func(id string) (string, error)) {
	total := len(vms)
	if total == 0 {
		return
	}
	logLine(fmt.Sprintf("开始%s（共 %d 台，并发 %d）", label, total, cfg.HTTPWorkers()))
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
					logLine(fmt.Sprintf("警告: 获取 %s 密码失败: %v", vms[idx].Name, err))
				} else {
					vms[idx].Password = pw
				}
				n := atomic.AddInt64(&done, 1)
				fmt.Fprintf(os.Stderr, "\r%s: %d/%d", label, n, total)
				if runLog != nil {
					runLog(fmt.Sprintf("%s: %d/%d", label, n, total))
				}
			}
		}()
	}
	for i := range vms {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	fmt.Fprintln(os.Stderr)
	if runLog != nil {
		runLog(fmt.Sprintf("%s完成: %d/%d", label, total, total))
	}
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
					logLine(fmt.Sprintf("警告: 获取 %s 详情失败: %v", vm.Name, err))
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
				if runLog != nil {
					runLog(fmt.Sprintf("获取裸金属详情: %d/%d", n, total))
				}
			}
		}()
	}
	for i := range vms {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	fmt.Fprintln(os.Stderr)
	if runLog != nil {
		runLog(fmt.Sprintf("获取裸金属详情完成: %d/%d", total, total))
	}
}

// collectBMS 拉取裸金属列表并按项目/IP 过滤，逐台取密码与详情（结构化数据盘；过滤后的主机不取）
func collectBMS(c *Client, cfg *Config, projectSet map[string]bool, allMode bool, pageSize int) ([]*VM, int, error) {
	var vms []*VM
	page := 1
	for {
		resp, err := c.describeBms(cfg.RegionID, page, pageSize)
		if err != nil {
			return nil, 0, fmt.Errorf("DescribeBms 第%d页失败: %w", page, err)
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
	// IP 筛选：只保留要执行的主机，后续取密码/详情请求只为保留的主机发起
	vms, dropped, err := filterVMs(cfg, vms)
	if err != nil {
		return nil, 0, err
	}
	// 并发获取密码与详情（带进度提示）
	fetchPasswords(c, cfg, vms, "获取裸金属密码", func(id string) (string, error) {
		return c.getBmsPassword(cfg.RegionID, id)
	})
	fetchBmsDetails(c, cfg, vms)
	return vms, dropped, nil
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
