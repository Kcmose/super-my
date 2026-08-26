# 管理端平台支持与验收矩阵

本文只定义第一个独立产品“管理端”的 Linux 安装平台范围。这里的管理端是
`probe-admin` 管理界面与 `probe-api` API 的同一安装产品。安装脚本不会安装
Agent，也不会安装访客前端；后两者仍使用各自的 Release、安装脚本和验收流程。

`management v1.2.0` 当前仍未发布。它是第一个独立 management candidate baseline，
历史 full v1.1.0 不是合法前序，因此 `promotion_eligible=false`，60 个 cell 不可晋级。
第一个可晋级目标是 v1.2.1；本文、安装器中的平台分支以及候选构建均不构成可用性
或生产支持承诺，机器可读账本当前明确记录 **60 candidate、0 supported**。

## 1. `probe-linux-systemd-v2` ABI

当前多系统运行时 ABI 为 `probe-linux-systemd-v2`。它不是 Release 版本，也不是
正式支持声明，而是安装器、management bundle 与 Finalizer 之间的精确适配契约：

bootstrap 源码按 `common + Debian + Ubuntu + CentOS` 四部分维护，根 `install.sh`
由固定生成器合成为 standalone 入口。运行时只根据经过数据解析的精确 `ID` 选择
内嵌适配器；不会依据 `ID_LIKE` 猜测，也不会联网取得家族脚本。源码拆分本身只是
降低平台逻辑耦合，不会把任何 candidate 自动提升为正式支持。

- CPU 架构只接受 Linux `amd64`、`arm64`；PID 1 必须是 systemd；
- 发行版必须与下表某个 `/etc/os-release` 映射精确匹配，不能从相邻版本或派生
  发行版推断；
- deb 系使用 `apt-get`、`postgresql.service`、`/usr/bin` PostgreSQL 工具和
  `certbot.timer`；
- CentOS Linux 7 使用 `yum`，其余 CentOS 单元使用 `dnf`；RPM 分支使用 PGDG 14
  的 `postgresql-14.service`、`/usr/pgsql-14/bin` 和 `certbot-renew.timer`；
- 系统版本选择 legacy/modern systemd 资产及 classic/legacy/modern Nginx 方言；
- 数据库服务器必须精确为 PostgreSQL 14.x，不能为了旧发行版降低数据库版本，也不能
  在未经大版本迁移设计时自动切到 15+；
- 安装对象固定为 management profile，包内不能包含 `probe-agent`、`probe-web`
  或它们的服务、下载目录和入口配置。

“ABI 分支存在”只说明代码可以进入相应候选路径。只有某个精确
OS/版本/架构/入口模式完成全部真实 VM 验收，才能单独升级为正式支持。

## 2. 精确 15 平台矩阵

| 精确平台 ID | 系统与 systemd 下限 | 包管理 | systemd / Nginx 档案 | 生命周期 | 当前地位 |
| --- | --- | --- | --- | --- | --- |
| `debian-9-systemd` | Debian 9 / 232 | `apt-get` | legacy / classic | EOL，需 `--accept-eol` | candidate，完整 E2E 未完成 |
| `debian-10-systemd` | Debian 10 / 241 | `apt-get` | legacy / legacy | EOL，需 `--accept-eol` | candidate；真实远端 Shell/bootstrap 契约通过，完整 E2E 未完成 |
| `debian-11-systemd` | Debian 11 / 247 | `apt-get` | legacy / legacy | LTS 于 2026-08-31 结束；需 `--accept-eol` | candidate，完整 E2E 未完成 |
| `debian-12-systemd` | Debian 12 / 252 | `apt-get` | modern / legacy | 2026-07-12 转入 LTS；需 `--accept-eol` | candidate，完整 E2E 未完成 |
| `debian-13-systemd` | Debian 13 / 257 | `apt-get` | modern / modern | 构建/适配基线 | candidate；v1.2.0 完整 E2E 未完成 |
| `ubuntu-18.04-systemd` | Ubuntu 18.04 / 237 | `apt-get` | legacy / legacy | EOL，需 `--accept-eol` | candidate，完整 E2E 未完成 |
| `ubuntu-20.04-systemd` | Ubuntu 20.04 / 245 | `apt-get` | legacy / legacy | 已退出标准维护；需 `--accept-eol` | candidate，完整 E2E 未完成 |
| `ubuntu-22.04-systemd` | Ubuntu 22.04 / 249 | `apt-get` | modern / legacy | 常规候选层 | candidate，完整 E2E 未完成 |
| `ubuntu-24.04-systemd` | Ubuntu 24.04 / 255 | `apt-get` | modern / legacy | 常规候选层 | candidate，完整 E2E 未完成 |
| `ubuntu-26.04-systemd` | Ubuntu 26.04 / 259 | `apt-get` | modern / modern | 常规候选层 | candidate，完整 E2E 未完成 |
| `centos-linux-7-systemd` | CentOS Linux 7 / 219 | `yum` | legacy / classic | EOL，需 `--accept-eol` | candidate；SELinux/真实 VM 未验收 |
| `centos-linux-8-systemd` | CentOS Linux 8 / 239 | `dnf` | legacy / legacy | EOL，需 `--accept-eol` | candidate；SELinux/真实 VM 未验收 |
| `centos-stream-8-systemd` | CentOS Stream 8 / 239 | `dnf` | legacy / legacy | EOL，需 `--accept-eol` | candidate；SELinux/真实 VM 未验收 |
| `centos-stream-9-systemd` | CentOS Stream 9 / 252 | `dnf` | modern / legacy | 常规候选层 | candidate；SELinux/真实 VM 未验收 |
| `centos-stream-10-systemd` | CentOS Stream 10 / 257 | `dnf` | modern / modern | 常规候选层 | candidate；SELinux/真实 VM 未验收 |

