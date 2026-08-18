package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// ---------- OpenAPI-V2 签名（HMAC-SHA1），编码规则严格按文档示例（Java版本） ----------

func percentEncode(s string) string {
	var sb strings.Builder
	for i := 0; i < len(s); i++ {
		b := s[i]
		if (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9') ||
			b == '-' || b == '_' || b == '.' || b == '~' {
			sb.WriteByte(b)
		} else {
			fmt.Fprintf(&sb, "%%%02X", b)
		}
	}
	return sb.String()
}

// sign 计算 OpenAPI-V2 签名
// 注意：文档示例中每个参数先编码值，拼成 key=value 后再整体编码一次（值被双重编码），
// 按 key 排序后以字面 "%26" 连接成规范化串，StringToSign = 方法&%2F&规范化串，
// HMAC-SHA1(key=Secret+"&")，Base64 后作为 Signature。
func sign(method string, params map[string]string, secret string) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	kvList := make([]string, 0, len(keys))
	for _, k := range keys {
		kvList = append(kvList, percentEncode(k+"="+percentEncode(params[k])))
	}
	canonical := strings.Join(kvList, "%26")
	stringToSign := method + "&%2F&" + canonical

	mac := hmac.New(sha1.New, []byte(secret+"&"))
	mac.Write([]byte(stringToSign))
	sig := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	return sig
}

// ---------- HTTP 客户端 ----------

type Client struct {
	endpoint   string
	ak         string
	sk         string
	regionID   string
	httpClient *http.Client
}

func newClient(cfg *Config) *Client {
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: cfg.InsecureSkipVerify}, // 支持跳过证书校验
	}
	return &Client{
		endpoint:   strings.TrimRight(cfg.Endpoint, "/"),
		ak:         cfg.AccessKeyID,
		sk:         cfg.AccessKeySecret,
		regionID:   cfg.RegionID,
		httpClient: &http.Client{Transport: tr, Timeout: cfg.HTTPTimeout()},
	}
}

