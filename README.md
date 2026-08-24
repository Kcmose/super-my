# Probe Panel

Probe Panel 是一套面向 Linux 节点的轻量监控与网络探测面板。它由四个彼此独立的源码工程组成：Linux Agent、Go API、匿名游客前端和管理员前端。游客无需账号或密码，但游客与管理员页面仍只允许配置的 IP/CIDR 访问；Agent 可从白名单外主动上报，必须使用设备凭据。

当前版本已实现 Linux 基础指标、最近 5 分钟曲线、节点状态、TCP/HTTP(S) 探测、最长 90 天趋势、匿名游客浏览、管理员配置、Agent 注册与 Token、新建节点自动生成一键安装命令、审计、双层管理白名单、三入口隔离，以及游客/管理面板各自独立的深浅色模式。探测分析会分别展示当前最小/平均/最大延迟、发送/接收数、传输丢包和综合失败率，并使用服务端时间校准的串行轮询；管理面板可查看服务端识别的来源 IP 与不泄露内部信息的 API/数据库粗粒度状态。ICMP 探测按项目决定暂缓，当前 API、Agent、前端和 systemd 均不开放 ICMP 或 `CAP_NET_RAW`。

## 界面

两套面板首次访问均保持原有深色外观，右上角可切换浅色；选择只保存在当前浏览器中，游客和管理面板互不共享。切换主题不会改变页面布局，节点与探测图表也会同步更新配色。

## 服务端一键安装（首次初始化）

