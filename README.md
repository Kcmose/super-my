# Probe Panel

Probe Panel 是一套面向 Linux 节点的轻量监控与网络探测面板。它由四个彼此独立的源码工程组成：Linux Agent、Go API、匿名游客前端和管理员前端。游客无需账号或密码，但游客与管理员页面仍只允许配置的 IP/CIDR 访问；Agent 可从白名单外主动上报，必须使用设备凭据。

当前版本已实现 Linux 基础指标、最近 5 分钟曲线、节点状态、TCP/HTTP(S) 探测、最长 90 天趋势、匿名游客浏览、管理员配置、Agent 注册与 Token、新建节点自动生成一键安装命令、审计、双层管理白名单、三入口隔离，以及游客/管理面板各自独立的深浅色模式。探测分析会分别展示当前最小/平均/最大延迟、发送/接收数、传输丢包和综合失败率，并使用服务端时间校准的串行轮询；管理面板可查看服务端识别的来源 IP 与不泄露内部信息的 API/数据库粗粒度状态。ICMP 探测按项目决定暂缓，当前 API、Agent、前端和 systemd 均不开放 ICMP 或 `CAP_NET_RAW`。

## 界面

两套面板首次访问均保持原有深色外观，右上角可切换浅色；选择只保存在当前浏览器中，游客和管理面板互不共享。切换主题不会改变页面布局，节点与探测图表也会同步更新配色。

## 服务端一键安装（首次初始化）

