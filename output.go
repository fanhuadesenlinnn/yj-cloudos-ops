package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/xuri/excelize/v2"
)

// ---------- 屏幕输出：一个虚拟机一行 ----------

func outputTable(cfg *Config, vms []*VM) error {
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "序号\t名称\t类型\t内网IP\tEIP\tMAC\t规格\t系统盘\t数据盘\t项目\t密码\tSSH登录\t运行状态\t服务状态\t流水线")
	for i, vm := range vms {
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			i+1,
			orDash(vm.Name),
			orDash(vm.Type),
			orDash(vm.IP),
			orDash(vm.EIP),
			orDash(vm.MAC),
			specStr(vm),
			sysDiskStr(vm),
			dataDiskStr(vm),
			orDash(vm.ProjectName),
			pwStr(cfg, vm),
			vm.SSHResult,
			statusStr(vm),
			servicesStr(vm),
			pipelineStr(vm),
		)
	}
	return w.Flush()
}

// pipelineStr 流水线执行摘要（屏幕/CSV/虚拟机清单展示）：每步“序号+状态符号”，如 “1✓ 2✗ 3✓”
func pipelineStr(vm *VM) string {
	steps := vm.ExecSteps
	if len(steps) == 0 {
		return "—"
	}
	parts := make([]string, 0, len(steps))
	for i, s := range steps {
		if s == nil {
			continue
		}
		parts = append(parts, fmt.Sprintf("%d%s", i+1, stepSymbol(s)))
	}
	if len(parts) == 0 {
		return "—"
	}
	return strings.Join(parts, " ")
}

// stepSymbol 步骤状态符号（屏幕摘要用）
func stepSymbol(s *ExecStepResult) string {
	switch s.State {
	case "success":
		return "✓"
	case "fail":
		return "✗"
	case "timeout":
		return "超"
	case "interrupted":
		return "断"
	case "skipped":
		return "-"
	default: // error
		return "!"
	}
}

// stepResultLabel 单个流水线步骤结果中文说明（Excel/落盘用，不截断）
func stepResultLabel(s *ExecStepResult) string {
	if s == nil {
		return "未执行"
	}
	switch s.State {
	case "success":
		return "成功"
	case "fail":
		return fmt.Sprintf("失败(exit %d)", s.ExitCode)
	case "timeout":
		return "超时"
	case "interrupted":
		return "会话中断(疑似关机/重启)"
	case "skipped":
		return "未执行(上游失败)"
	case "error":
		return "未执行: " + s.Error
	default:
		return s.State
	}
}

// servicesStr 服务状态摘要（每服务一行内展示: sshd:运行中 crond:停止）
func servicesStr(vm *VM) string {
	if len(vm.Services) == 0 {
		return "—"
	}
	parts := make([]string, 0, len(vm.Services))
	for _, svc := range vm.Services {
		parts = append(parts, svc.Name+":"+serviceStateLabel(svc.State))
	}
	return strings.Join(parts, " ")
}

// serviceStateLabel 服务状态中文说明
func serviceStateLabel(state string) string {
	switch state {
	case "active", "activating":
		return "运行中"
	case "inactive", "deactivating":
		return "停止"
	case "failed":
		return "异常"
	case "not-found":
		return "不存在"
	case "unknown":
		return "未知"
	default:
		return state
	}
}
func statusStr(vm *VM) string {
	s := vm.ServerStatus
	if s == nil {
		return "—"
	}
	var parts []string
	if s.CPUUsed != "" {
		parts = append(parts, "CPU "+s.CPUUsed+"%")
	}
	if s.MemUsedPct != "" {
		parts = append(parts, "内存 "+s.MemUsedPct+"%")
	}
	if s.DiskUsePct != "" {
		parts = append(parts, "磁盘 "+s.DiskUsePct+"%")
	}
	if s.LoadAvg != "" {
		parts = append(parts, "负载 "+s.LoadAvg)
	}
	if len(parts) == 0 {
		return "✓ 已连接"
	}
	return strings.Join(parts, " ")
}

func specStr(vm *VM) string {
	if vm.CPU > 0 && vm.Memory > 0 {
		return fmt.Sprintf("%d核%dG", vm.CPU, vm.Memory)
	}
	if vm.SpecName != "" {
		return vm.SpecName
	}
	if vm.SpecCode != "" {
		return vm.SpecCode
	}
	return "—"
}

func sysDiskStr(vm *VM) string {
	if vm.SysDiskDesc != "" {
		return vm.SysDiskDesc // 裸金属等：字符串描述，如 "600G HDD*2"
	}
	if vm.SysDisk.Size <= 0 {
		return "—"
	}
	t := diskShortType(vm.SysDisk)
	if t == "" {
		return fmt.Sprintf("%dG", vm.SysDisk.Size)
	}
	return fmt.Sprintf("%dG/%s", vm.SysDisk.Size, t)
}