干净的 Debian 13 `amd64`/`arm64` 服务器不需要安装 Go 或 npm，也不需要手工上传四个工程。当前服务端版本固定为不可变的 [`v1.0.0`](https://github.com/Kcmose/super-my/releases/tag/v1.0.0)：

```bash
curl -fsSL --proto '=https' --tlsv1.2 \
  https://raw.githubusercontent.com/Kcmose/super-my/refs/tags/v1.0.0/install.sh \
  | sudo bash
```

脚本下载当前架构的预编译 Release 和 `SHA256SUMS`，安全解包并拒绝绝对路径、`..`、符号链接、硬链接及特殊文件。服务器只安装运行时依赖：Nginx、PostgreSQL/客户端、Certbot、`iproute2` 和 `util-linux`，不会安装 Go、Node.js 或 npm。安装 Nginx 包前会临时 mask 服务，避免包安装脚本短暂开放默认站点；随后保持 Nginx 停止并禁用，确认 `80/443` 没有监听。PostgreSQL 只在确认使用本机监听配置后启动。接着仅启动 `127.0.0.1:18080` 上的临时初始化服务。它不会接收数据库或管理员密码，安装完成后按终端提示建立 SSH 隧道，在本机访问 `http://127.0.0.1:18080/install`，使用仅显示一次、30 分钟有效的 256-bit 安装码继续配置。

长期运行的 HTTP setup 服务保持空 Capability 集，不能直接修改生产配置或绑定 `80/443`。最终提交经严格校验后写入 root-only 的 tmpfs 请求文件，再由独立、无 HTTP 的 systemd oneshot Finalizer 处理；只有该短生命周期进程可以写入受控的 Nginx/systemd/TLS/发布目录、切换到 PostgreSQL 账户并临时绑定 TCP 80 完成 Certbot HTTP-01。全部验证通过后才激活正式 Nginx 和 API。

```bash
# 查看初始化服务状态（不显示安装码或哈希）
sudo bash install.sh status

# 普通卸载只移除 bootstrap 程序，保留配置、状态、数据库和备份
sudo bash install.sh uninstall
```

`purge` 刻意不提供；彻底删除数据必须先完成并验证最终备份，再走单独复核流程。`Kcmose/super-my` 使用 GitHub Immutable Releases；当前脚本不会回退到 `main` 或其他可移动分支。

## 工程结构

```text
probe-agent/   Linux Agent；独立 Go 模块，只主动访问 Agent API
probe-api/     Go API、数据库迁移、OpenAPI 与服务端部署文件
probe-web/     匿名游客 Vue/Vite SPA，只读 Panel API
probe-admin/   管理员 Vue/Vite SPA，登录与管理功能
docs/          架构、权限、协议、安装、运维和验收文档
```

四个工程拥有独立入口、依赖、测试和构建产物，不跨目录导入源码。`probe-api` 不嵌入前端文件，两个前端也不直接访问数据库。

## 安全边界

生产环境使用三个独立 HTTPS Host：

| Host | 内容 | 访问控制 |
|---|---|---|
| `panel.example.com` | `probe-web` 与匿名只读 `/api/v1/panel/*` | 管理 IP/CIDR 白名单，无游客登录 |
| `admin.example.com` | `probe-admin`、认证和管理 API | 同一白名单 + 管理员 Session + CSRF |
| `api.example.com` | Agent 注册、配置、上报及固定白名单内的公开安装文件 | API 使用一次性注册令牌或 Agent Bearer Token；下载文件不含秘密 |

Go API 只监听 loopback；PostgreSQL 不应暴露公网。Public API 默认关闭。系统没有 SSH、WebSSH、Shell、PTY、任意命令、文件管理、端口转发、反向隧道或远程 Agent 升级能力。

## Agent 一键接入

管理员在独立管理面板新建已启用节点后，面板会自动生成一条可复制的一键安装命令；禁用节点需先启用。未注册节点可重新生成，新命令会使此前未使用的命令失效；已注册节点则显示“重新安装命令”，成功在另一台主机执行会使原 Agent 凭证失效。短命令通过严格 HTTPS 从 [`Kcmose/my-agent` 的不可变 `v1.0.1` Release](https://github.com/Kcmose/my-agent/releases/tag/v1.0.1) 读取安装器，Raw 路径使用明确的 `refs/tags/v1.0.1`；安装器完整解析后才允许产生安装副作用，随后自动识别 Linux `amd64`/`arm64`，从 Agent Host 下载并校验 systemd 单元和二进制的 SHA256，安装低权限服务、完成首次注册，最后从环境文件移除一次性令牌。当前预览私有 CA 命令为 366 个字符，使用公信证书的典型生产命令约 291 个字符。

安装命令包含 15 分钟有效且只能使用一次的注册令牌，可直接粘贴到目标机能够使用 `sudo` 的 Shell。为保持 Komari 式单行命令，令牌通过 `-t` 短参数传入，不进入下载 URL，但会短暂出现在安装进程参数，并可能留在 Shell 历史和剪贴板中，必须按敏感凭据处理。内网私有 CA 只额外携带证书 SHA-256 指纹，不再把整份证书编码进命令；校验通过前安装器不会发送令牌。这个流程仍是管理员主动执行的首次安装，不赋予面板远程命令或升级能力。完整前置条件、失败语义和手动方案见 [Agent 部署](docs/operations/agent-deployment.md)。

## 构建原则

Windows 工作区只保留源码与文档。依赖安装、格式化、测试、构建和部署统一在 Debian 13 环境进行。典型验证命令如下：

```bash
cd probe-agent
find . -type f -name '*.go' -print0 | xargs -0 gofmt -w
go test ./...
go vet ./...
CGO_ENABLED=0 go build -trimpath -o build/probe-agent ./cmd/probe-agent

cd ../probe-api
find . -type f -name '*.go' -print0 | xargs -0 gofmt -w
go test ./...
go vet ./...
CGO_ENABLED=0 go build -trimpath -o build/probe-api ./cmd/probe-api

# 提供独立 PostgreSQL 测试库时，跨包集成夹具按顺序运行
PROBE_API_INTEGRATION_DATABASE_URL='postgres://...' go test -count=1 -p 1 ./...

cd ../probe-web
npm ci
npm test
npm run build

cd ../probe-admin
npm ci
npm test
npm run build
```

网络直连失败时，构建环境可临时使用项目约定的 SOCKS5 或 HTTP 代理；代理不得写入产品默认配置、源码或锁文件。

## 部署顺序

1. 准备 PostgreSQL 数据库、三个正式域名与可信 TLS 证书。
2. 配置并验证管理 IP/CIDR 白名单。
3. 在 Debian 构建环境完成四个工程的测试与生产构建。
4. 让安装器严格核对活动 Nginx 三 Host/路由、备份凭据和新源码 systemd 单元，再安装 API、两个静态站点、Nginx 和 systemd 文件。
5. 获取与备份/恢复共用的数据库维护锁，先备份数据库，再执行 `probe-api migrate status` 与 `probe-api migrate up`。
6. 创建首个管理员，部署需要监控的 Agent。
7. 执行安全冒烟、备份恢复演练和负载冒烟，再开放正式入口。

不要直接把示例域名、示例密码或示例数据库连接串投入生产。详细步骤见：

- [安装文档](docs/operations/install.md)
- [升级文档](docs/operations/upgrade.md)
- [Agent 部署](docs/operations/agent-deployment.md)
- [备份与恢复](docs/operations/backup-restore.md)
- [故障排查](docs/operations/troubleshooting.md)
- [内网预览手工验收清单](docs/operations/manual-testing.md)
- [API 使用说明](docs/api.md)
- [OpenAPI 契约](probe-api/api/openapi.yaml)
- [安全测试报告](docs/reports/security-test.md)
- [负载测试报告](docs/reports/load-test.md)

## 数据保留

- CPU、内存、硬盘、网络基础指标最多保留 5 分钟。
- TCP/HTTP(S) 探测原始与聚合数据按目标配置保留，最长 90 天。
- 每日维护使用 PostgreSQL advisory lock、持久化成功水位和单一事务，统一清理过期 Session、登录限流、注册/Agent Token 与满足聚合保护条件的幂等台账；失败不推进水位。
- 每日 PostgreSQL 备份保留最近 7 份；每周备份保留最近 4 份。
- 缩短保留期限后，后续清理会删除过期数据；已删除数据只能从事先存在的备份恢复。

## 文档入口

架构和约束分别见 [架构说明](docs/design/architecture.md)、[权限矩阵](docs/design/permissions.md)、[Agent 协议](docs/design/agent-protocol.md) 和 [保留策略](docs/design/retention.md)。开发轨道见 `jihua.md`，长期约定见 `jiyi.md`，已实施变更见 `biandong.md`。