旧版、EOL 或仅延长维护的平台默认失败关闭。`install --accept-eol` 只记录操作者明确
接受风险，不会提供或恢复厂商安全维护。Debian/Ubuntu 候选会创建独立受管 APT source，
按版本选择发行版 live/archive 与 PGDG live/archive；所有基础源显式绑定发行版 keyring，
PGDG key 同时固定 SHA-256 和完整 OpenPGP 指纹，包管理只读取该 source，不覆盖用户
已有配置。Debian 9 缺少安全 HTTPS method 时在源变更前拒绝，Debian 9 arm64 因官方
archive 不提供 PostgreSQL 14 而不可用。CentOS 候选已实现专用 `reposdir` 与精确
allowlist，按平台/架构固定 Vault/Stream、EPEL 和 PGDG 14；宿主仓库及插件在安装事务中
全部禁用。CentOS、EPEL 与 PGDG key 同时固定 SHA-256 和完整 OpenPGP 指纹，关键 RPM
还要绑定到预期签名 key；EL8/EL9 PostgreSQL module 状态纳入失败回滚。该实现只通过了
候选源码/契约层检查，尚无真实 CentOS 双架构完整安装 E2E；SELinux Enforcing 仍在任何
主机变更前失败关闭，因此没有改变支持账本。

Rocky Linux、AlmaLinux、Alpine Linux、OpenRC、非 systemd 环境及表中没有列出的
版本/派生发行版不属于 ABI v2。系统中存在 `apt-get`、`dnf`、`yum`、`apk` 或
`systemctl` 不能作为支持证据。

## 3. 机器可读状态账本与 gate

正式状态不能由 README、表格文字、CI 绿色结果或一次手工安装决定。仓库中的状态
真源由三部分组成：

- `probe-api/deploy/support/policy-v1.json` 固定源仓库、精确晋级谱系、ABI、15 个
  精确平台、`amd64`/`arm64`、`ip`/`domain`、必测场景和额外门禁；当前唯一谱系是
  `v1.2.1 <- v1.2.0`，未列入谱系的版本不能晋级；
- `probe-api/deploy/support/releases/v1.2.0.json` 显式列出
  `15 × 2 × 2 = 60` 个单元及其人工审核状态；
- `probe-support-gate` 严格解析上述 JSON，拒绝未知字段、重复键、尾随值、缺失、
  重复、扩展、乱序单元和路径中的符号链接，并校验证据文件、精确 GitHub Release
  Artifact URL、SHA-256 和 release subject 绑定。

当前未发布的 v1.2.0 ledger 中 60 个单元全部是 `candidate`，没有任何
`supported` 单元，也没有平台级正式支持。发布构建会执行：

```bash
cd probe-api
go run ./cmd/probe-support-gate verify \
  --support-root deploy/support \
  --release v1.2.0 \
  --require-zero-supported
```

`--require-zero-supported` 是当前 v1.2.0 候选构建的第二道门禁。即使移除该参数，
policy 的精确 `promotion_lineage` 与 release ledger 的 `promotion_eligible=false`
也会拒绝 v1.2.0 中的任何 `supported`。未知版本不会因为版本号更高而自动获得晋级
资格；未来版本必须先经过评审并显式加入谱系，不能只改文档措辞。

未来检查包含 `supported` 的 ledger 时，审核环境必须同时提供可信 tag commit 和
正式发布资产目录，二者不能只给一个：

```bash
go run ./cmd/probe-support-gate verify \
  --support-root deploy/support \
  --release v1.2.1 \
  --release-assets /reviewed/release-assets \
  --source-commit 0123456789abcdef0123456789abcdef01234567 \
  --upgrade-from-assets /reviewed/v1.2.0-assets \
  --upgrade-from-source-commit 89abcdef0123456789abcdef0123456789abcdef
```

