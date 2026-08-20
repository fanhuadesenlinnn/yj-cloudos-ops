# yj-cloudos-ops

CloudOS 7.0 虚拟机检查工具（Golang）

按项目检查云平台上所有服务器（虚拟机 / 裸金属）：IP / MAC / 名称 / 所属项目 / root 密码 / 规格 / 磁盘大小（含多块数据盘），
并用 IP + 密码测试 SSH 是否能正常登录；登录成功后执行 **流水线（exec-list）**：可自定义
传输文件（push/pull）、执行本地/远端命令、采集服务器运行状态、检查服务运行状态等模块，按顺序逐台执行。
结果默认显示到屏幕，可配置导出 Excel（文件名自动生成：`<配置名>_<时间戳>.xlsx`）。

- 纯 Go 实现，`CGO_ENABLED=0` 静态编译，**不依赖 glibc**，支持 Windows / Linux
- 支持跳过证书校验
- 项目按名称传入，支持**多项目**（`project.names`）与**全部项目**（`*` / `all`），同名多项目时屏幕列出供用户选择
- **IP 筛选**（`filter`）：选定项目后按 IP 圈定（include）/ 剔除（exclude）要执行的主机，支持精确 IP / CIDR / 通配符，内网 IP 与弹性 IP 分开匹配；过滤掉的主机不取密码、不执行、不输出，仅统计显示
- **实时进度**：执行阶段单行刷新，实时显示 `完成数/总数/百分比` + 正在执行的主机（IP+主机名）与其当前步骤，一眼看出卡在哪台机器哪个步骤
- **流水线 exec-list 是唯一的执行模型**：登录成功的每台服务器按顺序执行配置的步骤模块，支持
  - `files` 传输模块：`target: push`（本机→远端）/ `pull`（远端→本机），支持文件/文件夹，覆盖/建目录/权限可配
  - `command` 命令模块：`target: local`（本机）或 `remote`（远端），可先 `workdir` 进入目录再执行命令/脚本
  - `services` 服务检查模块：检查 sshd/docker 等服务运行状态
  - `status` 状态采集模块：采集 OS/内核/负载/CPU/内存/磁盘
  - 步骤按配置顺序执行，`onError: stop`（缺省）失败即终止后续步骤 / `continue` 失败后继续
  - 不配置 exec-list 时使用默认流水线：`status -> services`（保持工具原有检查能力）；配置 `execList: []` 则只测 SSH 连通性，不执行任何步骤
- 服务器运行状态：CPU / 内存 / 磁盘使用率、负载、运行时长、OS、内核，屏幕展示摘要，Excel 单独 Sheet 展示明细
- 服务运行状态：检查服务（如 sshd），屏幕展示 `服务名:运行中/停止/异常`，Excel 单独 Sheet 明细
- 命令执行（远端）：内容通过 stdin 以 `bash -s` 执行，带超时控制；结果分类为
  成功 / 失败(exit N) / 超时 / 会话中断(疑似关机/重启)，失败时 stderr 自动回显输出尾部；
  屏幕展示摘要，Excel「流水线执行结果」Sheet 存完整输出与退出码，可配置 `output.scriptDir` 按机器落盘完整输出
- 文件传输：通过 SFTP，同名文件默认跳过、可配置 `overwrite` 覆盖替换；传输失败时（onError=stop）
  该台后续步骤不执行（避免误跑旧文件）
- 导出目录留空则不导出；`output.dir` 配置导出目录，文件名自动生成 `<配置名>_<时间戳>.xlsx`（同秒撞名自动追加序号）

## Web 模式

除命令行外，支持 Web 模式：浏览器里完成 **配置管理 + 运行 + 实时进度 + 结果查看 + 导出** 全流程。

- **新建配置自动填充精简 demo**：替换凭证/项目即可保存运行（无需从零写 YAML）
- **帮助页**：内置各模块使用方法（顶层配置/execList 各模块/注意事项），随二进制打包、离线可用
- **文件管理**：页面上传安装包/脚本到文件目录（默认 `files/`，设置页可改），配置里 `files` 模块的 `local: "files/xxx"` 直接引用推送；支持列表/下载/删除/一键复制引用路径

```bash
./yj-cloudos-ops-linux-amd64 -web -web-addr 0.0.0.0:8080
```