func randomNonce() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (c *Client) doGET(path string, extra map[string]string, out interface{}) error {
	params := map[string]string{
		"Format":           "json",
		"AccessKeyId":      c.ak,
		"SignatureMethod":  "HMAC-SHA1",
		"SignatureVersion": "2.0",
		"Timestamp":        time.Now().UTC().Format("2006-01-02T15:04:05Z"),
		"SignatureNonce":   randomNonce(),
	}
	for k, v := range extra {
		params[k] = v
	}

	sig := sign("GET", params, c.sk)

	keys := make([]string, 0, len(params)+1)
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	q := make([]string, 0, len(keys)+1)
	for _, k := range keys {
		q = append(q, k+"="+percentEncode(params[k]))
	}
	q = append(q, "Signature="+percentEncode(sig))

	u := c.endpoint + path + "?" + strings.Join(q, "&")
	resp, err := c.httpClient.Get(u)
	if err != nil {
		return fmt.Errorf("请求失败(%s): %w", u, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("读取响应失败: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return apiError(resp.StatusCode, body)
	}
	if err := json.Unmarshal(body, out); err != nil {
		var e struct {
			Code    string `json:"Code"`
			Message string `json:"Message"`
			Msg     string `json:"Msg"`
		}
		_ = json.Unmarshal(body, &e)
		if e.Code != "" || e.Message != "" || e.Msg != "" {
			return fmt.Errorf("API错误: %s %s %s", e.Code, e.Message, e.Msg)
		}
		return fmt.Errorf("解析响应失败: %v; body=%s", err, truncate(string(body), 500))
	}
	return nil
}

func apiError(status int, body []byte) error {
	var e struct {
		Code    string `json:"Code"`
		Message string `json:"Message"`
		Msg     string `json:"Msg"`
	}
	_ = json.Unmarshal(body, &e)
	if e.Code != "" || e.Message != "" || e.Msg != "" {
		return fmt.Errorf("HTTP %d: API错误: %s %s %s", status, e.Code, e.Message, e.Msg)
	}
	return fmt.Errorf("HTTP %d: %s", status, truncate(string(body), 500))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// ---------- 数据模型 ----------

type Project struct {
	ID          string
	Name        string
	TypeName    string
	Enabled     int
	Description string
	CreateTime  string // 尽力而为：文档返回字段未列创建时间，实际接口有则展示
}

type ECS struct {
	InstanceID       string
	InstanceName     string
	InstanceCode     string
	InstanceCodeName string
	Status           string
	IP               string
	EipID            string
	EipAddr          string
	SysDiskID        string
	SysDiskSize      int
	SysDiskCode      string
	EniID            string
	ProjectID        string
	CPU              int
	Memory           int
}

type Disk struct {
	ID       string
	Name     string
	Size     int
	Type     string // diskType: DATA_DISK / SYSTEM_DISK
	SpecCode string
	SpecName string
	Status   string
}

type VM struct {
	ID        string
	Name      string
	IP        string
	EIP       string
	MAC       string
	Status    string
	SpecCode  string
	SpecName  string
	CPU       int
	Memory    int
	SysDiskID string
	SysDisk   Disk
	DataDisks []Disk
	Password  string
	SSHResult string
}

// ---------- GetProjectList ----------

type projectListResp struct {
	TotalCount int            `json:"TotalCount"`
	Data       []map[string]any `json:"Data"`
}

func (c *Client) getProjectList(name string) ([]*Project, error) {
	resp := &projectListResp{}
	err := c.doGET("/project", map[string]string{
		"Action":      "GetProjectList",
		"ProjectName": name, // 文档说明为模糊搜索，客户端再做精确匹配
		"Page":        "1",
		"Size":        "100",
	}, resp)
	if err != nil {
		return nil, err
	}
	var projects []*Project
	for _, m := range resp.Data {
		p := &Project{}
		if v, ok := m["Id"].(string); ok {
			p.ID = v
		}
		if v, ok := m["Name"].(string); ok {
			p.Name = v
		}
		if v, ok := m["ProjectTypeName"].(string); ok {
			p.TypeName = v
		}
		if v, ok := m["Description"].(string); ok {
			p.Description = v
		}
		if v, ok := m["Enabled"].(float64); ok {
			p.Enabled = int(v)
		}
		// 创建时间：文档未列明字段名，兼容多种命名，尽力展示
		for _, k := range []string{"createTime", "createdTime", "CreateTime", "gmtCreate", "CreateDate"} {
			if v, ok := m[k]; ok {
				p.CreateTime = formatTimeAny(v)
				break
			}
		}
		projects = append(projects, p)
	}
	return projects, nil
}

func formatTimeAny(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		if t > 1e12 { // 毫秒时间戳
			return time.UnixMilli(int64(t)).Format("2006-01-02 15:04:05")
		}
		if t > 1e9 { // 秒时间戳
			return time.Unix(int64(t), 0).Format("2006-01-02 15:04:05")
		}
		return fmt.Sprintf("%v", t)
	case json.Number:
		return formatTimeAny(float64(mustNumber(t)))
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", t)
	}
}

func mustNumber(n json.Number) int64 {
	v, _ := n.Int64()
	return v
}

// ---------- DescribeEcs ----------

type ecsListResp struct {
	Page       int        `json:"page"`
	Size       int        `json:"size"`
	TotalCount int        `json:"totalCount"`
	TotalPages int        `json:"totalPages"`
	List       []ecsItem  `json:"list"`
}

type ecsItem struct {
	InstanceID       string `json:"instanceId"`
	InstanceName     string `json:"instanceName"`
	InstanceCode     string `json:"instanceCode"`
	InstanceCodeName string `json:"instanceCodeName"`
	Status           string `json:"status"`
	IP               string `json:"ip"`
	EipID            string `json:"eipId"`
	EipAddr          string `json:"eipAddr"`
	SysDiskID        string `json:"sysDiskId"`
	SysDiskSize      any    `json:"sysDiskSize"`
	SysDiskCode      string `json:"sysDiskCode"`
	EniID            string `json:"eniId"`
	ProjectID        string `json:"projectId"`
	InstanceCPU      any    `json:"instanceCpu"`
	InstanceMemory   any    `json:"instanceMemory"`
}

func (c *Client) describeEcs(region string, page, size int) (*ecsListResp, error) {
	resp := &ecsListResp{}
	err := c.doGET("/compute/ecs/instances", map[string]string{
		"Action":   "DescribeEcs",
		"RegionId": region,
		"Page":     itoa(page),
		"Size":     itoa(size),
	}, resp)
	return resp, err
}

// ---------- GetEcsPassword ----------

type pwdResp struct {
	Password string `json:"password"`
}

func (c *Client) getEcsPassword(region, instanceID string) (string, error) {
	resp := &pwdResp{}
	err := c.doGET("/compute/ecs/instances", map[string]string{
		"Action":     "GetEcsPassword",
		"RegionId":   region,
		"InstanceId": instanceID,
	}, resp)
	if err != nil {
		return "", err
	}
	return resp.Password, nil
}

// ---------- DescribeEnis ----------

type eniResp struct {
	Page       int        `json:"page"`
	TotalPages int        `json:"totalPages"`
	List       []eniItem  `json:"list"`
}

type eniItem struct {
	InstanceID string `json:"instanceId"`
	VmID       string `json:"vmId"`
	IPv4       string `json:"ipv4Addr"`
	MacAddr    any    `json:"macAddr"` // 文档返回表未列，实际接口可能返回（CreateEni 返回里有 macAddr），尽力而为
	EniType    string `json:"type"`
	Status     string `json:"status"`
}

func (c *Client) describeEnis(region string, page, size int) (*eniResp, error) {
	resp := &eniResp{}
	err := c.doGET("/compute/ecs/instances", map[string]string{
		"Action":        "DescribeEnis",
		"RegionId":      region,
		"Page":          itoa(page),
		"Size":          itoa(size),
		"OnlySecondary": "false", // false: 同时返回主网卡和辅助网卡
	}, resp)
	return resp, err
}

// ---------- DescribeDisks ----------

type diskListResp struct {
	Records []diskItem `json:"records"`
	Pages   int        `json:"pages"`
	Total   string     `json:"total"`
	Current int        `json:"current"`
	Size    int        `json:"size"`
}

type diskItem struct {
	DiskID            string `json:"diskId"`
	DiskName          string `json:"diskName"`
	DiskSize          any    `json:"diskSize"`
	DiskType          string `json:"diskType"`
	SpecificationCode string `json:"specificationCode"`
	SpecificationName string `json:"specificationName"`
	Status            string `json:"status"`
	ProjectID         string `json:"projectId"`
	ProjectName       string `json:"projectName"`
	AttachInfos       []struct {
		InstanceID string `json:"instanceId"`
		Type       string `json:"type"`
	} `json:"attachInfos"`
}

func (c *Client) describeDisks(region string, page, size int) (*diskListResp, error) {
	resp := &diskListResp{}
	err := c.doGET("/ebs", map[string]string{
		"Action":   "DescribeDisks",
		"RegionId": region,
		"Version":  "2", // 文档要求版本号取值 2
		"Page":     itoa(page),
		"Size":     itoa(size),
	}, resp)
	return resp, err
}

// ---------- 工具 ----------

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

func toInt(v any) int {
	switch t := v.(type) {
	case float64:
		return int(t)
	case int:
		return t
	case json.Number:
		n, _ := t.Int64()
		return int(n)
	case string:
		var n int
		fmt.Sscanf(t, "%d", &n)
		return n
	}
	return 0
}

func toStr(v any) string {
	if v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return fmt.Sprintf("%.0f", t)
	case json.Number:
		return t.String()
	default:
		return fmt.Sprintf("%v", t)
	}
}
