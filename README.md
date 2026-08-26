# Probe Panel

Probe Panel 是一套由三个独立产品组成的 Linux 监控系统。当前开发阶段只交付第一个产品：**管理端**。

## 产品边界与安装顺序

| 顺序 | 产品 | 内容 | 安装位置 |
| ---: | --- | --- | --- |
| 1 | 管理端 | `probe-admin`、`probe-api`、数据库迁移、PostgreSQL/Nginx 配置 | 管理服务器 |
| 2 | Agent | `probe-agent` 二进制、配置和服务 | 每台被监控服务器 |
| 3 | 访客前端 | `probe-web` 静态站点和只读 API 接入配置 | 按需选择的 Web 服务器 |

三者分别发布、安装、升级、回滚和卸载。不存在“一个脚本把三者全部装完”的新版本安装方式。

运行时数据流固定为：

```text
probe-agent -> probe-api/PostgreSQL -> probe-admin
                                  \-> probe-web（最后按需安装）
```

管理界面与 API 属于同一个管理端产品，但仍分别构建；API 不嵌入管理前端文件。Agent 只主动上报和拉取结构化配置，不监听端口；两个前端只通过 API 取数，不连接数据库。

## 当前状态

- `management v1.2.0`：当前源码正在实现的第一个管理端独立候选基线，**尚未发布 GitHub Release**。历史 full 版本不是它的合法 management 前序，因此机器账本把 `promotion_eligible` 固定为 `false`；v1.2.0 的 60 个 cell 永远保持 candidate。第一个可晋级正式支持的目标版本是 v1.2.1。
- `full v1.1.0`：已经发布的历史全量安装包，仅保留兼容与审计用途，不代表新的产品拆分方向。当前 `v1.2.0` 根安装器不会选择、安装或迁移它；需要维护旧实例时必须使用不可变 `v1.1.0` 标签中的旧脚本。
- 当前多系统运行时 ABI 为 `probe-linux-systemd-v2`，候选架构为 Linux `amd64`/`arm64`。安装器只接受下面 15 个精确平台 ID，不能从发行版家族名、相邻版本或包管理器推断支持：

```text
debian-9-systemd       debian-10-systemd       debian-11-systemd
debian-12-systemd      debian-13-systemd
ubuntu-18.04-systemd   ubuntu-20.04-systemd     ubuntu-22.04-systemd
ubuntu-24.04-systemd   ubuntu-26.04-systemd
centos-linux-7-systemd centos-linux-8-systemd
centos-stream-8-systemd centos-stream-9-systemd centos-stream-10-systemd
```

- “进入 ABI 分支”只表示对应的包管理、Nginx 方言、PostgreSQL 路径和 systemd 资产已有精确适配，**不等于正式支持**。真实远端 Debian 10 已通过当前 Shell/bootstrap 契约、Go 全包测试/vet与双架构交叉构建，但完整安装、Setup、重启、升级、回滚、备份恢复与卸载 E2E 尚未完成；其余精确单元也必须分别验收。
- 正式支持状态由 `probe-api/deploy/support/policy-v1.json` 与 `releases/v1.2.0.json` 的机器可读账本约束：15 个精确平台 × 2 架构 × IP/域名，共 60 个 cell。当前门禁结果固定为 **60 candidate / 0 supported**；没有完整证据套件和人工显式提升的 cell 不能被文档或 Release 汇总成平台级正式支持。未来包含 `supported` 的账本还必须由审核环境通过 `--release-assets`、`--source-commit` 及对应的 `--upgrade-from-*` 参数提供真实双架构目标/前序管理包和可信 tag commit；gate 会直接重算外层包及包内清单哈希，并校验 `RELEASE-MANIFEST` 的版本、架构、仓库、ref 与 commit。
- Debian 9/10/11/12、Ubuntu 18.04/20.04、CentOS Linux 7/8 和 CentOS Stream 8 处于旧版、EOL 或延长维护层，默认拒绝安装；Debian 12 已于 2026-07-12 转入 LTS，Debian 11 LTS 将于 2026-08-31 结束，Ubuntu 20.04 已退出标准安全维护，因此尚未发布的 v1.2.0 对这些版本失败关闭。明确执行 `install --accept-eol` 后，Debian/Ubuntu 候选会创建独立的 Probe APT source，按版本选择发行版 live/archive 与 PGDG live/archive，并分别绑定发行版 keyring 和固定 PGDG key；它不读取或覆盖用户现有源。这个参数不会恢复安全更新。Debian 9 缺少安全 HTTPS method 时失败关闭，Debian 9 arm64 因官方 PGDG archive 没有 PostgreSQL 14 继续不可用。CentOS 候选现已实现隔离 `reposdir`、精确仓库 allowlist、Vault/Stream、EPEL 与 PGDG 14 映射，以及固定 key 哈希/完整指纹和已安装 RPM 来源绑定；实现这些源契约不等于已经可生产安装或有资格晋级。
- CentOS 各单元目前仍是 candidate：真实 CentOS VM 尚未完成验收，SELinux Enforcing 下 loopback API `8080` 的共享端口类型、Nginx 反向代理权限及可回滚策略仍未闭环。仓库中的 policy/helper 只是未集成的候选，不会进入正式 management bundle。为避免半安装，根安装器检测到 Enforcing 会在包、账号、服务和永久路径变更前拒绝；关闭 SELinux 不是生产支持方案。安装器不管理 firewalld；开放、关闭端口和区域策略由服务器管理员负责。
- Rocky Linux、AlmaLinux、Alpine Linux、OpenRC 及其他未列出的派生环境当前仅做拒绝或后续规划；检测到 `apt-get`、`dnf`、`yum`、`apk` 或 `systemctl` 不构成支持证据。