干净的 Debian 13 `amd64`/`arm64` 服务器不需要安装 Go 或 npm，也不需要手工上传四个工程。先登录 root 或执行 `sudo -i` 进入 root Shell；安装命令本身不依赖最小系统预装 `sudo`。修订后的服务端版本固定为不可变的 [`v1.1.0` GitHub Release](https://github.com/Kcmose/super-my/releases/tag/v1.1.0)：

```bash
curl -fsSL --proto '=https' --tlsv1.2 \
  https://raw.githubusercontent.com/Kcmose/super-my/refs/tags/v1.1.0/install.sh \
  | bash
```

脚本下载当前架构的预编译 Release 和 `SHA256SUMS`，安全解包并拒绝绝对路径、`..`、符号链接、硬链接及特殊文件。服务器只安装运行时依赖：Nginx、PostgreSQL/客户端、Certbot、`iproute2` 和 `util-linux`，不会安装 Go、Node.js 或 npm。PostgreSQL 只在确认使用本机监听配置后启动。首次安装不生成也不要求安装码：Setup 只接受 systemd 提供的 `/run/probe-panel-setup/setup.sock`，Socket 及父目录分别为 `root:root 0600/0700`，服务器没有 TCP 18080 监听。按终端提示用 root SSH 把该 Unix Socket 转发到自己电脑的 `127.0.0.1:18080`，再打开安装页面即可自动建立短期内存会话。

已经运行过 `v1.0.0`、但向导仍处于 `pending/configuring` 的服务器不能用默认安装覆盖。请在 root Shell 中把上面的命令改为 `| bash -s -- migrate-bootstrap`；脚本会严格复验旧 Release 与全部活动 bootstrap 文件，拒绝任何已 Finalize、恢复态或混合安装，并且只在新 root 私有 Socket 通过 readiness 后销毁旧安装码记录。完整边界和失败回滚语义见 [安装文档](docs/operations/install.md#已安装-v100但尚未完成向导)。

Setup 服务保持空 Capability 集和私有网络命名空间，不能直接修改生产配置或绑定任何 IP 端口。最终提交经严格校验后写入 root-only 的 tmpfs 请求文件，再由独立、无 HTTP 的 systemd oneshot Finalizer 处理。三个域名全部留空时自动使用服务器 IP 的 HTTPS 固定端口：游客 `18453`、Agent `18454`、管理 `18455`，并生成 IP SAN 私有 CA；三个域名全部填写时使用原三域名、80/443 和 ACME；只填写一部分域名会被拒绝。全部验证通过后才激活正式 Nginx 和 API。

```bash
# 查看初始化服务和私有 Socket 状态（不存在安装码）
bash install.sh status

# 普通卸载只移除 bootstrap 程序，保留配置、状态、数据库和备份
bash install.sh uninstall
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

生产入口明确二选一：有域名时使用三个独立 HTTPS Host 与公信证书；三个域名全部留空时使用同一 IP 的三个固定 HTTPS 端口与 IP SAN 私有 CA 证书。部分填写域名会被拒绝。

| Host | 内容 | 访问控制 |
|---|---|---|
| `panel.example.com` | `probe-web` 与匿名只读 `/api/v1/panel/*` | 管理 IP/CIDR 白名单，无游客登录 |
| `admin.example.com` | `probe-admin`、认证和管理 API | 同一白名单 + 管理员 Session + CSRF |
| `api.example.com` | Agent 注册、配置、上报及固定白名单内的公开安装文件 | API 使用一次性注册令牌或 Agent Bearer Token；下载文件不含秘密 |

IP 模式对应 `https://IP:18453`、`https://IP:18455`、`https://IP:18454`。浏览器 Cookie 不按端口隔离，因此该模式的游客与 Agent 代理会强制删除上游 Cookie 并隐藏 `Set-Cookie`；管理 Origin、CSRF、Session、路由和白名单仍按端口精确校验。IP 模式全程 HTTPS，但浏览器首次使用需要信任安装时生成的私有 CA；Agent 安装命令会自动校验证书指纹。

Go API 只监听 loopback；PostgreSQL 不应暴露公网。Public API 默认关闭。系统没有 SSH、WebSSH、Shell、PTY、任意命令、文件管理、端口转发、反向隧道或远程 Agent 升级能力。

## Agent 一键接入

管理员在独立管理面板新建已启用节点后，面板会自动生成一条可复制的一键安装命令；禁用节点需先启用。未注册节点可重新生成，新命令会使此前未使用的命令失效；已注册节点则显示“重新安装命令”，成功在另一台主机执行会使原 Agent 凭证失效。短命令通过严格 HTTPS 从 [`Kcmose/my-agent` 的不可变 `v1.0.2` Release](https://github.com/Kcmose/my-agent/releases/tag/v1.0.2) 读取安装器，Raw 路径使用明确的 `refs/tags/v1.0.2`；安装器完整解析后才允许产生安装副作用，随后自动识别 Linux `amd64`/`arm64`，从 Agent 入口下载并校验 systemd 单元和二进制的 SHA256，安装低权限服务、完成首次注册，最后从环境文件移除一次性令牌。IP 模式命令会额外携带固定 `ca.pem` 的 64 位 SHA-256 指纹；域名模式使用系统公信根，不携带该参数。

安装命令包含 15 分钟有效且只能使用一次的注册令牌，应先登录 root 或执行 `sudo -i` 进入 root Shell，再直接粘贴；生成的命令本身不依赖 `sudo`。为保持 Komari 式单行命令，令牌通过 `-t` 短参数传入，不进入下载 URL，但会短暂出现在安装进程参数，并可能留在 Shell 历史和剪贴板中，必须按敏感凭据处理。IP 模式只额外携带证书 SHA-256 指纹，不把整份证书编码进命令；校验通过前安装器不会发送令牌。这个流程仍是管理员主动执行的首次安装，不赋予面板远程命令或升级能力。完整前置条件、失败语义和手动方案见 [Agent 部署](docs/operations/agent-deployment.md)。

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

1. 选择三域名公信证书模式，或确认服务器规范 IP 与 `18453/18454/18455` 可达的私有 CA 模式；PostgreSQL `5432` 严格只监听 loopback。
2. 配置并验证管理 IP/CIDR 白名单。
3. 在 Debian 构建环境完成四个工程的测试与生产构建。
4. 让安装器按已选 ingress 模式严格核对活动 Nginx 路由、端口、证书、备份凭据和新源码 systemd 单元，再安装 API、两个静态站点、Nginx 和 systemd 文件。
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