- 启动后访问 `http://<主机>:8080`，默认账号 `admin / admin`（登录后请在「设置」页修改）
- **多配置管理**：配置目录（默认 `./configs`，可用 `-web-configs` 或设置页修改）下每个 `.yaml` 是一个配置；
  页面支持新建（自动填充精简 demo）/ 编辑（表单 + YAML）/ 复制 / 删除；YAML 首行 `# 描述: xxx` 注释作为配置简介
- 配置文件与 CLI 完全兼容：`./yj-cloudos-ops -c configs/生产环境.yaml` 可直接用同一文件
- **运行**：选配置 →（同名多项目时页面下拉选择）→ 开始；一次只跑一个任务，重复点击会被拒绝；
  SSE 实时推送进度与日志；结果表每行可展开查看流水线步骤明细；可下载导出的 Excel
- **检查 vs 运行**：「🔍 开始检查」= 验证配置语法 + 拉取主机 + SSH 连通性测试，**不执行 exec-list 流水线**（无副作用）；
  语法/项目解析错误或 SSH 不通会友好提示（区分“改配置”与“可重试”）；「▶ 开始运行」= 按配置完整执行（含流水线步骤与导出）
- **设置**：登录账号密码（加盐哈希存储）、配置目录、文件目录、历史保留条数（默认 10，重启即清空）
- 运行历史保存在内存，重启即清空
- 鉴权：除登录接口与静态页面外，所有 API 需要登录会话（Cookie）

### 后台运行与退出

```bash
# 后台运行（Windows/Linux/macOS 各自系统原生方式脱离终端）：
# 命令行窗口可关闭，程序继续运行，日志写入 web.log
./yj-cloudos-ops -web -web-addr 0.0.0.0:8080 -daemon

# 停止后台实例（读取 web.pid，优雅退出：停任务->关HTTP->退进程）
./yj-cloudos-ops -stop
```

- **后台化原理**：Windows 用 `DETACHED_PROCESS` 脱离控制台；Linux/macOS 用 `Setsid` 新会话，不受终端关闭/SIGHUP 影响
- **PID 文件** `web.pid`（与 settings.yaml 同目录）：记录进程ID、shutdown token、端口；`-stop` 据此定位
- **退出方式**：Web 页面「退出程序」按钮（需登录）或命令行 `-stop`（Unix 发 SIGTERM / Windows 走本机+token 的 shutdown 请求）；-stop 未运行时会提示
- **日志**：程序自动写 `web.log`（前台/后台都写），设置页可查看尾部与下载

Web 模式相关参数：`-web` / `-web-addr`（默认 0.0.0.0:8080）/ `-web-configs` / `-web-settings`（默认 settings.yaml）/ `-daemon` / `-stop`。

## 构建

支持 **Windows / macOS / Linux × amd64 / arm64** 共 6 个平台（纯 Go，`CGO_ENABLED=0` 静态编译，单二进制内嵌 Web 页面）：

```bash
make all          # 构建全部 6 个平台，输出到 build/
make linux        # Linux amd64 + arm64
make windows      # Windows amd64 + arm64
make darwin       # macOS amd64 + arm64
make linux-amd64  # 单平台
```

产物位于 `build/`：`yj-cloudos-ops-<平台>-<架构>[.exe]`，例如
`yj-cloudos-ops-linux-amd64` / `yj-cloudos-ops-windows-arm64.exe` / `yj-cloudos-ops-darwin-arm64`。

