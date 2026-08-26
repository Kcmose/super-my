# 管理端独立安装

本文只描述第一个产品“管理端”的安装。Agent 和访客前端必须在后续阶段使用各自的发行包与安装脚本，不能由本文的脚本代装。

> 发布状态：当前源码定义的是第一个独立管理端候选基线 `management v1.2.0`，但该 GitHub Release 尚未发布。历史 `full v1.1.0` 不是合法的 management 前序，因此 v1.2.0 的 `promotion_eligible=false`，60 个 cell 必须保持 candidate；第一个可晋级目标是 v1.2.1。多系统 ABI `probe-linux-systemd-v2` 只是一组精确候选分支，不改变这一状态。因此现在不能把示例地址当成可用的一键安装命令。推送 `v1.2.0` 标签只生成保留 14 天的 GitHub Actions 候选 Artifact，不会创建或公开 Release；标签本身不是发布批准。

## 1. 管理端包含什么

管理端产品由以下部分组成：

```text
probe-admin（独立构建的管理 SPA）
probe-api（管理、认证、查询、存储与后续 Agent API）
PostgreSQL 迁移与本机数据库配置
管理入口 Nginx 配置
Setup、systemd 与数据库备份资产
```

管理端 Release 不得包含：

```text
probe-web 构建物
probe-agent 二进制或源码
probe-agent.service
Agent 下载目录或占位目录
游客入口配置
```

管理 SPA 与 API 同属一个安装产品，但仍是两个构建产物；Nginx 提供静态文件并把同源 API 请求转发到 loopback 上的 Go API，API 不嵌入 SPA。

## 2. 当前平台实施矩阵

当前运行时 ABI 为 `probe-linux-systemd-v2`。架构只接受 Linux `amd64`、`arm64`；
PID 1 必须是 systemd。下表是安装器唯一接受的 15 个精确候选平台，不把
“脚本出现对应分支”写成正式支持：

| 精确平台 ID | `/etc/os-release` 对应系统 | systemd 下限 | 包管理 | 生命周期与当前证据 |
| --- | --- | ---: | --- | --- |
| `debian-9-systemd` | Debian 9 | 232 | `apt-get` | EOL；需 `--accept-eol`；candidate |
| `debian-10-systemd` | Debian 10 | 241 | `apt-get` | EOL；需 `--accept-eol`；真实远端 Shell/Go/交叉构建契约通过，完整 E2E 未完成 |
| `debian-11-systemd` | Debian 11 | 247 | `apt-get` | LTS 于 2026-08-31 结束；需 `--accept-eol`；candidate |
| `debian-12-systemd` | Debian 12 | 252 | `apt-get` | 2026-07-12 转入 LTS；需 `--accept-eol`；candidate |
| `debian-13-systemd` | Debian 13 | 257 | `apt-get` | 既有构建/适配基线，当前 Release 完整 E2E 未完成 |
| `ubuntu-18.04-systemd` | Ubuntu 18.04 | 237 | `apt-get` | EOL；需 `--accept-eol`；candidate |
| `ubuntu-20.04-systemd` | Ubuntu 20.04 | 245 | `apt-get` | 已退出标准维护；需 `--accept-eol`；candidate |
| `ubuntu-22.04-systemd` | Ubuntu 22.04 | 249 | `apt-get` | candidate，完整 E2E 未完成 |
| `ubuntu-24.04-systemd` | Ubuntu 24.04 | 255 | `apt-get` | candidate，完整 E2E 未完成 |
| `ubuntu-26.04-systemd` | Ubuntu 26.04 | 259 | `apt-get` | candidate，完整 E2E 未完成 |
| `centos-linux-7-systemd` | CentOS Linux 7 | 219 | `yum` | EOL；需 `--accept-eol`；SELinux/真实 VM 验收未完成 |
| `centos-linux-8-systemd` | CentOS Linux 8 | 239 | `dnf` | EOL；需 `--accept-eol`；SELinux/真实 VM 验收未完成 |
| `centos-stream-8-systemd` | CentOS Stream 8 | 239 | `dnf` | EOL；需 `--accept-eol`；SELinux/真实 VM 验收未完成 |
| `centos-stream-9-systemd` | CentOS Stream 9 | 252 | `dnf` | candidate；SELinux/真实 VM 验收未完成 |
| `centos-stream-10-systemd` | CentOS Stream 10 | 257 | `dnf` | candidate；SELinux/真实 VM 验收未完成 |

