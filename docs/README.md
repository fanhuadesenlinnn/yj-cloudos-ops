# CloudOS 7.0 OpenAPI 接口文档

| 文件 | 说明 |
|---|---|
| `CloudOS7.0-OpenAPI-E7117-20260104.docx` | CloudOS 7.0 OpenAPI 完整接口文档（2026-01-04 版），含弹性云主机、裸金属、云硬盘、对象存储、文件存储、网络产品、云监控、用户侧云运营等 API 定义与签名机制说明 |

本工具 `yj-cloudos-ops` 基于该文档实现，相关接口：

- OpenAPI-V2 签名（HMAC-SHA1）机制见文档「公共请求参数」章节
- `GetProjectList`：按名称解析项目
- `DescribeEcs` / `GetEcsPassword` / `DescribeEnis`：弹性云主机章节
- `DescribeDisks`：云硬盘章节
