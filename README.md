# yj-cloudos-ops

CloudOS 7.0 虚拟机检查工具（Golang）

按项目检查云平台上所有虚拟机：IP / MAC / 名称 / 所属项目 / root 密码 / 规格 / 磁盘大小（含多块数据盘），
并用 IP + 密码测试 SSH 是否能正常登录。结果默认显示到屏幕，可配置导出 CSV / Excel。

- 纯 Go 实现，`CGO_ENABLED=0` 静态编译，**不依赖 glibc**，支持 Windows / Linux
- 支持跳过证书校验
- 项目按名称传入，同名多项目时屏幕列出（名称+创建时间）供用户选择
- 导出文件路径留空则不导出

## 构建

```bash
# Linux amd64
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o yj-cloudos-ops-linux-amd64 .

# Windows amd64
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o yj-cloudos-ops-windows-amd64.exe .

# 或直接 make linux / make windows
```

## 版本发布

推送 `v*` 标签即触发 GitHub Actions 流水线自动构建双平台二进制并发布 Release：

```bash
git tag v1.0.0
git push origin v1.0.0
```

查看版本：`yj-cloudos-ops -v`

## 配置

复制 `config.example.yaml` 为 `config.yaml` 并填写：

| 配置项 | 说明 |
|---|---|
| `endpoint` | API 服务地址，私有云一般为 `https://k8sVIP:30990` |
| `insecureSkipVerify` | 是否跳过证书校验 |
| `accessKeyId` / `accessKeySecret` | 平台凭证，控制台 -> 统一身份认证 -> 访问秘钥 |
| `regionId` | 区域ID，如 cn-beijing |
| `project.name` | 虚拟机所属项目名称 |
| `ssh.useIp` | `internal` / `eip` / `internal-then-eip` |
| `output.csvPath` / `output.excelPath` | 留空则不导出 |
| `raw.dir` | 接口原始返回数据保存目录，留空则不保存；保存为 `raw.dir/<运行时间戳>/<Action>[_p<页码>].json`，**可能含密码等敏感信息** |

## 运行

```bash
# 先查看账号可见的区域ID，确认 config.yaml 中的 regionId
./yj-cloudos-ops-linux-amd64 -c config.yaml -list-regions

# 正式检查项目下所有虚拟机
./yj-cloudos-ops-linux-amd64 -c config.yaml
```

## 实现说明

- 认证：OpenAPI-V2 签名（HMAC-SHA1），公共参数 + 接口参数按字母排序，
  RFC3986 编码（值双重编码，与文档 Java 示例一致），`StringToSign = 方法&%2F&规范化串`，
  HMAC key 为 `Secret+"&"`，Base64 后作为 `Signature`。
- 调用接口：
  - `GetProjectList`（/project）按名称解析项目
  - `DescribeEcs`（/compute/ecs/instances）分页拉全量主机，客户端按 projectId 过滤（该接口无项目筛选参数）
  - `GetEcsPassword`（/compute/ecs/instances）取 root 初始密码
  - `DescribeEnis`（/compute/ecs/instances）取 MAC（**尽力而为**：文档 DescribeEnis 返回字段未列 macAddr，实际有则展示，无则留空）
  - `DescribeDisks`（/ebs，Version=2）按挂载关系匹配每台主机的数据盘（多盘）
- SSH 登录测试：并发 worker 池，用 `root + GetEcsPassword 密码` 连接并执行验证命令，
  结果标记 `✓ 成功` / `✗ 认证失败(密码错误或已修改)` / `✗ 连接超时` 等。
  **注意**：GetEcsPassword 返回的是初始密码，若用户已修改密码，登录会标记失败（属预期）。
- OpenAPI 接口文档见 [docs](docs/)
- 屏幕展示：一台虚拟机一行，多块磁盘用 `盘名:大小G/类型 + ...` 压缩进单元格。

## 注意事项

- Windows 老终端（cmd）中文输出请先执行 `chcp 65001`。
- 分页大小 `pagination.pageSize` 默认 100，部分平台可能有上限，接口报错时调小（如 10）。
- MAC 地址与项目创建时间字段文档未明确列出，程序按实际返回尽力解析。
