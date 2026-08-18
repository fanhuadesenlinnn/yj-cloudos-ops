package main

import (
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/xuri/excelize/v2"
)

// ---------- 屏幕输出：一个虚拟机一行 ----------

func outputTable(cfg *Config, vms []*VM) error {
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "序号\t名称\t内网IP\tEIP\tMAC\t规格\t系统盘\t数据盘\t项目\t密码\tSSH登录\t运行状态")
	for i, vm := range vms {
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			i+1,
			orDash(vm.Name),
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
		)
	}
	return w.Flush()
}

// statusStr 服务器运行状态摘要（一行展示 CPU/内存/磁盘/负载）
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
		return "—"
	}
	parts := make([]string, 0, len(vm.DataDisks))
	for _, d := range vm.DataDisks {
		t := diskShortType(d)
		if d.Name != "" {
			if t == "" {
				parts = append(parts, fmt.Sprintf("%s:%dG", d.Name, d.Size))
			} else {
				parts = append(parts, fmt.Sprintf("%s:%dG/%s", d.Name, d.Size, t))
			}
		} else {
			if t == "" {
				parts = append(parts, fmt.Sprintf("%dG", d.Size))
			} else {
				parts = append(parts, fmt.Sprintf("%dG/%s", d.Size, t))
			}
		}
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

func exportCSV(cfg *Config, vms []*VM) error {
	f, err := os.Create(cfg.Output.CSVPath)
	if err != nil {
		return err
	}
	defer f.Close()

	// UTF-8 BOM，便于 Excel 直接打开中文不乱码
	if _, err := f.WriteString("\xEF\xBB\xBF"); err != nil {
		return err
	}
	w := csv.NewWriter(f)
	defer w.Flush()

	header := []string{"序号", "虚拟机名称", "实例ID", "内网IP", "EIP", "MAC", "状态",
		"规格编码", "规格描述", "CPU核数", "内存G", "系统盘大小G", "系统盘类型",
		"数据盘", "项目名称", "项目ID", "root密码", "SSH登录结果",
		"操作系统", "内核版本", "运行时长", "负载(1/5/15)",
		"CPU使用率%", "内存总量", "内存已用", "内存使用率%",
		"根分区总量", "根分区已用", "根分区使用率%"}
	if err := w.Write(header); err != nil {
		return err
	}
	for i, vm := range vms {
		row := []string{
			itoa(i + 1),
			vm.Name,
			vm.ID,
			vm.IP,
			vm.EIP,
			vm.MAC,
			vm.Status,
			vm.SpecCode,
			vm.SpecName,
			itoa(vm.CPU),
			itoa(vm.Memory),
			itoa(vm.SysDisk.Size),
			diskShortType(vm.SysDisk),
			dataDiskStr(vm),
			vm.ProjectName,
			vm.ProjectID,
			vm.Password,
			vm.SSHResult,
		}
		row = append(row, statusCSVFields(vm)...)
		if err := w.Write(row); err != nil {
			return err
		}
	}
	return nil
}

// statusCSVFields 服务器运行状态明细列
func statusCSVFields(vm *VM) []string {
	s := vm.ServerStatus
	if s == nil {
		return []string{"", "", "", "", "", "", "", "", "", "", ""}
	}
	return []string{s.OS, s.Kernel, s.Uptime, s.LoadAvg,
		s.CPUUsed, s.MemTotal, s.MemUsed, s.MemUsedPct,
		s.DiskTotal, s.DiskUsed, s.DiskUsePct}
}

// ---------- 导出 Excel（配置了路径才导出） ----------

func exportExcel(cfg *Config, vms []*VM) error {
	if err := os.MkdirAll(filepath.Dir(cfg.Output.ExcelPath), 0o755); err != nil {
		return err
	}
	f := excelize.NewFile()
	defer f.Close()
	sheet := "虚拟机清单"
	f.SetSheetName("Sheet1", sheet)

	header := []string{"序号", "虚拟机名称", "实例ID", "内网IP", "EIP", "MAC", "状态",
		"规格编码", "规格描述", "CPU核数", "内存G", "系统盘大小G", "系统盘类型",
		"数据盘", "项目名称", "项目ID", "root密码", "SSH登录结果"}
	style, _ := f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true}})
	writeHeader(f, sheet, header, style)

	for i, vm := range vms {
		row := []any{
			i + 1,
			vm.Name,
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
			dataDiskStr(vm),
			vm.ProjectName,
			vm.ProjectID,
			vm.Password,
			vm.SSHResult,
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

	if err := f.SaveAs(cfg.Output.ExcelPath); err != nil {
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