或手动交叉编译：

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o yj-cloudos-ops-linux-arm64 .
```

> macOS 提示：从网络下载的未签名二进制首次运行需右键 → 打开，或执行 `xattr -cr yj-cloudos-ops-darwin-arm64` 后运行。

## 版本发布

推送 `v*` 标签即触发 GitHub Actions 流水线自动构建全部 6 平台二进制并发布 Release：

```bash
git tag v1.0.0
git push origin v1.0.0
```

查看版本：`yj-cloudos-ops -v`

## 配置

### 生成示例配置（-init）

```bash
# 生成带完整注释的示例配置到 config.yaml（已存在则不覆盖）
./yj-cloudos-ops -init
# 或指定路径
./yj-cloudos-ops -init -c configs/demo.yaml
```

示例配置包含**全部模块的用法注释**（project / filter / execList 的 files/command/services/status / output 等），编辑其中的 endpoint/凭证/regionId/项目后即可使用。

### 手动创建

复制 `config.example.yaml` 为 `config.yaml` 并填写：

| 配置项 | 说明 |
|---|---|
| `endpoint` | API 服务地址，私有云一般为 `https://k8sVIP:30990` |
| `insecureSkipVerify` | 是否跳过证书校验 |
| `accessKeyId` / `accessKeySecret` | 平台凭证，控制台 -> 统一身份认证 -> 访问秘钥 |
| `regionId` | 区域ID，如 cn-beijing |
| `resource.type` | 检查的资源类型：`ecs`(默认) / `bms`(裸金属) / `all` |
| `project.name` / `project.names` | 虚拟机所属项目名称（names 支持多个；填 `*` 或 `all` 检查全部项目） |
| `filter.includeIPs` / `filter.excludeIPs` | **IP 筛选（可选）**：`IP` 列表匹配内网 IP、`EIP` 列表匹配弹性 IP；每条规则支持精确 IP / CIDR（`10.10.1.0/24`）/ 通配符（`10.10.0.*`）。include 圈定白名单（不配置不限制），再剔除 exclude；被过滤的主机不执行不输出，仅统计 |
| `ssh.useIp` | `internal` / `eip` / `internal-then-eip` |
| `ssh.services` | 未配置 exec-list 时默认流水线 services 步骤检查的服务名（留空默认 sshd） |
| `execList` | **流水线步骤列表**（唯一执行模型）。**不配置**→默认流水线 status→services；**`execList: []`**→空流水线，只测 SSH 连通性；配置步骤则完全按步骤执行。每步包含模块类型、方向/位置、运行方式与失败策略，见下 |
| `output.dir` | 导出目录（相对/绝对），留空则不导出；文件名自动生成 `<配置名>_<时间戳>.xlsx`（如 `results/生产环境_20260820_153045.xlsx`），Excel 含「虚拟机清单」「服务器运行状态」「服务运行状态」「流水线执行结果」四个 Sheet |
| `output.scriptDir` | command 步骤完整输出按机器落盘目录（留空不落盘）；保存为 `scriptDir/<运行时间戳>/<机器名>_<内网IP>_<步骤名>.log` |
| `raw.dir` | 接口原始返回数据保存目录，留空则不保存；保存为 `raw.dir/<运行时间戳>/<Action>[_<实例ID>][_p<页码>].json`，**可能含密码等敏感信息** |

### IP 筛选 filter（可选）

选定项目后，再按 IP 圈定/剔除需要执行的主机。被过滤掉的主机**不执行 SSH 测试、不取密码、不出现在结果表中**，仅在拉取时统计显示：

```
IP筛选: 共拉取 25 台，过滤掉 5 台，保留 20 台执行
```

```yaml
filter:
  includeIPs:                 # 白名单：只执行命中的主机（不配置则不限制）
    IP: ["10.10.1.5", "10.10.1.0/24"]   # 匹配内网 IP
    EIP: ["1.2.3.4"]                    # 匹配弹性公网 IP
  excludeIPs:                 # 黑名单：剔除命中的主机（先 include 圈定，再剔除 exclude）
    IP: ["10.10.1.7", "10.10.0.*"]
    EIP: []
```

- 每条规则支持：**精确 IP**（`10.10.1.5`）、**CIDR**（`10.10.1.0/24`）、**通配符**（`10.10.0.*`，`*` 匹配一段非点字符）
- include 命中判定：内网 IP 命中 `IP` 列表 **或** 弹性 IP 命中 `EIP` 列表即保留；exclude 同理，任一命中即剔除
- 过滤在取密码/详情**之前**执行，被过滤的主机不会产生任何 API 请求
- 规则格式错误（如非法 CIDR）在启动时即报错退出

### 实时进度

SSH 测试与流水线执行阶段，stderr 单行实时刷新，不再只有“完成几台”：

```
[3/20] 15% | 执行中: 10.10.1.5 web-01(2/3 部署)  10.10.1.9 db-01(1/3 分发) | 完成: 3
```

- 主机被 worker 领走（开始 SSH 连接）即显示为“执行中”，连接慢/超时的机器也能看到卡在哪
- 每台显示 IP + 主机名 + 当前步骤（第几步/共几步 步骤名），单步超时时能看出卡在哪个步骤
- 失败/超时等 stderr 回显会先清掉进度行、输出完再恢复，不互相覆盖；完成后进度行消失

### 流水线 exec-list

登录成功的每台服务器按 `execList` 顺序执行（并发 worker 池内逐台执行）。**execList 有三种形态**：

