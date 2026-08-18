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

func outputTable(cfg *Config, project *Project, vms []*VM) error {
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "序号\t名称\t内网IP\tEIP\tMAC\t规格\t系统盘\t数据盘\t项目\t密码\tSSH登录")
	for i, vm := range vms {
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			i+1,
			orDash(vm.Name),
			orDash(vm.IP),
			orDash(vm.EIP),
			orDash(vm.MAC),
			specStr(vm),
			sysDiskStr(vm),
			dataDiskStr(vm),
			project.Name,
			pwStr(cfg, vm),
			vm.SSHResult,
		)
	}
	return w.Flush()
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

// ---------- 导出 CSV（配置了路径才导出） ----------

func exportCSV(cfg *Config, project *Project, vms []*VM) error {
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
		"数据盘", "项目名称", "项目ID", "root密码", "SSH登录结果"}
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
			project.Name,
			project.ID,
			vm.Password,
			vm.SSHResult,
		}
		if err := w.Write(row); err != nil {
			return err
		}
	}
	return nil
}

// ---------- 导出 Excel（配置了路径才导出） ----------

func exportExcel(cfg *Config, project *Project, vms []*VM) error {
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
	for j, h := range header {
		cell, _ := excelize.CoordinatesToCellName(j+1, 1)
		f.SetCellValue(sheet, cell, h)
	}
	// 表头加粗
	style, _ := f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true}})
	f.SetCellStyle(sheet, "A1", cellName(len(header), 1), style)

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
			project.Name,
			project.ID,
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
	if err := f.SaveAs(cfg.Output.ExcelPath); err != nil {
		return err
	}
	return nil
}

func cellName(col, row int) string {
	name, _ := excelize.CoordinatesToCellName(col, row)
	return name
}