推送 `v1.2.0` 标签只会在 GitHub Actions 中构建并暂存 14 天的候选 Artifact，**不会创建、编辑或公开 GitHub Release**。标签和候选包都不是发布批准。v1.2.0 可在完成首次安装、回滚、备份恢复、卸载和候选资产门禁后，由人工独立审核为不可变的 0-supported baseline；它不要求也不能伪造从历史 full 版本升级的证据。随后 v1.2.1 必须从同平台、同架构、同入口的不可变 v1.2.0 完成升级证据，才有资格逐格晋级。

## 管理端独立安装契约

管理端发行包名称固定为：

```text
probe-panel-management-v1.2.0-linux-amd64.tar.gz
probe-panel-management-v1.2.0-linux-arm64.tar.gz
```

包内只允许出现：

- `probe-api` 与 `probe-setup`；
- 独立构建的 `probe-admin` 静态文件；
- 数据库迁移；
- 管理端 Setup、systemd、备份、Nginx，以及独立的校验、升级调用、恢复协调和普通卸载资产。

包内明确禁止出现 `probe-web`、`probe-agent` 二进制、Agent systemd 单元或 Agent 下载目录。

所有目标系统（尤其 Debian 9–12、Ubuntu 18.04/20.04、CentOS 7 等旧版或延长维护系统）只下载、校验并运行同一 Release 中预构建的 management bundle；目标服务器不会克隆源码，也不会安装 Go/Node 后现场编译。构建机与安装目标是两条独立边界。

管理端 bootstrap 在仓库中按职责拆分：`install/common.sh` 只保存通用解析、验包、事务、回滚和 Setup 编排；`install/platforms/debian.sh`、`ubuntu.sh`、`centos.sh` 分别保存该系列的精确版本映射、包管理、原生 unit/包归属、账号和安全门禁。`install/build-standalone.sh` 按固定顺序确定性生成根 `install.sh`。对外的一键安装仍只执行这个完整的根脚本：它解析 `/etc/os-release` 后调用对应家族适配器，不在安装过程中下载或 `source` GitHub 上的活动子脚本。因此源码可以按系列维护，而公开入口仍保持单文件、同版本和截断失败关闭。

bootstrap 在修改主机前要求已有 Bash、Python 3、系统 CA bundle、支持 TLS 1.2 的 `curl` 或 HTTPS `wget`（二选一），以及校验、文本处理、iproute、util-linux、procps、systemd 和原生包管理工具；旧 curl 不满足能力时会使用已有 wget，二者都不可用则列明后退出。详细清单见[管理端安装](docs/operations/install.md)。

首次安装仍通过 root SSH 转发的私有 Unix Socket 打开向导，数据库密码和首个管理员密码不会进入安装命令、进程参数或公网请求。向导只配置一个管理入口：

- IP 模式：`https://<服务器IP>:18455`，使用 IP SAN 私有 CA；
- 域名模式：一个管理域名，使用 ACME；
- 不再要求游客 `18453`、Agent `18454`、游客域名或 Agent 域名。

安装完成后可以创建节点并保存每个节点的 Agent 采集参数。全局 Agent 入口尚未配置时，系统状态明确显示“待配置”，不会注册 Agent 公网路由，也不会签发安装命令或 Token；后续 Agent 阶段会通过显式配置安全启用这些能力。

完成 Setup 的管理端通过同一目标版本 standalone 脚本执行 `validate`、`upgrade` 和
普通 `uninstall`；数据库恢复使用本机
`/usr/local/lib/probe-panel/restore-management.sh`。这些命令只管理 API、管理 SPA、
migrations 和 Probe 自有服务/链接，普通卸载继续保留配置、数据库、备份与未激活
发布目录，`purge` 不实现。源码存在不代表 `v1.2.0` 已经发布。

## 已有业务服务器上的共存原则

管理端安装器借鉴成熟脚本的“先探测、再分派”思路，但只修改 Probe 自己拥有的路径：