func dataDiskStr(vm *VM) string {
	if len(vm.DataDisks) == 0 {
		if vm.DataDiskDesc != "" {
			return vm.DataDiskDesc // 裸金属等：字符串描述
		}
		return "—"
	}
	parts := make([]string, 0, len(vm.DataDisks))
	for _, d := range vm.DataDisks {
		t := diskShortType(d)
		size := fmt.Sprintf("%dG", d.Size)
		if d.SizeDesc != "" {
			size = d.SizeDesc
		} else if d.Size <= 0 {
			size = ""
		}
		name := d.Name
		var one string
		switch {
		case name != "" && t != "" && size != "":
			one = fmt.Sprintf("%s:%s/%s", name, size, t)
		case name != "" && size != "":
			one = fmt.Sprintf("%s:%s", name, size)
		case size != "" && t != "":
			one = fmt.Sprintf("%s/%s", size, t)
		case size != "":
			one = size
		case t != "":
			one = t
		default:
			one = "?"
		}
		parts = append(parts, one)
	}
	return strings.Join(parts, " + ")
}

// diskShortType 优先规格名称，其次去掉 ebs. 前缀的规格编码，再次 diskType
func diskShortType(d Disk) string {
	if d.SpecName != "" {
		return d.SpecName
	}
	if d.SpecCode != "" {
		return strings.TrimPrefix(d.SpecCode, "ebs.")
	}
	if d.Type != "" {
		return d.Type
	}
	return ""
}

func pwStr(cfg *Config, vm *VM) string {
	if !cfg.Output.ShowPassword {
		return "******"
	}
	if vm.Password == "" {
		return "—"
	}
	return vm.Password
}

// ---------- 区域列表输出（-list-regions） ----------

func printRegions(client *Client) error {
	regions, err := client.getRegions("VM")
	if err != nil {
		return err
	}
	if len(regions) == 0 {
		fmt.Println("未查询到可用区域")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "区域ID\t区域名称\t可用区")
	for _, r := range regions {
		var azs []string
		for _, a := range r.AzList {
			azs = append(azs, fmt.Sprintf("%s(%s)", orDash(a.AzID), orDash(a.AzName)))
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", orDash(r.RegionID), orDash(r.RegionName), strings.Join(azs, ", "))
	}
	return w.Flush()
}

// ---------- 项目列表输出（-list-projects） ----------

func printProjects(client *Client) error {
	projects, err := client.getProjectList()
	if err != nil {
		return err
	}
	if len(projects) == 0 {
		fmt.Println("未查询到可用项目")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "项目ID\t项目名称\t类型\t启用\t描述")
	for _, p := range projects {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			orDash(p.ID), orDash(p.Name), orDash(p.TypeName), enabledStr(p.Enabled), orDash(p.Description))
	}
	return w.Flush()
}

// ---------- 导出 CSV（配置了路径才导出） ----------

// ---------- 导出 Excel（配置了 output.dir 才导出） ----------