机器账本将上述 15 平台再按 2 个架构和 2 个入口拆为 60 个 cell，当前严格为
`60 candidate / 0 supported`。一个 cell 只代表指定 IP 或 domain 入口；同平台
四格全部 supported 才能汇总成平台级正式支持。

这些目标共享的管理产品契约仍是：本机 PostgreSQL 只能监听 loopback，使用
包归属可证明的原生 Nginx，IP 模式只提供 HTTPS `18455`，域名模式只配置一个
管理域名。每个 OS、版本、架构和入口模式都是独立验收单元，不能由另一个单元
的结果推断。

Rocky Linux、AlmaLinux、Alpine Linux、OpenRC 以及其他未列出的版本/派生系统
不在这份 ABI 矩阵；安装器应失败关闭。Probe 按 `/etc/os-release`、包管理器和
服务管理器分派，但不会仅凭 `apt-get`、`dnf`、`yum`、`apk` 或 `systemctl`
存在就认定系统受支持。完整状态定义和验收矩阵见
[管理端平台支持与验收矩阵](platform-support.md)。

### 安装器源码与单文件入口

为了分别处理各发行版系列的问题，仓库中的管理端 bootstrap 源码拆成：

```text
install/common.sh              通用验包、事务、回滚和 Setup 编排
install/platforms/debian.sh    Debian 9–13 精确适配
install/platforms/ubuntu.sh    Ubuntu 18.04–26.04 精确适配
install/platforms/centos.sh    CentOS Linux/Stream 精确适配
install/build-standalone.sh    确定性生成并核对根 install.sh
install.sh                     对外发布的完整单文件入口
```

根 `install.sh` 先把整份脚本解析完成，再读取 `/etc/os-release`，并用固定的
`debian`、`ubuntu`、`centos` 分派调用对应适配器。它不会把未验证的发行版字符串
拼成路径，也不会在服务器安装时从 GitHub `main` 或其他可移动地址下载、`source`
家族脚本。这样既允许各系列独立维护，又保留 `curl | bash` 只有一个同版本可信
脚本、下载被截断时不进入 `main` 的安全边界。

修改任一 `install/` 源文件后必须重新生成根脚本，并在提交或候选构建前检查一致性：

```bash
bash install/build-standalone.sh
bash install/build-standalone.sh --check
```

正式发布构建器会再次执行 `--check`；根脚本与拆分源码不一致时失败，不会暗中重写
候选来源。三套适配器只负责平台动作，不能设置 trap、改写通用事务状态或自行进入
主机变更阶段。

## 3. 安装前检查

用 root 登录或先执行 `sudo -i`。安装命令本身不依赖最小系统预装 `sudo`。

bootstrap 为了保证损坏 Release 或不兼容环境不会先触发包管理、建账号或改服务，
要求主机预先具备只读识别和安全验包工具。至少包括：

- Bash、Python 3 和可读取的系统 CA bundle（通常由 `ca-certificates` 提供）；
- 支持 TLS 1.2 的 `curl` **或** HTTPS `wget`，二者任有一个即可完成下载预检；
- `sha256sum`、coreutils、findutils、`grep`、`sed`、`awk`；
- `ip`/`ss`、util-linux（含 `flock`、`runuser`、`setpriv`）、procps、systemd 工具；
- deb 系的 `apt-get`/`dpkg-query`/`adduser`，或 RPM 系对应的 `yum`/`dnf`、
  `rpm`/`rpmkeys`/`useradd`。

缺项时脚本会列出前置项并在主机发生永久修改前退出，不会为了下载或验证 Release
先自动改机。极简镜像仍须逐个平台验收，不能把“标准安装通常自带”理解为任意
最小镜像都能一条命令安装。

目标机只下载、校验并运行预构建的 management bundle；不会克隆源码，也不会在
Debian 9–12、Ubuntu 18.04/20.04、CentOS 7 等旧版或延长维护目标上安装 Go/Node 后现场编译。所有平台
消费相同版本、相同架构的不可变字节，构建环境与运行环境彼此独立。

