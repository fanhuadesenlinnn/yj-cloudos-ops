package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 用本地 mock 服务器验证 GetRegion 请求构造与响应解析
func TestGetRegions(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/product" {
			t.Errorf("路径错误: %s", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("Action") != "GetRegion" {
			t.Errorf("Action 错误: %s", q.Get("Action"))
		}
		if q.Get("ProductCode") != "VM" {
			t.Errorf("ProductCode 错误: %s", q.Get("ProductCode"))
		}
		if q.Get("Signature") == "" {
			t.Errorf("缺少 Signature 参数")
		}
		w.Write([]byte(`{"Data":[
			{"RegionId":"cn-beijing","RegionName":"华北2-北京2","AzList":[
				{"AzId":"cn-beijing-a","AzName":"北京-A"},
				{"AzId":"cn-beijing-b","AzName":"北京-B"}]},
			{"RegionId":"44b9743d-0r23","RegionName":"天津-真","AzList":[
				{"AzId":"cn-tianjin1-a","AzName":"天津1-A"}]}
		],"RequestId":"test-req"}`))
	}))
	defer srv.Close()

	c := &Client{
		endpoint:   strings.TrimRight(srv.URL, "/"),
		ak:         "test-ak",
		sk:         "test-sk",
		regionID:   "cn-beijing",
		httpClient: srv.Client(),
	}
	regions, err := c.getRegions("VM")
	if err != nil {
		t.Fatalf("getRegions 失败: %v", err)
	}
	if len(regions) != 2 {
		t.Fatalf("区域数量错误: %d", len(regions))
	}
	if regions[0].RegionID != "cn-beijing" || regions[0].RegionName != "华北2-北京2" {
		t.Fatalf("区域解析错误: %+v", regions[0])
	}
	if len(regions[0].AzList) != 2 || regions[0].AzList[1].AzID != "cn-beijing-b" {
		t.Fatalf("可用区解析错误: %+v", regions[0].AzList)
	}
}