- **不配置**（缺失/`null`）→ 默认流水线 `status -> services`（保持工具原有“采集运行状态 + 检查服务”能力）；
- **`execList: []`** → 显式空流水线，**只测 SSH 连通性**，不执行任何步骤（不想采集时的用法）；
- **配置步骤列表** → 完全按配置执行（配置里没有 status/services 步骤就不会采集）。

步骤通用字段：

| 字段 | 取值 | 说明 |
|---|---|---|
| `name` | 任意字符串 | 步骤名，展示/结果表/落盘文件名用；缺省自动生成 step1/step2... |
| `type` | `files` / `command` / `services` / `status` | 模块类型 |
| `target` | files: **`push`/`pull`（必填）**；command: `local`/`remote`（缺省 remote） | files 为传输方向；command 为执行位置；services/status 固定 remote |
| `run` | `once` / `always` | `once`=本地步骤只跑一次（缺省）；`always`=每台服务器都跑；仅 command target=local 有意义 |
| `onError` | `stop` / `continue` | `stop`=失败终止后续步骤（缺省）；`continue`=失败后继续下一步 |

**模块类型：**

- `files`（文件传输，target 必填 push / pull）
  ```yaml
  # push：本机 -> 远端（分发/部署）
  - name: "分发安装包"
    type: files
    target: push
    overwrite: false       # 步骤级默认覆盖目标端同名文件；缺省 false=已存在则跳过（安全）
    mkdirs: true           # 目标端父目录不存在时自动创建，缺省 true
    files:
      - local: "dist/app.tar.gz"         # 本机源（文件/文件夹）
        remote: "/opt/myapp/app.tar.gz"  # 远端目标（绝对路径，不能以 / 结尾）
        mode: "0755"                     # 目标端权限（八进制），默认 0644
        overwrite: true                  # 单文件覆盖开关，优先于步骤级
      - local: "configs/"                # 本机文件夹：递归传输，保持目录结构
        remote: "/opt/myapp/configs"     # 文件夹时远端为目标目录，每文件映射为 目录/<相对路径>

  # pull：远端 -> 本机（收集日志/配置）
  - name: "收集日志"
    type: files
    target: pull
    files:
      - remote: "/var/log/app/"          # 远端源（文件夹：递归拉取保持结构）
        local: "logs/app/"               # 本机目标目录
      - remote: "/etc/app.conf"          # 单个文件
        local: "logs/app.conf"
  ```
- `command`（先 cd 到 workdir 再执行命令/脚本，target 支持 local / remote）
  ```yaml
  - name: "本地构建"                       # target=local：在本机执行（只跑一次或每台都跑）
    type: command
    target: local
    run: once
    workdir: "/home/ci/app"               # 可选：先进入该目录（本地执行）
    command: "go build -o dist/app ."     # 单行命令；或 script: 内嵌多行 / scriptPath: 本地脚本文件（三选一）
    timeout: 120s

  - name: "远端部署"                       # target=remote：在每台服务器经 SSH 执行
    type: command
    target: remote
    workdir: "/opt/app"                   # 可选：执行前先 cd（远端要求绝对路径）
    scriptPath: "scripts/deploy.sh"       # 内容走 stdin 传远端 bash -s，无转义/注入问题；Windows(CRLF) 格式脚本自动归一化为 \n
    timeout: 60s
  ```
- `services`（检查服务运行状态，target 固定 remote）
  ```yaml
  - name: "检查服务"
    type: services
    services: ["sshd", "docker"]          # 留空默认检查 sshd；结果写入 Excel「服务运行状态」Sheet
    onError: continue
  ```
- `status`（采集服务器运行状态，target 固定 remote）
  ```yaml
  - name: "采集运行状态"
    type: status                          # 结果写入 Excel「服务器运行状态」Sheet
    onError: continue
  ```

**执行语义：**

- 步骤严格按配置顺序执行；**本地 once 步骤（阶段一）在所有机器并发前只执行一次**，
  其失败（onError=stop）会**全局终止**流水线（所有机器不再执行后续步骤）；
  远端/always 步骤的失败（onError=stop）只终止**该台机器**的后续步骤。
- 被终止而未能执行的步骤标记「未执行(上游失败)」，屏幕/Excel 可见失败链。
- 登录失败的机器不执行任何远端步骤（与登录结果无关的本地 once 步骤仍正常执行）。
- `command` 步骤结果按 State 分类：`success` / `fail`(收到退出码，含被信号杀如 kill -9 → 128+N) /
  `timeout` / `interrupted`(未收到退出码或连接断开，典型如脚本里执行了 `init 0`/`reboot`) / `error`(未执行)。
