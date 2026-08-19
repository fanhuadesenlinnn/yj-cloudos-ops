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
	fmt.Fprintln(w, "序号\t名称\t类型\t内网IP\tEIP\tMAC\t规格\t系统盘\t数据盘\t项目\t密码\tSSH登录\t运行状态\t服务状态\t文件上传\t脚本执行")
	for i, vm := range vms {
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
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
			uploadStr(vm),
			scriptStr(vm),
		)
	}
	return w.Flush()
}

// uploadStr 文件上传摘要（屏幕/CSV/虚拟机清单展示）
func uploadStr(vm *VM) string {
	ups := vm.Uploads
	if len(ups) == 0 {
		return "—"
	}
	ok, skip, fail := 0, 0, 0
	for _, u := range ups {
		if u == nil {
			continue
		}
		switch u.State {
		case "success":
			ok++
		case "skipped":
			skip++
		default:
			fail++
		}
	}
	switch {
	case fail > 0:
		return fmt.Sprintf("✗ %d失败", fail)
	case skip > 0:
		return fmt.Sprintf("✓ %d/%d(跳过%d)", ok, len(ups), skip)
	default:
		return fmt.Sprintf("✓ %d/%d", ok, len(ups))
	}
}

// uploadResultLabel 单条上传结果中文说明（Excel 明细用）
func uploadResultLabel(u *UploadResult) string {
	if u == nil {
		return ""
	}
	switch u.State {
	case "success":
		if u.Overwritten {
			return "已覆盖"
		}
		return "上传成功"
	case "skipped":
		return "跳过(已存在)"
	case "error":
		return "失败"
	default:
		return u.State
	}
}

// scriptStr 脚本执行摘要（屏幕展示；按 State 分类，错误信息过长时截断）
func scriptStr(vm *VM) string {
	s := vm.Script
	if s == nil {
		return "—"
	}
	switch s.State {
	case "success":
		return "✓ 成功"
	case "fail":
		return fmt.Sprintf("✗ 失败(exit %d)", s.ExitCode)
	case "timeout":
		return "✗ 超时"
	case "interrupted":
		return "✗ 会话中断(疑似关机/重启)"
	case "error":
		return "✗ " + truncate(s.Error, 30)
	default: // 兼容未设置 State 的旧结果
		if s.OK {
			return "✓ 成功"
		}
		if s.Error != "" {
			return "✗ " + truncate(s.Error, 30)
		}
		return fmt.Sprintf("✗ 失败(exit %d)", s.ExitCode)
	}
}

// scriptResultLabel 脚本执行结果中文说明（Excel/落盘用，不截断）
func scriptResultLabel(s *ScriptResult) string {
	if s == nil {
		return "未配置脚本"
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
	case "error":
		return "未执行: " + s.Error
	default:
		if s.OK {
			return "成功"
		}
		return fmt.Sprintf("失败(exit %d)", s.ExitCode)
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

	header := []string{"序号", "虚拟机名称", "类型", "实例ID", "内网IP", "EIP", "MAC", "状态",
		"规格编码", "规格描述", "CPU核数", "内存G", "系统盘大小G", "系统盘类型", "系统盘描述",
		"数据盘", "项目名称", "项目ID", "root密码", "SSH登录结果", "服务状态", "文件上传", "脚本执行",
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
			vm.Type,
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
			vm.SysDiskDesc,
			dataDiskStr(vm),
			vm.ProjectName,
			vm.ProjectID,
			vm.Password,
			vm.SSHResult,
			servicesStr(vm),
			uploadStr(vm),
			scriptStr(vm),
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

	header := []string{"序号", "虚拟机名称", "类型", "实例ID", "内网IP", "EIP", "MAC", "状态",
		"规格编码", "规格描述", "CPU核数", "内存G", "系统盘大小G", "系统盘类型", "系统盘描述",
		"数据盘", "项目名称", "项目ID", "root密码", "SSH登录结果", "文件上传", "脚本执行"}
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
			uploadStr(vm),
			scriptStr(vm),
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

	// 脚本执行结果表（每台虚拟机一行，脚本输出存全文；超限截断时输出自带截断标注）
	scrSheet := "脚本执行结果"
	f.NewSheet(scrSheet)
	scrHeader := []string{"序号", "虚拟机名称", "内网IP", "项目名称", "SSH登录结果",
		"脚本执行结果", "退出码", "错误信息", "脚本输出"}
	writeHeader(f, scrSheet, scrHeader, style)
	scrRow := 2
	for i, vm := range vms {
		res, exit, errMsg, out := "未配置脚本", "", "", ""
		if s := vm.Script; s != nil {
			res = scriptResultLabel(s)
			exit = itoa(s.ExitCode)
			errMsg = s.Error
			out = s.Output
		}
		row := []any{i + 1, vm.Name, vm.IP, vm.ProjectName, vm.SSHResult, res, exit, errMsg, out}
		for j, v := range row {
			cell, _ := excelize.CoordinatesToCellName(j+1, scrRow)
			f.SetCellValue(scrSheet, cell, v)
		}
		scrRow++
	}
	if err := f.AutoFilter(scrSheet, "A1:"+cellName(len(scrHeader), scrRow-1), nil); err != nil {
		return err
	}

	// 文件上传结果表（每台虚拟机每个文件一行，含覆盖/跳过/失败明细）
	upSheet := "文件上传结果"
	f.NewSheet(upSheet)
	upHeader := []string{"序号", "虚拟机名称", "内网IP", "项目名称", "SSH登录结果",
		"本地路径", "远端路径", "结果", "是否覆盖", "权限", "错误信息"}
	writeHeader(f, upSheet, upHeader, style)
	upRow := 2
	for i, vm := range vms {
		if len(vm.Uploads) == 0 {
			row := []any{i + 1, vm.Name, vm.IP, vm.ProjectName, vm.SSHResult, "", "", "", "", "", ""}
			for j, v := range row {
				cell, _ := excelize.CoordinatesToCellName(j+1, upRow)
				f.SetCellValue(upSheet, cell, v)
			}
			upRow++
			continue
		}
		for _, u := range vm.Uploads {
			if u == nil {
				continue
			}
			over := "否"
			if u.Overwritten {
				over = "是"
			}
			row := []any{i + 1, vm.Name, vm.IP, vm.ProjectName, vm.SSHResult,
				u.Local, u.Remote, uploadResultLabel(u), over, u.Mode, u.Error}
			for j, v := range row {
				cell, _ := excelize.CoordinatesToCellName(j+1, upRow)
				f.SetCellValue(upSheet, cell, v)
			}
			upRow++
		}
	}
	if err := f.AutoFilter(upSheet, "A1:"+cellName(len(upHeader), upRow-1), nil); err != nil {
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