- 已安装且配置有效的 Nginx 可以复用；管理端 IP 模式只新增自己的配置链接并占用 `18455`，失败时只移除该链接，再经 `nginx -t` 后 reload；不会停止、禁用或清理其他站点。
- 已有 PostgreSQL 只有在本机服务可用且 `5432` 严格限制在 loopback 时才会复用；发现对外监听会拒绝安装，不会擅自修改或停止现有服务。
- 发现既有 Probe 文件、服务、数据库名/用户冲突、目标端口占用或无法证明归属的路径时失败关闭，不覆盖猜测中的旧安装。
- 当前域名模式仍使用独占的 standalone ACME，要求 Nginx inactive 且 `80/443` 空闲；因此暂不宣称它能与正在运行的 Web 站点共存。已有站点的服务器优先使用 IP 模式，或等待后续经验证的域名共存方案。
- 安装器不自动调整 firewalld。管理员必须按选定入口自行管理防火墙；CentOS SELinux Enforcing 策略尚未完成真实 VM 闭环前，不能把对应 candidate 当成可生产部署平台。

“非干净服务器可安装”指安全识别并保留无关服务，不代表强行覆盖任何环境。

## 工程结构

```text
install/       管理端 bootstrap 通用源码、三套平台适配器与单文件生成器
install.sh     由 install/ 确定性生成的公开 standalone bootstrap
probe-admin/   管理员 Vue/Vite SPA
probe-api/     Go API、Setup、迁移、OpenAPI 与管理端部署资产
probe-agent/   独立 Linux Agent；当前不进入管理端发行包
probe-web/     独立访客前端；当前不进入管理端发行包
docs/          架构、协议、安装、运维和验收文档
```

## 开发与验证约束

Windows 工作区只保存源码和文档。普通依赖安装、格式化、单元测试、静态检查和双架构交叉构建在专用远端 Linux 测试机上低并发执行；正式候选资产仍只允许在干净、受控的 Debian 13 构建环境生成。这个构建机约束不表示安装目标只能是 Debian 13，旧目标服务器只消费预构建 management bundle。每个候选目标的安装、重启和生命周期 E2E 必须在对应的真实 systemd VM 中另外执行。管理端阶段至少需要验证：

```bash
cd probe-api
find . -type f -name '*.go' -print0 | xargs -0 gofmt -w
go test ./...
go vet ./...

cd ../probe-admin
npm ci
npm test
npm run build

cd ../probe-api
bash ../install/tests/package-source-contract.sh
bash deploy/tests/bootstrap-install-contract.sh
bash deploy/tests/build-release-bundles-contract.sh
bash deploy/tests/management-lifecycle-contract.sh
bash deploy/tests/load-smoke-contract.sh
bash deploy/tests/selinux-contract.sh
bash deploy/tests/selinux-management-contract.sh
go run ./cmd/probe-support-gate verify \
  --support-root deploy/support --release v1.2.0 --require-zero-supported
```

发布构建器只接受 `--profile management`，`--profile full` 会失败，并且不得读取或构建 `probe-web`、`probe-agent`。management 包只携带运行所需部署资产；CI 构建器、历史整包入口以及含 Agent/游客构建或部署分支的 helper 都不得进入发布包。`v1.2.0` 标签工作流只上传未发布的 Actions 候选 Artifact，不自动公开 Release。一个 cell 只验证其指定的 IP 或 domain 入口；正式支持该 cell 前，必须在对应真实 systemd VM 完成该入口的全新安装、原生 Nginx/PostgreSQL 契约、冲突无修改、重启、升级、失败回滚、备份恢复和普通卸载保留数据。平台级正式支持要求 `amd64/ip`、`amd64/domain`、`arm64/ip`、`arm64/domain` 四格全部 supported；Docker 契约测试不能代替这些证据。

## 安全边界

- API 固定监听 loopback；PostgreSQL 不暴露公网。
- 管理入口同时要求 IP/CIDR 白名单、管理员 Session；写请求还要求 Origin 和 CSRF。
- 管理端未配置 Agent 集成时，Agent API、Token 和安装命令均失败关闭。
- 系统禁止 SSH/WebSSH、Shell、任意命令、文件管理、端口转发、反向隧道和远程自动升级。
- CPU、内存、硬盘和网络历史最多保留 5 分钟；探测数据最长保留 90 天。

## 文档入口

- [管理端安装](docs/operations/install.md)
- [管理端平台支持与验收矩阵](docs/operations/platform-support.md)
- [架构与产品边界](docs/design/architecture.md)
- [Agent 协议](docs/design/agent-protocol.md)
- [权限矩阵](docs/design/permissions.md)
- [Agent 部署（后续阶段）](docs/operations/agent-deployment.md)
- [备份与恢复](docs/operations/backup-restore.md)
- [长期约定](jiyi.md)
- [实施计划](jihua.md)
- [变动记录](biandong.md)