- 失败/超时/会话中断时 stderr 自动回显该台状态、原因与输出尾部（最后 20 行）；单步输出上限 100KB，超出截断保留末尾。

## 运行

```bash
# 先查看账号可见的区域ID，确认 config.yaml 中的 regionId
./yj-cloudos-ops-linux-amd64 -c config.yaml -list-regions

# 查看账号可见的项目，确认 project.names 填哪些
./yj-cloudos-ops-linux-amd64 -c config.yaml -list-projects

# 正式检查项目下所有虚拟机（多项目/全部项目均可；登录成功的机器执行 exec-list 流水线）
./yj-cloudos-ops-linux-amd64 -c config.yaml
```

## 实现说明

- 认证：OpenAPI-V2 签名（HMAC-SHA1），公共参数 + 接口参数按字母排序，
  RFC3986 编码（值双重编码，与文档 Java 示例一致），`StringToSign = 方法&%2F&规范化串`，
  HMAC key 为 `Secret+"&"`，Base64 后作为 `Signature`。
- 项目支持：`project.names` 多项目（可含 `*`/`all` 检查全部）；优先 GetProjectList 全量匹配，
  未返回时从云硬盘数据 projectName 反查，仍找不到则报错并列出可识别项目。
- IP 筛选：`filter` 在 collectECS/collectBMS 取密码之前执行（过滤掉的主机不产生密码/详情请求）；
  规则编译为 精确IP map + CIDR(IPNet.Contains) + 通配符正则 三种匹配器，
  内网 IP 与弹性 IP 分开匹配，先 include 圈定再 exclude 剔除，规则格式启动时即校验。
- 流水线：`execList` 是唯一的执行模型。阶段一在并发前串行执行 `type=command && target=local && run=once` 的步骤（只跑一次）；
  阶段二在 worker 池内对每台登录成功的服务器按顺序执行其余步骤。每步独立记录 名称/类型/方向(位置)/状态/退出码/输出/耗时，
  屏幕「流水线」列展示每步摘要（如 `1✓ 2✗ 3✓`），Excel「流水线执行结果」Sheet 每台每步一行存完整明细。
- 服务器运行状态：status 步骤经 SSH 执行 `uname`/`uptime`/`top`/`free`/`df` 采集 OS、内核、运行时长、
  负载、CPU/内存/磁盘使用率，屏幕展示摘要，Excel「服务器运行状态」Sheet 展示明细。
- 服务运行状态：services 步骤检查配置的服务（systemctl 优先，兼容 SysV service），
  屏幕展示 `服务名:运行中/停止/异常/不存在`，Excel「服务运行状态」Sheet 每台虚拟机每个服务一行。
- 命令执行（remote）：内容通过 SSH channel 的 stdin 传给远端 `bash -s`（不经 shell 拼接，无转义/注入问题），
  配置 `workdir` 时先 `cd '<dir>' && bash -s`（单引号转义防注入）；带步骤级超时（`timeout`，默认 60s，超时关闭会话强制中断）。
  结果按 State 分类：`success` / `fail` / `timeout` / `interrupted`（未收到退出码或连接断开，如脚本执行了 `init 0`/`reboot`——
  命令可能已下发、机器正在关机，故单独标记为"会话中断"而非"失败"） / `error`（未执行）。
  失败/超时/会话中断时 stderr 自动回显该台状态、原因与输出尾部（最后 20 行）；单步输出上限 100KB，超出截断保留末尾。
  可配置 `output.scriptDir` 按机器落盘。以上状态均**不影响** SSH 登录结果标记。
- 命令执行（local）：配置 `workdir` 时先进入该目录（`cmd.Dir`），内容经 `sh -s`（Unix，stdin 传入）/ `cmd /C`（Windows）执行，
  同样带超时（超时杀掉整个进程组，防止 sleep 等子进程持有管道阻塞等待）与退出码，支持 `run: once` 只跑一次或 `run: always` 每台机器各跑一次。
