package main

import "testing"

// 模拟 statusCommand 输出（Kylin/OpenEuler 类系统）
const sampleStatusOutput = `===OS===
Kylin Linux Advanced Server V10 (SP3) (Lance)
===KERNEL===
4.19.90-24.4.v2101.ky10.x86_64
===UPTIME===
 09:33:05 up 178 days,  4:14,  2 users,  load average: 0.08, 0.05, 0.04
===CPU===
%Cpu(s):  3.2 us,  0.5 sy,  0.0 ni, 95.9 id,  0.3 wa,  0.0 hi,  0.0 si,  0.0 st
===MEM===
              total        used        free      shared  buff/cache   available
Mem:          31869       15302        1041         204      15525     15516
Swap:          8191           0        8191
===DISK===
Filesystem      Size  Used Avail Use% Mounted on
/dev/vda2       150G   45G   99G  32% /
tmpfs            16G     0   16G   0% /dev/shm
`

func TestParseStatus(t *testing.T) {
	st := parseStatus(sampleStatusOutput)
	if st.OS != "Kylin Linux Advanced Server V10 (SP3) (Lance)" {
		t.Errorf("OS解析错误: %q", st.OS)
	}
	if st.Kernel != "4.19.90-24.4.v2101.ky10.x86_64" {
		t.Errorf("内核解析错误: %q", st.Kernel)
	}
	if st.Uptime != "178 days,  4:14" {
		t.Errorf("运行时长解析错误: %q", st.Uptime)
	}
	if st.LoadAvg != "0.08, 0.05, 0.04" {
		t.Errorf("负载解析错误: %q", st.LoadAvg)
	}
	if st.CPUUsed != "4.1" { // 100 - 95.9
		t.Errorf("CPU使用率解析错误: %q", st.CPUUsed)
	}
	if st.MemTotal != "31.1G" || st.MemUsed != "14.9G" {
		t.Errorf("内存解析错误: total=%q used=%q", st.MemTotal, st.MemUsed)
	}
	if st.MemUsedPct != "48.0" { // 15302/31869*100
		t.Errorf("内存使用率解析错误: %q", st.MemUsedPct)
	}
	if st.DiskTotal != "150G" || st.DiskUsed != "45G" || st.DiskUsePct != "32" {
		t.Errorf("磁盘解析错误: %+v", st)
	}
}

func TestParseStatusEmpty(t *testing.T) {
	st := parseStatus("")
	if st.OS != "" || st.CPUUsed != "" || st.DiskUsePct != "" {
		t.Errorf("空输出应返回空状态: %+v", st)
	}
}