旧版、EOL 或仅延长维护的平台默认失败关闭。显式使用 `install --accept-eol` 不会恢复
上游安全维护。Debian/Ubuntu 候选会新增独立的
`/etc/apt/sources.list.d/probe-panel-runtime.list`，按精确版本选择发行版与 PGDG 的
live/archive，使用 `sourcelist/sourceparts` 隔离现有源，并把发行版和 PGDG 分别绑定到
已验证 keyring；不会改写用户的 `sources.list`。Debian 9 必须已有安全 HTTPS method，
其 arm64 官方 archive 又缺少 PostgreSQL 14，因此该架构当前明确拒绝。CentOS 候选会把
Vault/Stream、EPEL/Certbot 和 PGDG 14 写入专用
`/etc/yum.repos.d/probe-panel-runtime.repos`，所有仓库默认禁用；安装事务使用独立
`reposdir`、`--noplugins`、`--disablerepo='*'` 和精确 allowlist，不读取主机既有仓库。
CentOS、EPEL 与 PGDG key 均固定 SHA-256 和完整 OpenPGP 指纹，安装后的关键 RPM 还要
绑定到预期签名 key；EL8/EL9 的 PostgreSQL module 变更纳入失败回滚。这些候选契约已经
实现，但尚无真实 CentOS 双架构完整安装 E2E，不能据此描述为正式支持或生产可用。

管理端安装器会检查：

- `/etc/os-release`、CPU 架构和 systemd；
- Release 名称、外层 `SHA256SUMS`、安全 tar 成员和内层完整清单；
- 既有 Probe 文件、服务、状态和固定配置路径；
- Nginx 是否存在、配置是否能通过 `nginx -t`、目标端口是否冲突；
- PostgreSQL 是否可启动或复用，以及 `5432` 是否只监听 `127.0.0.1`/`::1`；
- Setup Socket、状态目录和 systemd 单元是否已经存在。

安装器只拥有名称明确的 Probe 路径，例如 `/srv/probe`、`/etc/probe-panel`、Probe systemd 单元和 `/etc/nginx/conf.d/probe-panel.conf`。它不会为了安装 Probe 停止、禁用、覆盖或删除无关的站点、数据库、证书或数据。无法确认归属时直接退出。

### CentOS 的额外候选门禁

CentOS Linux/Stream 分支已完成运行时路径、隔离受管仓、签名来源绑定和静态契约适配，
但尚未通过真实 CentOS 双架构 VM 的完整验收。SELinux Enforcing 下，Nginx 需要连接 loopback API `8080`；该端口
类型可能已由系统或其他服务共享，安装器不能无条件重标、删除已有标签或打开全局
权限。安全、可回滚的 SELinux port/fcontext/反向代理策略仍待闭环，所以不能把
CentOS candidate 当成已支持生产部署。

当前根安装器会在识别到 RPM 平台的 SELinux Enforcing 后，于包管理、建账号、服务
和永久目录修改前失败关闭。Permissive 只允许隔离候选测试；关闭 SELinux 或绕过
该门禁不是部署办法。仓库中的最小策略候选尚未进入正式 management bundle。

安装器也不会修改 firewalld zone、service 或 rich rule。管理员负责按入口模式开放
管理端口（IP 模式为 `18455/tcp`；域名模式还涉及 `80/443`），并负责保留已有业务
规则。关闭 SELinux 或清空防火墙不是验收方案。

### 已有 Nginx/PostgreSQL 的服务器

管理 IP 模式是当前共存路径：

- 现有 Nginx 必须先通过 `nginx -t`；
- 生成管理端片段和证书后，会把 Probe 链接临时加入现有配置树再执行一次组合 `nginx -t`，随后立即撤掉；只有组合配置无 upstream、限速区或 `18455 default_server` 冲突时才创建数据库角色并初始化管理员；
- `18455` 必须空闲；
- 安装只增加 Probe 自己的配置链接；
- 失败回滚只停 Probe API/备份单元并移除该链接，随后 `nginx -t` 成功才 reload；
- 不停止或禁用现有 Nginx、Certbot；
- PostgreSQL 对外监听时拒绝安装，不擅自修复或停止它。

管理域名模式目前仍用 Certbot standalone 完成 ACME，所以不能和正在占用 `80/443` 的 Nginx 共存。已有业务站点时请选择 IP 模式；不要手工停站点来绕过检查，除非已经单独评审停机窗口、证书与回滚。

端口占用、DNS 未就绪、现有 Nginx/Certbot 单元状态不兼容、数据库名或角色已存在等只读预检失败，不会把向导永久锁死：状态会退回 `configuring`，root 私有 Setup Socket 保持可用，页面清除密码并建立新会话，修正后才能显式重试。只有 `installed` 或 `recovery_required` 终态才会在 30 秒交接窗口后关闭 Setup Socket。组合 `nginx -t` 需要先生成 Probe 配置与证书，所以这一步失败仍进入 `recovery_required`，但发生在数据库角色、迁移和管理员初始化之前。只有开始接管或写入 Probe 正式资产后的失败才进入 `recovery_required`。