// autoExcelPath 生成导出文件路径：<output.dir>/<配置名>_<时间戳>.xlsx；
// 同秒撞名时自动追加序号避免覆盖；dir 为空返回空串（不导出）。
func autoExcelPath(profile string, cfg *Config) string {
	dir := cfg.Output.Dir
	if dir == "" {
		return ""
	}
	ts := time.Now().Format("20060102_150405")
	base := sanitizeFileName(profile) + "_" + ts
	path := filepath.Join(dir, base+".xlsx")
	for i := 1; fileExists(path); i++ {
		path = filepath.Join(dir, fmt.Sprintf("%s_%d.xlsx", base, i))
	}
	return path
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func exportExcel(path string, vms []*VM) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f := excelize.NewFile()
	defer f.Close()
	sheet := "虚拟机清单"
	f.SetSheetName("Sheet1", sheet)

	header := []string{"序号", "虚拟机名称", "类型", "实例ID", "内网IP", "EIP", "MAC", "状态",
		"规格编码", "规格描述", "CPU核数", "内存G", "系统盘大小G", "系统盘类型", "系统盘描述",
		"数据盘", "项目名称", "项目ID", "root密码", "SSH登录结果", "流水线"}
	style, _ := f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true}})
	writeHeader(f, sheet, header, style)

	for i, vm := range vms {
		row := []any{
			i + 1,
			vm.Name,
			vm.Type,
			vm.ID,
			vm.IP,
			vm.EIP,
			vm.MAC,
			vm.Status,
			vm.SpecCode,
			vm.SpecName,
			vm.CPU,
			vm.Memory,
			vm.SysDisk.Size,
			diskShortType(vm.SysDisk),
			vm.SysDiskDesc,
			dataDiskStr(vm),
			vm.ProjectName,
			vm.ProjectID,
			vm.Password,
			vm.SSHResult,
			pipelineStr(vm),
		}
		for j, v := range row {
			cell, _ := excelize.CoordinatesToCellName(j+1, i+2)
			f.SetCellValue(sheet, cell, v)
		}
	}
	if err := f.AutoFilter(sheet, "A1:"+cellName(len(header), len(vms)+1), nil); err != nil {
		return err
	}

	// 服务器运行状态表
	statusSheet := "服务器运行状态"
	f.NewSheet(statusSheet)
	statusHeader := []string{"序号", "虚拟机名称", "内网IP", "项目名称", "SSH登录结果",
		"操作系统", "内核版本", "运行时长", "负载(1/5/15)",
		"CPU使用率%", "内存总量", "内存已用", "内存使用率%",
		"根分区总量", "根分区已用", "根分区使用率%"}
	writeHeader(f, statusSheet, statusHeader, style)
	rowIdx := 2
	for i, vm := range vms {
		s := vm.ServerStatus
		row := []any{i + 1, vm.Name, vm.IP, vm.ProjectName, vm.SSHResult}
		if s == nil {
			row = append(row, make([]any, 11)...)
		} else {
			row = append(row, s.OS, s.Kernel, s.Uptime, s.LoadAvg,
				s.CPUUsed, s.MemTotal, s.MemUsed, s.MemUsedPct,
				s.DiskTotal, s.DiskUsed, s.DiskUsePct)
		}
		for j, v := range row {
			cell, _ := excelize.CoordinatesToCellName(j+1, rowIdx)
			f.SetCellValue(statusSheet, cell, v)
		}
		rowIdx++
	}
	if err := f.AutoFilter(statusSheet, "A1:"+cellName(len(statusHeader), rowIdx-1), nil); err != nil {
		return err
	}

	// 服务运行状态表（每台虚拟机每个服务一行）
	svcSheet := "服务运行状态"
	f.NewSheet(svcSheet)
	svcHeader := []string{"序号", "虚拟机名称", "内网IP", "项目名称", "SSH登录结果", "服务名", "状态", "状态说明"}
	writeHeader(f, svcSheet, svcHeader, style)
	svcRow := 2
	for i, vm := range vms {
		if len(vm.Services) == 0 {
			row := []any{i + 1, vm.Name, vm.IP, vm.ProjectName, vm.SSHResult, "", "", ""}
			for j, v := range row {
				cell, _ := excelize.CoordinatesToCellName(j+1, svcRow)
				f.SetCellValue(svcSheet, cell, v)
			}
			svcRow++
			continue
		}
		for _, svc := range vm.Services {
			row := []any{i + 1, vm.Name, vm.IP, vm.ProjectName, vm.SSHResult,
				svc.Name, svc.State, serviceStateLabel(svc.State)}
			for j, v := range row {
				cell, _ := excelize.CoordinatesToCellName(j+1, svcRow)
				f.SetCellValue(svcSheet, cell, v)
			}
			svcRow++
		}
	}
	if err := f.AutoFilter(svcSheet, "A1:"+cellName(len(svcHeader), svcRow-1), nil); err != nil {
		return err
	}

	// 流水线执行结果表（每台虚拟机每个步骤一行，含输出/退出码/耗时；输出超限截断时自带截断标注）
	plSheet := "流水线执行结果"
	f.NewSheet(plSheet)
	plHeader := []string{"序号", "虚拟机名称", "内网IP", "项目名称", "SSH登录结果",
		"步骤名", "类型", "目标", "结果", "退出码", "耗时", "错误信息", "输出"}
	writeHeader(f, plSheet, plHeader, style)
	plRow := 2
	for i, vm := range vms {
		if len(vm.ExecSteps) == 0 {
			row := []any{i + 1, vm.Name, vm.IP, vm.ProjectName, vm.SSHResult, "", "", "", "", "", "", "", ""}
			for j, v := range row {
				cell, _ := excelize.CoordinatesToCellName(j+1, plRow)
				f.SetCellValue(plSheet, cell, v)
			}
			plRow++
			continue
		}
		for _, s := range vm.ExecSteps {
			if s == nil {
				continue
			}
			row := []any{i + 1, vm.Name, vm.IP, vm.ProjectName, vm.SSHResult,
				s.Name, s.Type, s.Target, stepResultLabel(s), itoa(s.ExitCode), s.Duration, s.Error, s.Output}
			for j, v := range row {
				cell, _ := excelize.CoordinatesToCellName(j+1, plRow)
				f.SetCellValue(plSheet, cell, v)
			}
			plRow++
		}
	}
	if err := f.AutoFilter(plSheet, "A1:"+cellName(len(plHeader), plRow-1), nil); err != nil {
		return err
	}

	if err := f.SaveAs(path); err != nil {
		return err
	}
	return nil
}

// writeHeader 写入表头并加粗
func writeHeader(f *excelize.File, sheet string, header []string, style int) {
	for j, h := range header {
		cell, _ := excelize.CoordinatesToCellName(j+1, 1)
		f.SetCellValue(sheet, cell, h)
	}
	f.SetCellStyle(sheet, "A1", cellName(len(header), 1), style)
}

func cellName(col, row int) string {
	name, _ := excelize.CoordinatesToCellName(col, row)
	return name
}