两个目录都必须包含各自版本精确命名的 amd64、arm64 management tarball。gate 会
流式重算目标包和升级前序包的外层 SHA-256，从每个包唯一的
`BUNDLE-SHA256SUMS` 重算内层清单 SHA-256，并解析唯一 `RELEASE-MANIFEST`，把
profile、版本、架构、源仓库、tag ref 与可信 commit 绑定；账本和 evidence 同步伪造
同一个值也不能绕过可信输入。v1.2.0 没有升级前序，其 0-supported baseline 资产
复核只提供目标 `--release-assets` 与 `--source-commit`。

完整证据不会自动把 candidate 提升为 supported。每个单元只有在 ledger 中由人工
明确声明 `supported`，并且其 evidence SHA、可信 source commit、实际外层资产 SHA、
实际内层 manifest SHA 和全部场景证据均通过 gate 后才成立。同一架构的 IP/domain 证据还
必须引用同一不可变 management bundle。平台级汇总要求该平台的
`amd64/ip`、`amd64/domain`、`arm64/ip`、`arm64/domain` 四个单元全部 supported；
不能用一个单元外推整个发行版。

## 4. 当前已有验证证据

截至 2026-08-26，真实远端 Debian 10/systemd 241 主机已经通过 standalone 生成器
`--check`、全量 Shell 语法、ShellCheck 0.11.0、package-source/bootstrap/release-builder/
lifecycle/load/SELinux 契约、Go 1.23.12 全包测试与 `go vet`、support gate，以及
API/Setup 的 amd64/arm64 静态 ELF 交叉构建。只读网络探测没有读取或修改主机既有
source：Debian/Ubuntu 20 个 OS×架构单元的基础、updates/security 索引均可达，19 个
可安装单元的 PGDG 索引都包含精确 `postgresql-14`；Debian 9 arm64 的 PGDG
`binary-arm64` 索引确实不存在，因此安装器在改机前失败关闭。Buster 的隔离 APT
解析还实际得到 `postgresql-14`/`postgresql-client-14` 候选版本
`14.13-1.pgdg100+1`。这些结果证明 ABI v2 的 legacy Shell、平台映射、受管软件源、
management 包契约和 Go 产物能在该旧用户空间运行；它们没有安装产品，也没有执行
Setup、重启或生命周期 E2E，因此 `debian-10-systemd` 仍是需要 `--accept-eol` 的
candidate。

同一轮 package-source 契约也验证了 CentOS 专用 `reposdir`、精确 allowlist、各版本/
架构的 Vault/Stream/EPEL/PGDG 14 映射、固定 key 哈希/完整指纹、RPM 签名来源绑定，
以及 EL8/EL9 module 状态回滚入口。10 个 CentOS OS×架构单元的五组仓库元数据和
所需 key URL 均经远端只读探测可达；探测还发现并修正了 CentOS 8+/Stream 官方 key
URL 与固定字节哈希不一致的问题。该结果仍只是端点、源码与契约证据，没有在真实
CentOS 双架构主机执行 yum/dnf 求解或安装，不能外推成运行时或正式支持证据。

此前 Debian 12/13、Ubuntu 22.04/24.04 的隔离 `amd64` 根文件系统验证过真实仓库
包布局、Nginx 方言及 unit 静态解析。这些历史静态证据仍可用于回归，但不能代替
真实 PID 1 systemd 下的 Setup、重启、升级、回滚、备份恢复和卸载。

当前没有任何一个 ABI v2 精确单元完成本文件的全部 E2E 门槛；CentOS 也尚未在
真实 CentOS VM 上验收。因此文档只能写“candidate”“契约通过”或“既有基线”，
不能声称 15 个平台已经全部支持或可用于生产。

这些已有结果没有改变机器账本；当前汇总仍是 60 candidate、0 supported。

## 5. 构建机、旧系统与 bootstrap

正式 management 候选资产固定在干净、受控的 Debian 13 构建环境生成。同一版本
只构建一次 `amd64`/`arm64` bundle；所有候选目标必须消费相同的外层和内层
SHA-256 已验证字节。

Debian 9–12、Ubuntu 18.04/20.04、CentOS 7 等旧版或延长维护目标机只下载、校验并运行预构建的
management bundle。目标机不会克隆源码，也不会安装 Go、Node 后现场编译；旧系统
兼容依靠预构建 ELF、Shell 兼容层和按平台选择的运行时资产，而不是降低构建工具链。

在包管理、建账号或修改服务前，bootstrap 要求目标机已有：

- Bash、Python 3、系统 CA bundle；
- 支持 TLS 1.2 的 `curl` 或 HTTPS `wget`（任一个即可）；
- `sha256sum`、coreutils/findutils、`grep`、`sed`、`awk`；
- iproute/iproute2、util-linux、procps/procps-ng 和 systemd 工具；
- 该平台原生包管理、包归属校验与用户管理命令。