- 文件传输：files 步骤通过 SFTP（`github.com/pkg/sftp`，纯 Go）双向传输。
  - `push`（本机 → 远端）：`local` 为本机源（文件/文件夹），`remote` 为远端目标（绝对路径且**精确到文件**，不能以 / 结尾）；
    本地为文件夹时递归展开（保持相对目录结构，每个文件精确映射到 `remote/<相对路径>`）。
  - `pull`（远端 → 本机）：`remote` 为远端源（文件/文件夹，SFTP 递归列出后拉取），`local` 为本机目标；
    远端为文件夹时递归拉取，每个文件映射到 `local/<相对路径>`。
  - 目标端同名文件已存在时默认**跳过**（安全，不误覆盖），单条 `overwrite: true` 或步骤级 `overwrite: true` 才覆盖替换；
    `mkdirs` 控制目标端父目录自动创建（默认开）；`mode` 设置目标端权限（八进制，默认 0644）。
  - 传输失败（onError=stop）时该台后续步骤不执行，避免误跑远端旧文件。屏幕「流水线」列展示摘要，失败/跳过会在 stderr 回显现场。
- 调用接口：
  - `GetProjectList`（/project）按名称解析项目
  - `DescribeEcs`（/compute/ecs/instances）分页拉全量主机，客户端按 projectId 过滤（该接口无项目筛选参数）
  - `GetEcsPassword`（/compute/ecs/instances）取 root 初始密码
  - `DescribeEnis`（/compute/ecs/instances）取 MAC（**尽力而为**：文档 DescribeEnis 返回字段未列 macAddr，实际有则展示，无则留空）
  - `DescribeDisks`（/ebs，Version=2）按挂载关系匹配每台主机的数据盘（多盘）
- SSH 登录测试：并发 worker 池，用 `root + GetEcsPassword 密码` 连接并执行验证命令，
  结果标记 `✓ 成功` / `✗ 认证失败(密码错误或已修改)` / `✗ 连接超时` 等。
- 实时进度：`progressMgr` 维护 完成数/执行中主机(IP+主机名)/当前步骤，ticker 每 500ms 重绘 stderr 单行；
  主机被 worker 领走即标记执行中，runPipeline 每步开始前更新步骤；
  EIP 通过别名映射到主 key（useIp=eip 时也能正确显示）；
  失败回显等 stderr 输出先 clear 进度行再恢复，避免互相覆盖；测试直调 trySSH 时进度控制器为 nil、自动跳过。
  **注意**：GetEcsPassword 返回的是初始密码，若用户已修改密码，登录会标记失败（属预期）。
- OpenAPI 接口文档见 [docs](docs/)
- 屏幕展示：一台虚拟机一行，多块磁盘用 `盘名:大小G/类型 + ...` 压缩进单元格。

## 注意事项

- 命令执行：remote 命令以 root 身份在每台服务器执行，等同于远端任意代码执行能力，请勿在命令/脚本中写入密码等敏感信息；
  脚本请用 LF 换行（Windows 编辑器的 CRLF 会导致 `bash -s` 报错）；远端需有 bash（本工具状态采集本身也依赖 bash）。
- 命令里执行 `init 0` / `reboot` / `shutdown` 等会掐断 SSH 会话：拿不到退出码、输出可能不完整，
  程序会标记为「会话中断(疑似关机/重启)」而非「失败」，这是正常语义，请结合机器实际状态判断；已收到的部分输出会照常保存。
- Windows 老终端（cmd）中文输出请先执行 `chcp 65001`；本地 command 步骤在 Windows 上用 `cmd /C` 执行。
- 分页大小 `pagination.pageSize` 默认 100，部分平台可能有上限，接口报错时调小（如 10）。
- 取密码/详情等按实例请求已**并发化**（`http.concurrent`，默认 10，可调大提速），并有进度提示，不再长时间无响应。
- 项目列优先用 GetProjectList（全量拉取）补全项目名，无磁盘的项目也能显示名称而非 ID。
- MAC 地址：DescribeEnis 返回字段中**未包含 macAddr**（与文档一致），故 MAC 列为空属正常；如确实需要 MAC，需另行确认平台接口。
- 项目解析：优先用 GetProjectList 全量匹配项目名称；若该接口未返回目标项目（本项目实测只返回 default），会自动从云硬盘数据中的 projectName 反查，仍找不到则报错并列出所有可识别项目。
- 接口实际返回与文档存在差异（如 DescribeDisks 的 total 文档写 String、实际返回数字），程序已按实际返回兼容，原始数据可通过 `raw.dir` 保存排查。
- `GetEcsPassword` 返回的是初始密码，若用户已修改密码，登录测试会标记失败（属预期）。
