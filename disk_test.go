package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 回归测试：使用真实平台返回的 DescribeDisks 数据
// 文档写 total 为 String，实际返回数字 72；diskSize 是字符串 "1000"；attachInfos 含实例挂载关系
const realDescribeDisks = `{"current":1,"size":100,"total":72,"pages":1,"records":[
{"shareable":false,"azoneName":"北露园","regionName":"药监局私有云","chargeType":1,
 "diskSize":"1000","payType":"DAY_MONTH","expired":false,"recycle":0,
 "diskType":"DATA_DISK","dualactive":"0","instanceLabel":1,
 "specificationName":"AG区云硬盘","diskName":"EBS117901324","disasterType":"normal",
 "instanceEndTime":0,"updateTime":1787024211000,"instanceStartTime":1787024325000,
 "userId":"5665fc96-3f9d-438c-bdee-2cf4228fbb75","azoneId":"dc-1fhmpju10jew-az1",
 "regionId":"dc-1fhmpju10jew","createTime":1787024211000,
 "realDiskId":"ebsbp078cdf3r2j8zs8jfrvhvqy7","storageType":"onestor",
 "diskId":"ebs-wwjn5bfs8t1x","productConf":"{\"projectId\":\"2977066116050118669\"}",
 "specificationCode":"ag.ones.hdd",
 "attachInfos":[{"canCreateSnapshots":true,"sourceId":"ebs-wwjn5bfs8t1x",
   "instanceId":"ecs-u8v8o8ahvyv8","redirectInstance":true,
   "name":"prod_国家药监云一体化管理平台-数据库服务器","type":"ECS",
   "ecsDisasterType":"normal","projectId":"2977066116050118669"}],
 "projectName":"国家药监云一体化管理平台","projectId":"2977066116050118669","status":"In-use"}
],"RequestId":"ede9cda2-3094-436e-b106-96b9cf525705"}`

func TestDescribeDisksRealData(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(realDescribeDisks))
	}))
	defer srv.Close()

	c := &Client{
		endpoint:   strings.TrimRight(srv.URL, "/"),
		ak:         "ak",
		sk:         "sk",
		regionID:   "dc-1fhmpju10jew",
		httpClient: srv.Client(),
	}
	resp, err := c.describeDisks("dc-1fhmpju10jew", 1, 100)
	if err != nil {
		t.Fatalf("describeDisks 解析失败: %v", err)
	}
	if resp.Pages != 1 || len(resp.Records) != 1 {
		t.Fatalf("分页/记录数错误: %+v", resp)
	}
	r := resp.Records[0]
	if r.DiskID != "ebs-wwjn5bfs8t1x" {
		t.Fatalf("diskId 解析错误: %s", r.DiskID)
	}
	if toInt(r.DiskSize) != 1000 {
		t.Fatalf("diskSize 字符串解析错误: %v", r.DiskSize)
	}
	if r.ProjectName != "国家药监云一体化管理平台" {
		t.Fatalf("projectName 解析错误: %s", r.ProjectName)
	}
	if len(r.AttachInfos) != 1 || r.AttachInfos[0].InstanceID != "ecs-u8v8o8ahvyv8" {
		t.Fatalf("attachInfos 解析错误: %+v", r.AttachInfos)
	}
}

// 回归测试：使用真实平台返回的 DescribeEcs 数据（projectId 过滤依据）
const realDescribeEcs = `{"page":1,"size":100,"totalCount":121,"totalPages":1,"list":[
{"instanceId":"ecs-u8v8o8ahvyv8","instanceName":"prod_国家药监云一体化管理平台-数据库服务器",
 "sysDiskSize":60,"sysDiskCode":"ag.ones.hdd","sysDiskId":"sys-u8v8okl4iw9v","status":"RUNNING",
 "imageId":"kylin-linux-V10-sp3-anxin-mini-0vuln-202608180858","imageType":"linux",
 "imageCode":"image.public.image.init","imageParentCode":"ecs.image.public","description":"",
 "ip":"10.71.5.207","eipId":null,"eipName":null,"eipAddr":null,"eipSize":null,"eipCode":null,
 "instanceCode":"ag-iaas-16c32m","instanceCodeName":"ag-iaas-16c32m",
 "instanceSystem":"Kylin_Linux_Advanced_Server_V10_SP3(Lance)_64bit","payType":"DAY_MONTH",
 "startTime":1787024325000,"endTime":null,"bindDiskCount":1,"eniId":"eni-u8v8o8ahvyv9",
 "secondaryEni":null,"azoneId":"dc-1fhmpju10jew-az1","instanceCpu":16,"instanceMemory":32,
 "vpcId":"vpc-u8v8n4tvme8j","vpcCidr":null,"subnetId":"vsnet-u8v8n4tvme8k",
 "subnetCidr":"10.71.5.0/24","projectId":"2977066116050118669","disasterType":"normal",
 "regionId":"dc-1fhmpju10jew","maxDisk":15}
],"RequestId":"fd28f5b8-c3a1-4195-8ab0-bd061ade953a"}`

func TestDescribeEcsRealData(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(realDescribeEcs))
	}))
	defer srv.Close()

	c := &Client{
		endpoint:   strings.TrimRight(srv.URL, "/"),
		ak:         "ak",
		sk:         "sk",
		regionID:   "dc-1fhmpju10jew",
		httpClient: srv.Client(),
	}
	resp, err := c.describeEcs("dc-1fhmpju10jew", 1, 100)
	if err != nil {
		t.Fatalf("describeEcs 解析失败: %v", err)
	}
	item := resp.List[0]
	if item.ProjectID != "2977066116050118669" {
		t.Fatalf("projectId 解析错误: %s", item.ProjectID)
	}
	if toInt(item.InstanceCPU) != 16 || toInt(item.InstanceMemory) != 32 {
		t.Fatalf("CPU/内存解析错误: %v/%v", item.InstanceCPU, item.InstanceMemory)
	}
	if toInt(item.SysDiskSize) != 60 {
		t.Fatalf("sysDiskSize 解析错误: %v", item.SysDiskSize)
	}
}