缺项会在永久修改前列明并退出。极简镜像不能仅凭发行版名称视为已满足前置条件。

## 6. CentOS 的 SELinux 与 firewalld 边界

CentOS 分支当前仍有独立的安全验收阻塞项。正式 API 监听 loopback `8080`，由
Nginx 反向代理；SELinux Enforcing 下，`8080` 的端口类型可能已被系统或其他服务
共享。安装器不能把共享标签当成 Probe 独占，也不能无条件重标端口、删除既有标签
或打开过宽的全局权限。Nginx 连接权限、Probe 路径 fcontext、端口策略、失败回滚
和卸载保留边界必须形成可审计方案并在真实 CentOS Enforcing VM 验证后，CentOS
单元才有资格升级状态。关闭 SELinux 不是验收方案。

在该闭环完成前，根安装器检测到 SELinux Enforcing 会在包、账号、服务或永久路径
修改前失败关闭；Permissive 只可用于隔离候选测试。仓库中的最小 policy/helper
只通过静态契约，尚未进入正式 management bundle，不能被描述为已完成适配。

firewalld 始终由服务器管理员管理。安装器不会自动修改 zone、service 或 rich
rule；管理员需根据 IP 模式 `18455/tcp` 或域名模式 `80/443` 管理规则，并保证不
破坏已有业务。清空防火墙也不是验收方案。

## 7. 每个 cell 的正式支持门槛

每一个准备提升的精确 OS/版本/架构/入口 cell，都必须在隔离的 full-system VM
中使用与账本绑定的同一不可变 management bundle，保存以下全部 `pass` 证据：

1. `fresh`：完成该 cell 指定的 IP 或 domain 全新安装、Setup、管理员登录和 Setup
   正式关闭；
2. `coexistence`：IP cell 必须验证“活动原生 Nginx + loopback PostgreSQL”共存；
   domain cell 使用 Probe 独占 80/443 的入口契约，不能把活动原生 Nginx 当成可共存；
3. `conflict` 与 `no_mutation`：端口、外露 PostgreSQL、第三方 Web 栈、部分 Probe
   资产或归属不明路径必须在承诺边界失败，且无包、账号、服务或永久路径修改；
4. `reboot`：真实重启前后 boot ID 必须不同，并复验 API、Nginx、PostgreSQL、备份
   timer 和 Setup 关闭状态；
5. `upgrade`：从同平台、同架构、同入口的不可变前序管理版本升级，并记录迁移和
   失败回退边界；首个有效目标 v1.2.1 必须从 v1.2.0 baseline 升级，v1.2.0 本身
   不可用历史 full 版本伪造这项证据；
6. `fault`：注入安装或 Finalizer 失败，验证原业务和 Probe 自有资产边界；
7. `backup_restore`：生成、校验并在隔离环境完成 PostgreSQL 恢复；
8. `uninstall`：普通卸载保留配置、TLS、数据库、备份和无关系统资产；
9. EOL cell 额外通过 `eol_repository`，保存可信仓库、密钥和风险接受证据；
10. CentOS cell 额外通过 `selinux_enforcing`，环境状态必须是 Enforcing，并验证策略、
    反向代理、失败回退和卸载边界。Permissive 或 Disabled 不能代替该证据。

evidence 还必须记录 full-system VM image 与 `/etc/os-release` 哈希、systemd PID 1、
精确机器架构、真实重启 boot ID、不可变 GitHub Release 证据资产及其 SHA-256 和
审核人。短期 GitHub Actions artifact、容器、伪重启或与 ledger 不同的 bundle
不能通过 gate。

Docker/OCI 容器可以用于 OS 解析、包映射和无副作用契约测试，但不能替代真实
systemd VM。容器中的 `systemctl`、伪重启、无 SELinux 环境或只运行静态二进制，
都不能作为正式支持证据。

## 8. 状态措辞

- **识别/规划**：只有设计或拒绝分支，不可安装。
- **candidate/候选**：已进入机器矩阵且已有或正在实现适配代码；单元可能仍被缺包、
  安全源或宿主策略阻塞，只有消除其明确 blocker 后才可进入隔离验收，不承诺生产支持。
- **契约通过**：某组静态或 Shell 契约已通过，只覆盖其明确列出的范围。
- **既有基线**：用于保持已实现路径和回归行为，不自动免除当前 Release 门禁。
- **正式支持**：精确矩阵单元已经完成本文件全部门槛，并随不可变 Release 保存证据。

README、安装文档和 Release 说明只能把最后一种写成“正式支持”。候选 Artifact、
GitHub Actions 构建成功、平台分支存在、Debian 10 契约通过或一次手工安装成功，
都不能自行改变状态。
