package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const realDescribeBms = `{"page":1,"size":100,"totalCount":1,"totalPages":1,"list":[
{"instanceId":"bms-kawkob3nq4wk","instanceName":"i-MTlrq5yLTJ","instanceCode":"physical.c2.8xlarge",
 "instanceCodeName":"通用II型","status":"RUNNING","ip":"10.1.0.13","eipId":null,"eipAddr":null,
 "sysDisk":"600G HDD*2","dataDisk":"1.92T SSD*2","startTime":1603185715000,"endTime":1605888000000,
 "azoneId":"cn-beijing-a","payType":"YEAR_MONTH","cpu":"Intel Xeon Gold 4110(2*8核/2.1GHz) 256G",
 "dataDisk2":"1.92T SSD*2","regionId":"cn-beijing","regionName":"华北2-北京2",
 "instanceCpu":16,"instanceMemory":256,"vpcId":"vpc-c8xc0kb4fdlv","vpcCidr":"192.168.0.0/16",
 "subnetId":"a53f710afd08414e95a61d18c78729c6","subnetCidr":"192.168.0.0/24",
 "projectId":"rg-c8xc0kb4fdly",
 "networkInterfaces":[{"ip":"10.1.0.13","eniId":"eni-kawkob3nq4wl","bond":"bond4"}]}
],"RequestId":"b9de1a62-d09e-4d34-9a61-38f0f56564b6"}`

func TestDescribeBmsRealData(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(realDescribeBms))
	}))
	defer srv.Close()

	c := &Client{
		endpoint:   strings.TrimRight(srv.URL, "/"),
		ak:         "ak",
		sk:         "sk",
		regionID:   "cn-beijing",
		httpClient: srv.Client(),
	}
	resp, err := c.describeBms("cn-beijing", 1, 100)
	if err != nil {
		t.Fatalf("describeBms 解析失败: %v", err)
	}
	item := resp.List[0]
	if item.InstanceID != "bms-kawkob3nq4wk" || item.ProjectID != "rg-c8xc0kb4fdly" {
		t.Fatalf("裸金属字段解析错误: %+v", item)
	}
	if toInt(item.InstanceCPU) != 16 || toInt(item.InstanceMemory) != 256 {
		t.Fatalf("CPU/内存解析错误: %v/%v", item.InstanceCPU, item.InstanceMemory)
	}
	if item.SysDisk != "600G HDD*2" || item.DataDisk != "1.92T SSD*2" {
		t.Fatalf("磁盘描述解析错误: %q / %q", item.SysDisk, item.DataDisk)
	}
	if len(item.NetworkInterfaces) != 1 || item.NetworkInterfaces[0].EniID != "eni-kawkob3nq4wl" {
		t.Fatalf("网卡解析错误: %+v", item.NetworkInterfaces)
	}
}

func TestBmsDiskSize(t *testing.T) {
	cases := []struct {
		in   any
		desc string
		size int
	}{
		{"100", "100G", 100},
		{100.0, "100G", 100},
		{"1.92T", "1.92T", 1966}, // 1.92*1024
		{"600G", "600G", 600},
		{"", "", 0},
		{nil, "", 0},
	}
	for _, c := range cases {
		desc, size := bmsDiskSize(c.in)
		if desc != c.desc || size != c.size {
			t.Errorf("bmsDiskSize(%v) = (%q,%d), want (%q,%d)", c.in, desc, size, c.desc, c.size)
		}
	}
}