## 4. 发布后的一键安装形式

只有 `v1.2.0` 不可变 baseline Release、两种架构包和 `SHA256SUMS` 实际发布并通过候选资产验收后，安装命令才采用以下形态；这仍不表示任何 cell 已正式支持：

```bash
curl -fsSL --proto '=https' --tlsv1.2 \
  https://raw.githubusercontent.com/Kcmose/super-my/refs/tags/v1.2.0/install.sh \
  | bash
```

当前请勿执行这条命令：远端 `v1.2.0` 资产尚不存在。脚本默认寻找：

```text
probe-panel-management-v1.2.0-linux-amd64.tar.gz
probe-panel-management-v1.2.0-linux-arm64.tar.gz
SHA256SUMS
```

当前 `v1.2.0` 根安装器只接受 management，设置 `PROBE_PANEL_RELEASE_PROFILE=full` 会直接失败。历史 `full v1.1.0` 只能使用其不可变 `v1.1.0` 标签中的旧脚本和旧资产；当前脚本不会安装或迁移旧整包。

EOL 候选平台的参数形态是 `install --accept-eol`。若未来从标准输入执行已发布且已
校验来源的脚本，对应 Bash 参数传递形式为 `bash -s -- install --accept-eol`。当前
Release 尚未发布，请不要据此拼接或执行公网命令。

## 5. 首次安装向导

安装器不接收数据库密码、管理员密码或域名参数。成功安装 bootstrap 后，在自己的电脑创建 root SSH 转发：

```bash
ssh -N -o ExitOnForwardFailure=yes \
  -L 127.0.0.1:18080:/run/probe-panel-setup/setup.sock \
  root@<服务器IP>
```

保持 SSH 会话打开，再访问：

```text
http://127.0.0.1:18080/install
```

服务器没有 TCP `18080` 监听。Setup 只接受 systemd 提供的 root 私有 Unix Socket；父目录为 `root:root 0700`，Socket 为 `root:root 0600`。浏览器会在该通道内取得短期内存 Session 和 CSRF Token。

root-owned `PROBE_SETUP_PROFILE=management` 固定本次安装档案。浏览器可以省略 `profile`，但不能提交 `full` 把它改回旧整包。

## 6. 管理端向导字段

| 区域 | 字段 | 管理档案规则 |
| --- | --- | --- |
| PostgreSQL | 数据库名、用户名、两次密码 | 新的本机数据库/角色；密码 12–1024 UTF-8 字节 |
| 入口 | 服务器地址 | IP 模式使用规范、可路由的 IPv4/IPv6 |
| 入口 | 管理域名 | 留空为 IP 模式；填写一个裸 FQDN 为域名模式 |
| 不存在 | 其他产品入口 | 管理向导及其 API 请求没有游客或 Agent 入口字段 |
| TLS | 模式、邮箱 | IP 使用 `private_ca` 且邮箱为空；域名使用 `acme` 且邮箱必填 |
| 白名单 | IP/CIDR | 1–128 项；禁止 `/0`、重复项和带主机位 CIDR |
| 管理员 | 用户名、两次密码 | 创建首个管理员 |

向导只返回管理地址：

- IP：`https://<IP>:18455/login`；
- 域名：`https://<管理域名>/login`。

IP 模式生成只包含所选 IP SAN 的私有 CA/服务证书。浏览器需安全信任该 CA；不能用明文 HTTP 或关闭 TLS 校验替代。

## 7. 安装后的能力边界

管理端安装完成后：

- `probe-admin`、`probe-api`、PostgreSQL 和备份定时器可运行；
- 管理员可以创建节点、编辑节点信息和保存 Agent 采集参数；
- 管理界面所需的只读 `/api/v1/panel/*` 仍可同源使用；
- 未配置全局 Agent 入口时，系统状态返回 `agent.status=not_configured`；
- `/api/v1/agent/*` 不注册，签发安装命令和轮换 Agent Token 返回 `409 agent_not_configured`，不会生成秘密；
- 没有游客站点、Agent 下载站点或对应端口。

在后续 Agent 阶段完成明确的入口、TLS、安装器来源和公开 URL 配置后，才允许启用 Agent 注册/上报并在目标服务器单独安装 Agent。访客前端仍要最后单独安装。

## 8. 构建管理端发行包

正式构建必须在干净、固定到 `v1.2.0` 标签且远端标签一致的 Debian 13 仓库执行：

```bash
cd probe-api
bash deploy/scripts/build-release-bundles.sh \
  --profile management \
  --version v1.2.0 \
  --output-dir /var/tmp/probe-panel-management-release-v1.2.0
```

构建器只接受 `management`，传入 `--profile full` 会直接失败。它只构建 API、Setup 和管理 SPA，不会读取、安装依赖或构建相邻的游客/Agent 仓库。发布包采用部署资产白名单，不携带 CI 构建器、历史整包入口或任何 Agent/游客构建逻辑；包内运行时 helper 也是独立生成的 management-only 文件。构建器还必须证明包中不存在 `artifacts/web`、`artifacts/agent` 及 full 专用 Nginx/Finalizer 文件。

源码供应链采用固定快照：构建器禁用 Git replace，校验本地标签、精确提交和远端标签对象后，只从该提交读取一次归档；原始归档专用于打包，构建与测试在独立工作副本进行。每个构建/测试阶段后都会按路径、类型、权限模式和文件内容重新计算非生成源码的完整 SHA-256 摘要，任何持久污染都使构建失败。`RELEASE-MANIFEST` 生成前后还会重新读取远端标签，保证清单中的 `super_my_ref` 在产物组装时仍绑定同一个 `source_commit`。最终输出用原子 no-clobber 重命名发布，目标目录并发出现时失败，不覆盖既有资产。

固定 Debian 13 是构建供应链和可复现性约束，不是安装目标限制。同一版本只构建
一次双架构候选包；全部 15 个候选平台的 E2E 必须消费完全相同的外层与内层
SHA-256 已验证资产，不得按目标系统分别重建不同字节。

推送精确的 `v1.2.0` 标签时，GitHub Actions 只执行同一 management 构建并上传名为 `probe-panel-management-v1.2.0-candidate` 的候选 Artifact。工作流没有 `contents: write` 权限，也不创建 Draft、编辑 Release 或公开资产。候选包只用于隔离验收；v1.2.0 完成首次安装、回滚、备份恢复、卸载及候选资产门禁后，仍须人工复核才能另行创建不可变的 0-supported baseline。升级证据从 v1.2.1 开始，并必须来自同格不可变 v1.2.0。

## 9. 发布前验收

日常格式化、单元测试、静态检查和双架构交叉构建在专用远端 Linux 测试机执行；
正式发布资格作业必须在干净、受控的 Debian 13 构建环境用同一不可变源码提交全部
重跑并保存结果。当前管理端至少执行：

```bash
cd probe-api
find . -type f -name '*.go' -print0 | xargs -0 gofmt -w
GOMAXPROCS=1 go test -count=1 -p=1 ./...
GOMAXPROCS=1 go vet -p=1 ./...

cd ..
shellcheck install.sh install/common.sh install/build-standalone.sh \
  install/platforms/*.sh install/tests/*.sh \
  probe-api/deploy/scripts/*.sh probe-api/deploy/tests/*.sh
bash install/build-standalone.sh --check
bash install/tests/package-source-contract.sh
sh probe-api/deploy/tests/bootstrap-install-contract.sh
sh probe-api/deploy/tests/build-release-bundles-contract.sh
sh probe-api/deploy/tests/management-lifecycle-contract.sh
sh probe-api/deploy/tests/load-smoke-contract.sh
bash probe-api/deploy/tests/selinux-contract.sh
sh probe-api/deploy/tests/selinux-management-contract.sh

cd probe-api
go run ./cmd/probe-support-gate verify \
  --support-root deploy/support --release v1.2.0 --require-zero-supported

cd ../probe-admin
npm ci
npm test
npm run build
```

每个准备进入正式支持表的精确 OS/版本/架构/入口 cell，必须在对应的真实 PID 1
systemd VM 至少覆盖：

1. 全新系统完成该 cell 指定的 IP 或单管理域名安装、Setup、登录和正式入口关闭检查；
2. 与包归属可证明的原生 Nginx 站点及 loopback PostgreSQL 共存；
3. `18455`、`80/443`、外露 PostgreSQL、第三方 Web 栈和已有 Probe 资产等冲突
   在承诺的阶段失败且没有 apt、账号、服务或永久路径修改；
4. 真实重启后正式服务恢复且 Setup 不重新开放；
5. 从同平台、同架构、同入口的不可变前序 management 版本完成升级，并验证失败
   回滚及数据库前向迁移边界；首个有效目标 v1.2.1 从 v1.2.0 baseline 升级；
6. Finalizer 中途失败后原站点继续运行，Probe 自有配置被准确清理；
7. PostgreSQL 备份通过校验并完成隔离恢复；
8. 普通卸载保留配置、数据库和备份，且不删除无关数据；
9. management 包没有 Agent/访客产物，未配置 Agent 时路由、Token 与 UI 状态
   全部失败关闭。

Docker/OCI 契约测试不能代替真实 systemd VM、真实重启和生命周期验收。未取得
某一精确单元的全部证据前，只能称“源码/契约已实现”或 `candidate`，不能称
“正式支持”或“可生产部署”。不得把推送标签、Actions 构建成功或下载候选
Artifact 写成已经发布；当前工作流不会替操作者公开 `v1.2.0`。

当前额外证据为：真实远端 Debian 10 已通过 standalone 生成器检查、全量 Bash 语法、
ShellCheck 0.11.0、package-source/bootstrap/release-builder/lifecycle/load/SELinux 契约、
support gate、Go 全包测试与 vet，以及 API/Setup 的 amd64/arm64 静态交叉构建。未读取或
修改用户已有 APT source 的只读网络探测覆盖 Debian/Ubuntu 20 个 OS×架构单元：基础、
updates/security 索引均可达，除安装器已明确拒绝的 Debian 9 arm64 外，19 个 PGDG
索引都包含精确 PostgreSQL 14。它尚未完成本节列出的完整安装、Setup、重启与生命周期
E2E，因此仍是 candidate 网络/契约证据，不能外推为任何单元的正式支持。
package-source 契约已经覆盖 CentOS 隔离 `reposdir`、精确 allowlist、Vault/Stream/EPEL/
PGDG 14、固定 key 与 RPM 来源绑定；远端只读探测也确认 10 个 CentOS OS×架构单元的
仓库元数据与 key 端点可达，并据此修正一个 CentOS 8+/Stream key URL 哈希不匹配问题，
但没有执行真实 CentOS 双架构 yum/dnf 安装。CentOS 还必须在
SELinux Enforcing 的真实 VM 中验证端口标签、Nginx 反向代理权限和失败回滚；
firewalld 始终由管理员管理。

## 10. 安装后的管理端生命周期

首次 Setup 完成前可查看 bootstrap 状态：

```bash
bash install.sh status
```

Setup 成功后，同一管理端产品提供独立的只读校验、目标 Release 升级和普通卸载：

```bash
bash install.sh validate
bash install.sh upgrade
bash install.sh uninstall
```

EOL 平台升级仍须显式附加 `--accept-eol`。升级入口只接受目标版本已经发布且外层、
内层 SHA-256 均通过的 management bundle；它先验证现有主机和活动发布，再由包内
`install-release.sh` 创建备份、执行前向迁移并原子切换 API、管理 SPA 和 migrations，
不会构建或触碰 Agent、访客前端，也不会切换 ingress 模式。

普通 `uninstall` 会停用并删除 Probe 自有的 API/备份 unit、三个活动发布链接和
Nginx include，再移除 bootstrap 程序；保留 `/etc/probe-panel`、
`/var/lib/probe-panel`、`/var/backups/probe-panel`、PostgreSQL 数据库和未激活的
发布目录，不卸载或停止共享 Nginx/PostgreSQL。任一已标记 bootstrap 目录删除失败
会明确返回失败，不能打印完整成功。`purge` 仍不实现。

生产数据库恢复使用安装在本机、持有部署维护窗口的协调器：

```bash
/usr/local/lib/probe-panel/restore-management.sh \
  --confirm-database probe \
  /var/backups/probe-panel/postgres/daily/probe-YYYYMMDDTHHMMSSZ.dump
```

它会先验证当前管理端，停止并临时禁用 API 与备份 timer，以 `probe-api` 低权限账号
校验/恢复归档，再使用当前不可变 API 执行前向迁移和完整运行态复验。恢复或迁移失败
时 API 与 timer 保持 stopped/disabled，必须人工检查后再恢复服务。

上述入口是源码合同；在 `v1.2.0` 不可变 Release 尚未公开前，不应拼接公网安装或
升级命令，也不能把契约实现写成已经发布或已经正式支持。
