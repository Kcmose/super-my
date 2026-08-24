# Debian 13 生产安装

推荐方式是在一台干净、专用的 Debian 13 主机上运行 GitHub 固定版本的一键安装器，再通过 SSH 隧道完成浏览器向导。目标服务器只安装运行时组件，不安装 Go、Node.js 或 npm。Windows 本地目录仍只保存源码和文档；源码构建和手工发布见本文末尾的高级流程。

## 1. 一键安装前准备

一键安装仅支持 Debian 13 的 Linux `amd64`/`arm64`。执行前确认：

- 使用全新或已人工清点的专用主机，并先登录 root 或执行 `sudo -i` 进入 root Shell。后续单行命令直接调用 `bash`，不依赖最小 Debian 预装 `sudo`。安装器发现既有/残留 Probe Panel 文件或 systemd 单元会拒绝覆盖；发现正在运行的 Nginx 也会拒绝继续。
- 入口二选一：三个域名全部留空，自动使用服务器 IP 的固定 HTTPS 端口；或准备三个不同且互不包含的真实域名。不能只填其中一两个域名。
- IP 模式默认端口为游客 `18453`、Agent `18454`、管理 `18455`，必须在云安全组、主机防火墙和 NAT 中按需放行；向导会显示自动检测的服务器 IP，也允许在 NAT 场景改成实际对外 IPv4/IPv6。三个端口不能被其他进程占用。
- 域名模式要求三个 DNS `A`/`AAAA` 都已解析到服务器，公网 TCP `80/443` 可达且未被占用；ACME HTTP-01 期间不要让 CDN 或其他 Web 服务接管请求。
- 服务器能通过 HTTPS 访问 GitHub Release、GitHub Raw、Debian 软件源和 ACME 服务，并能从你的电脑通过 SSH 登录。网络需要代理时可临时使用部署环境自己的 SOCKS5 或 HTTP 代理，但不要把内网代理地址写入生产配置、公开仓库或日志。

安装器会用 APT 安装以下运行时依赖：

```text
ca-certificates  curl  python3  nginx  postgresql  postgresql-client
certbot          iproute2  util-linux
```

## 2. 执行一键安装

使用固定且不可变的服务端 `v1.1.0` 标签：

```bash
curl -fsSL --proto '=https' --tlsv1.2 \
  https://raw.githubusercontent.com/Kcmose/super-my/refs/tags/v1.1.0/install.sh \
  | bash
```

脚本会识别架构，下载对应的预编译 GitHub Release 和 `SHA256SUMS`，校验 SHA-256 后安全解包。它不会从 `main` 构建，也不会接收数据库密码、管理员密码或域名参数。安装过程中 Nginx 保持停止，PostgreSQL 只允许本机监听。成功后终端直接显示 SSH 隧道和浏览器地址，不生成、不保存也不要求安装码。

### 已安装 v1.0.0、但尚未完成向导

若服务器上已有旧版 `v1.0.0` 首次安装服务，直接再次执行默认 `install` 会拒绝覆盖并提示迁移。无论旧安装码仍在、已经过期还是已经丢失，都不要手工删除状态文件；改用同一个固定 `v1.1.0` 脚本的显式迁移命令：

```bash
curl -fsSL --proto '=https' --tlsv1.2 \
  https://raw.githubusercontent.com/Kcmose/super-my/refs/tags/v1.1.0/install.sh \
  | bash -s -- migrate-bootstrap
```

迁移只接受能够逐项证明为官方 `v1.0.0` 脚本创建、状态为 `pending` 或 `configuring`、且从未进入正式部署的 bootstrap。脚本会复验旧 Release 的内部 SHA-256 清单，以及活动 binary、Setup UI、systemd 单元、环境文件、状态和旧安装码记录；只要发现 `finalizing`、`installed`、`recovery_required`、正式 API/Nginx/前端文件、Finalize 请求/结果、未知文件或任何混合版本，就会失败关闭，不会猜测或覆盖。

严格复验通过后，脚本先在旧服务仍可用时下载并验证不可变 `v1.1.0` Release，再短暂停止旧 Setup/Finalizer、执行第二次稳定状态复验，并在备份保护下切换 binary、UI、环境和单元，把首次状态安全重置为 `pending`，启动 root 私有 Unix Socket。旧安装码记录只会在新服务通过 Socket 权限、无 TCP 18080 和 HTTP readiness 检查后删除；旧浏览器 Session 一律失效。普通错误会尽量回滚到原 bootstrap；旧状态为 `configuring`，或旧安装码在迁移期间已经过期时，旧服务无法安全重启，因此回滚后保持禁用，应修复报错后重新执行 `migrate-bootstrap`，不能手工启动旧服务。若磁盘或 systemd 错误导致自动回滚本身不完整，脚本会保持服务禁用，并明确打印保留在 root-only `/var/tmp/probe-panel-bootstrap.*` 下的恢复副本路径，不会继续启动混合版本。

## 3. 通过 SSH 隧道打开首次向导

在你自己的电脑执行，替换服务器地址；SSH 端口或用户不同时按实际修改：

```bash
ssh -N -o ExitOnForwardFailure=yes \
  -L 127.0.0.1:18080:/run/probe-panel-setup/setup.sock \
  root@<服务器IP>
```

保持该 SSH 会话打开，然后在本机浏览器访问：

```text
http://127.0.0.1:18080/install
```

服务器没有 TCP 18080 监听。SSH 以 root 身份连接服务器上的 `/run/probe-panel-setup/setup.sock`；其父目录为 `root:root 0700`，Socket 为 `root:root 0600`。浏览器打开页面后自动换取只保存在当前页面内存中的 15 分钟 Session 和 CSRF Token；刷新、响应丢失或 setup 服务重启时，`pending/configuring` 状态可自动安全重签。隧道必须显式绑定本机 `127.0.0.1`，在可信电脑使用，完成后关闭。若 SSH 服务器禁用了 StreamLocal 转发，安装必须失败关闭，不能退回服务器 TCP 或公网 Setup 页面。

## 4. 首次向导字段

向导提交的是一个严格 JSON 对象；未知字段、重复字段、缺少字段、尾随数据以及超过 64 KiB 的正文都会被拒绝。首版字段固定如下：

| 区域 | 字段 | 规则 |
| --- | --- | --- |
| 本机 PostgreSQL | `database.mode` | 固定为 `local`，V1 不接受远程数据库配置 |
| 本机 PostgreSQL | `database.name`、`database.username` | 小写 PostgreSQL 标识符；不使用 `postgres`、`template0`、`template1` 等保留值 |
| 本机 PostgreSQL | `database.password`、`password_confirmation` | 两次一致；12–1024 UTF-8 字节；禁止 NUL、CR/LF 和其他控制字符 |
| 服务器地址 | `network.address` | IP 模式必填规范 IPv4/IPv6；默认使用安装器从默认路由检测的地址，可在 NAT 场景覆盖；域名模式必须为空 |
| 三个入口 | `domains.panel`、`domains.admin`、`domains.agent` | 三项全部为空即 IP 模式；三项全部填写即域名模式，必须为不同且互不包含的小写裸 FQDN；部分填写拒绝 |
| TLS | `tls.mode`、`tls.email` | IP 模式固定 `private_ca` 且邮箱为空；域名模式固定 `acme` 并要求证书通知邮箱 |
| 管理白名单 | `allowlist` | 游客面板与管理面板共用；1–128 个 IPv4/IPv6 或规范 CIDR；裸 IP 自动转成 `/32` 或 `/128`；禁止 `/0`、重复项和带主机位 CIDR |
| 首个管理员 | `administrator.username` | 非空、无首尾空白或控制字符，最长 128 个字符 |
| 首个管理员 | `administrator.password`、`password_confirmation` | 两次一致；12–1024 UTF-8 字节 |

IP 模式由 Finalizer 生成独立私有 CA 和只包含所选 IP SAN 的服务证书，不调用 DNS、Certbot 或 TCP 80；浏览器需要信任生成的 CA，Agent 安装器通过固定 `ca.pem` 和 SHA-256 指纹严格验证。域名模式才会通过 ACME HTTP-01 取得公信证书，并在完成后由 Nginx 接管 80/443。

## 5. 临时 Setup API 契约

Setup API 属于独立的 `probe-setup` 进程，不是正式 `probe-api serve` 的公开路由。它只接受 systemd 传入的 root 私有 Unix Socket，不创建任何 TCP listener；本机浏览器仍通过 SSH 转发使用 `http://127.0.0.1:18080`。连接必须来自经过校验的 AF_UNIX root peer，`Host` 只能为 `127.0.0.1:18080` 或 `localhost:18080`。浏览器 POST 的 `Origin` 必须精确匹配这两个本机 Origin；GET 可不带 Origin，但带了也必须匹配。全部响应使用 `Cache-Control: no-store`。

状态机只有五态：

| `status` | 含义 |
| --- | --- |
| `pending` | 等待浏览器通过 root 私有 Socket 自动建立会话 |
| `configuring` | 已建立过安装会话；允许重签，等待提交配置 |
| `finalizing` | 严格校验完成，独立的特权 Finalizer 正在异步执行 |
| `installed` | 安装成功；响应同时包含 `admin_url`，随后临时服务关闭 |
| `recovery_required` | 失败关闭；不能重新打开首次注册，必须由服务器管理员恢复 |

三个接口及精确凭据头如下：

| 方法与路径 | 必需请求头 | 请求正文 | 成功响应 |
| --- | --- | --- | --- |
| `GET /api/v1/setup/status` | 无；浏览器自动发送的 `Host`/`Origin` 仍受上述限制 | 无 | `200 {"status":"...","defaults":{...}}`；安装成功时另含 `admin_url` |
| `POST /api/v1/setup/session` | `Content-Type: application/json`、允许的 `Origin` | 精确空对象 `{}` | `200 {"session_token":"<64hex>","csrf_token":"<64hex>","expires_at":"<RFC3339>","defaults":{...}}` |
| `POST /api/v1/setup/complete` | `Content-Type: application/json`、允许的 `Origin`、`X-Probe-Setup-Session: <session_token>`、`X-CSRF-Token: <csrf_token>` | 下方严格对象 | `202 {"status":"finalizing"}`；随后轮询 status |

`complete` 不接受 `Authorization: Bearer` 代替 `X-Probe-Setup-Session`。完整正文结构为：

```json
{
  "database": {
    "mode": "local",
    "name": "probe",
    "username": "probe",
    "password": "<至少12字节>",
    "password_confirmation": "<与password一致>"
  },
  "network": {
    "address": "203.0.113.20"
  },
  "domains": {
    "panel": "",
    "admin": "",
    "agent": ""
  },
  "tls": {
    "mode": "private_ca",
    "email": ""
  },
  "allowlist": ["203.0.113.25", "2001:db8:1234::/48"],
  "administrator": {
    "username": "admin",
    "password": "<至少12字节>",
    "password_confirmation": "<与password一致>"
  }
}
```

域名模式把 `network.address` 设为空字符串、填写三个域名，并提交 `{"mode":"acme","email":"operations@your-domain.tld"}`。`defaults` 固定返回服务端检测的 `server_ip` 以及 `panel_url`、`agent_url`、`admin_url` 三个 HTTPS IP 端口地址，前端不得从 SSH 隧道的 `127.0.0.1` 推断服务器地址。

精确的机器可读定义见 [`probe-api/api/openapi.yaml`](../../probe-api/api/openapi.yaml)。OpenAPI 中每条 Setup 路径都使用独立的本机浏览器转发 URL，不会继承三个生产 HTTPS Server；服务端实际仍只监听 root 私有 Unix Socket。

## 6. 成功、失败与恢复

`POST /complete` 返回 `202` 只表示 Finalizer 已开始，不表示安装已经成功。页面必须持续轮询 `/status`：

- `installed`：读取 `admin_url` 并打开域名管理入口或 `https://<IP>:18455/login`。初始化服务会在短暂展示结果后自动关闭，setup service/socket/finalizer 单元被禁用，Socket 文件被移除。
- `recovery_required`：Finalizer 或持久状态出现失败。系统保持 fail closed，正式 API/Nginx 不应对外提供半完成站点；内存 Session 不会恢复，也不能从浏览器重新生成管理员。

出现 `recovery_required` 时，不要重复提交、手工改状态文件或删除数据库来绕过保护。先在服务器查看不含秘密的服务状态和日志：

```bash
systemctl status probe-panel-setup.socket probe-panel-setup.service probe-panel-finalizer.service --no-pager
journalctl -u probe-panel-setup.socket -u probe-panel-setup.service -u probe-panel-finalizer.service -n 200 --no-pager
```

Finalizer 在 `ProtectSystem=strict` 下运行，只对完成安装所需目录开放精确写权限；其中 `/var/lib/nginx` 用于 Debian 13 的 `nginx -t` 初始化运行时请求体目录，`/etc/nginx/conf.d` 用于安装正式配置，`/etc/nginx/sites-enabled` 仅用于安全移除经验证的 Debian 默认站点链接。整个 `/etc/nginx` 不可写。它的 systemd Socket 沙箱也只允许绑定两种正式入口所需的 TCP `80`、`443`、`18453`、`18454`、`18455`，不允许安装隧道端口 `18080` 或其他端口。若日志显示上述精确路径为只读或所选模式的正式端口返回 `EPERM`，说明活动 `probe-panel-finalizer.service` 不是当前受支持单元或发生了配置漂移；应恢复发行包中的单元并重新执行验证，不要放宽整个文件系统或绑定策略。

根据首个明确错误修复所选模式的端口、DNS/ACME、PostgreSQL 或文件系统问题，并按恢复流程处理。状态不会自动退回 `pending`；若尚无需要保留的数据且主机可重装，最安全的恢复方式是重建干净 Debian 13 后重新安装。若已有配置、数据库或备份，必须先验证备份，再由管理员审查和恢复；普通 `uninstall` 有意保留 `/etc/probe-panel`、`/var/lib/probe-panel`、数据库和备份，也不会充当 `purge`。

## 7. 安装后的固定边界

正式 Nginx 的三个入口都永久对 `/install`、`/install/*`、`/api/v1/setup` 和 `/api/v1/setup/*` 返回 `404`。不要在安装后重新启动 Setup Socket/服务，也不要向公网开放本机转发端口 `18080`、API upstream `8080` 或 PostgreSQL `5432`。域名模式只监听 80/443；IP 模式只监听 18453/18454/18455。IP 模式的游客和 Agent 代理必须清空 Cookie 并隐藏 Set-Cookie，因为浏览器 Cookie 本身不按端口隔离。

---

# 高级：从源码手工构建和部署

以下流程供开发、审计或无法使用预编译 Release 的场景使用。它需要在 Debian 13 构建机安装 Go、Node.js/npm 等构建依赖，手工准备 TLS、环境文件和首个管理员；不要与上面的一键首次向导混用。

## A.1 最终边界

生产主机使用以下固定布局：

```text
/srv/probe/
├── api/probe-api        # 指向当前 API 发布的原子链接
├── agent                # 指向当前 Agent 首次安装下载发布的原子链接
├── web                  # 指向当前 probe-web 发布的原子链接
├── admin                # 指向当前 probe-admin 发布的原子链接
├── migrations           # 指向当前迁移清单的原子链接
├── config/              # 持久配置；发布不得覆盖
├── releases/            # 不可变版本目录
└── backups/             # 升级前数据库备份
```

四个源码工程必须分别位于同步目录中的 `probe-agent/`、`probe-api/`、`probe-web/` 和 `probe-admin/`。构建过程分别进入四个临时副本，不允许跨目录导入。服务端发布会交叉构建并公开校验过的 Linux amd64/arm64 Agent 首次安装文件，但不会在服务端启用 Agent；目标节点安装见 [agent-deployment.md](agent-deployment.md)。

Nginx 监听必须与持久化的 `PROBE_INGRESS_MODE` 一致：`domain` 只允许 TCP 80/443，`ip` 只允许 TCP 18453/18454/18455。Go API 固定监听 `127.0.0.1:8080`，PostgreSQL `5432` 必须严格只监听 loopback（`127.0.0.1` 和/或 `::1`），不能使用内网地址代替；Agent 不监听端口。

## A.2 安装前准备

先明确选择一种入口模式，并在整个手工安装过程中保持不变；不能同时配置两种模式的监听、Origin 或证书：

- `domain`：准备游客、管理、Agent API 三个不同且互不包含的小写域名；三个 DNS `A`/`AAAA` 记录指向主机，TCP 80/443 可达，三组固定路径下的证书链必须通过系统公信根、对应 DNS SAN、ServerAuth、有效期和私钥匹配校验，并配置可用的 Certbot 续期 timer。
- `ip`：准备一个规范的可路由 IPv4 或 IPv6 地址，并确认 TCP 18453/18454/18455 可达且未占用。准备固定私有 CA 与由其直接签发的服务证书；叶证书只能有该地址这一个 IP SAN，不能有 DNS SAN，并必须允许 ServerAuth。浏览器需信任该 CA，Agent 依靠固定 `ca.pem` 的 SHA-256 指纹验证；该模式不启用 Certbot。

两种模式共同需要：

- 已确定允许访问游客面板和管理面板的 IPv4/IPv6 CIDR。
- 主机是专用 Debian 13；现有 Nginx 站点、数据库和防火墙规则已经人工盘点。
- 项目源码以只包含源码和文档的方式同步到虚拟机，例如 `/var/tmp/probe-source-20260823/`。不要把同步目录放在 `/srv/probe` 中，也不要同步 `node_modules`、`dist` 或 Go 二进制。

安装脚本不包含密码、Token、证书或代理地址。若依赖下载需要代理，只在当前命令环境中临时配置包管理器、npm 或 Go 的标准代理变量，完成后清除；不得写入生产配置。

## A.3 准备系统和持久目录

从同步源码目录执行：

```bash
cd /var/tmp/probe-source-20260823
bash probe-api/deploy/scripts/install.sh \
  --source-root /var/tmp/probe-source-20260823 \
  --prepare-only
```

该步骤会：

- 安装 Debian 13 的 Go、Node.js/npm、Nginx、PostgreSQL 客户端/服务端和验证工具；
- 创建无登录权限的 `probe-api` 系统账户；若同名账户或组已经存在，则只做严格复验，不会静默修复。账户必须是 `/etc/login.defs` 定义的 Debian system UID 范围内的非 root 用户，唯一使用同名主组，home 为 `/nonexistent`、Shell 为 `/usr/sbin/nologin`，没有附加组；同名组也不能包含或作为其他账户的主组。任一条件不符都会在写入发布文件或执行数据库迁移前停止；
- 创建 `/srv/probe`、配置目录、发布目录和备份目录；
- 准备由 `probe-api` 用户拥有的 `/var/backups/probe-panel/postgres` 定时备份根目录；
- 更新 `probe-api.env.example`、域名模式 `nginx.conf.example` 与 IP 模式 `nginx-ip.conf.example`；
- 仅在不存在时创建空的 `/etc/probe-panel/admin-allowlist.geo`。空白名单会安全地拒绝所有游客和管理员访问。

它不会创建数据库密码、管理员密码、TLS 密钥或活动配置，也不会覆盖已有活动配置和数据库。

## A.4 创建 PostgreSQL 数据库

使用 PostgreSQL 自带的交互式密码提示，避免密码进入命令历史：

```bash
sudo -u postgres createuser --pwprompt probe
sudo -u postgres createdb --owner=probe probe
```

确认 PostgreSQL 严格只监听 loopback。Debian 默认通常是 `localhost`，仍需实际检查：

```bash
sudo -u postgres psql -Atc 'show listen_addresses'
ss -lntp | grep ':5432' || true
```

`show listen_addresses` 只能表示 localhost/loopback，`ss` 中只能出现 `127.0.0.1:5432` 和/或 `[::1]:5432`。任何 `0.0.0.0`、`[::]`、公网或内网地址都必须先修正；不能以防火墙作为非 loopback 监听的替代保护。

不要在安装脚本、源码、文档或命令行参数中写明文数据库密码。

## A.5 创建 API 活动环境文件

复制示例并只在虚拟机上编辑：

```bash
install -o root -g probe-api -m 0640 \
  /srv/probe/config/probe-api.env.example \
  /srv/probe/config/probe-api.env
editor /srv/probe/config/probe-api.env
```

两种模式都要替换：

- `PROBE_DATABASE_URL`：真实 PostgreSQL URL，Host 必须为 loopback；密码中的保留字符必须进行 URL 百分号编码；
- `PROBE_AGENT_INSTALLER_URL`：当前源码已核验并明确允许的 `Kcmose/my-agent/refs/tags/v1.0.2` Raw URL；完整 40 位小写提交仅保留兼容，不要使用其他未经核验的版本标签、可变的 `main` 或 `refs/heads/*`；
- `PROBE_ADMIN_ALLOWLIST_FILE=/etc/probe-panel/admin-allowlist.geo`。

生产固定值：

```text
PROBE_API_LISTEN_ADDR=127.0.0.1:8080
PROBE_TRUSTED_PROXY_CIDRS=127.0.0.1/32,::1/128
```

按已选模式再设置以下四项（IPv6 Origin 中的地址必须加 `[]`）：

```text
# 域名模式
PROBE_INGRESS_MODE=domain
PROBE_ADMIN_ORIGIN=https://admin.example.net
PROBE_AGENT_PUBLIC_URL=https://agent.example.net
PROBE_AGENT_INSTALL_CA_FILE=

# IP 模式（示例 IPv4）
PROBE_INGRESS_MODE=ip
PROBE_ADMIN_ORIGIN=https://203.0.113.20:18455
PROBE_AGENT_PUBLIC_URL=https://203.0.113.20:18454
PROBE_AGENT_INSTALL_CA_FILE=/etc/probe-panel/tls/private-ca/ca.pem
```

每次只保留其中一组。域名模式的 Origin 必须与活动 Nginx 中的管理/Agent Host 完全一致；IP 模式的两个 Origin 必须使用同一个规范 IP 和固定端口。环境文件只接受无空格、无引号的 `PROBE_NAME=value` 行，不能写 Shell 表达式。脚本会拒绝示例凭据、模式/Origin/CA 不一致、Agent Origin 与 Nginx 入口不一致、非 `Kcmose/my-agent` 完整提交或严格不可变版本路径的安装器 URL、宽泛代理信任和不安全权限。

## A.6 配置数据库备份凭据

备份和恢复工具故意不复用含完整 URL 的 `PROBE_DATABASE_URL`，避免把数据库密码放进客户端命令参数。复制离散 libpq 参数示例：

```bash
install -o root -g root -m 0600 \
  /srv/probe/config/probe-postgres-backup.env.example \
  /srv/probe/config/probe-postgres-backup.env
editor /srv/probe/config/probe-postgres-backup.env
```

默认安装使用 `127.0.0.1:5432`、数据库和角色 `probe`。活动备份环境文件必须显式包含示例中的全部 9 个键；安装器会拒绝缺项、重复项、未知项、远端 `PGHOST`、URL 形式的 `PGDATABASE`、不受 systemd 写路径保护的备份目录，以及越界的保留参数。生产备份单元不再提供会掩盖拼写或缺项的默认连接变量。

密码只写入由服务用户独占的 `.pgpass`：

```bash
install -o probe-api -g probe-api -m 0600 /dev/null \
  /srv/probe/config/probe-postgres.pgpass
editor /srv/probe/config/probe-postgres.pgpass
```

文件行格式为 `127.0.0.1:5432:probe:probe:<数据库密码>`。完整安装会在构建和迁移前验证两个文件均非符号链接、非空且权限不宽于 `0600`，并确认 `probe-api` 能读取 `.pgpass`；校验不会读取或打印密码。详见 [备份与恢复](backup-restore.md)。

## A.7 配置白名单、TLS 和 Nginx

白名单每行只能是明确 IP/CIDR 加数字 `1`，例如：

```nginx
192.0.2.10/32 1;
2001:db8:1234::/48 1;
```

设置权限：

```bash
chown root:root /etc/probe-panel
chmod 0755 /etc/probe-panel
chown root:probe-api /etc/probe-panel/admin-allowlist.geo
chmod 0640 /etc/probe-panel/admin-allowlist.geo
```

`/etc/probe-panel` 的 `0755` 只用于让 Nginx worker 穿越目录并在 IP 模式公开读取 `tls/private-ca/ca.pem`，不表示其中的凭据可公开读取。`setup.env` 和 TLS 私钥保持 `root:root 0600`，白名单保持 `root:probe-api 0640`；不得对该目录递归放宽权限。

### 域名模式

把三个域名的公信 fullchain 和对应私钥分别放入固定位置：

```text
/etc/probe-panel/tls/panel/fullchain.pem
/etc/probe-panel/tls/panel/privkey.pem
/etc/probe-panel/tls/admin/fullchain.pem
/etc/probe-panel/tls/admin/privkey.pem
/etc/probe-panel/tls/api/fullchain.pem
/etc/probe-panel/tls/api/privkey.pem
```

私钥由 root 持有并设置为 `0600`。创建域名模式的活动 Nginx 片段：

```bash
install -o root -g root -m 0644 \
  /srv/probe/config/nginx/nginx.conf.example \
  /srv/probe/config/nginx/nginx.conf
editor /srv/probe/config/nginx/nginx.conf
```

只把 `panel.example.com`、`admin.example.com`、`api.example.com` 一致替换为三个互不包含的真实域名。通过全部安装前，`certbot.timer` 必须同时为 enabled 和 active。

### IP 模式

固定 TLS 文件为：

```text
/etc/probe-panel/tls/private-ca/ca.pem
/etc/probe-panel/tls/private-ca/fullchain.pem
/etc/probe-panel/tls/private-ca/privkey.pem
```

`ca.pem` 必须只包含一张有效、自签名且允许签发证书的 CA 公钥证书；`fullchain.pem` 必须严格由一张叶证书和同一张 CA 组成，叶证书由该 CA 直接签发，并只包含已选 IP 这一个 SAN。`privkey.pem` 是与叶证书匹配的 root-only `0600` 私钥；签发 CA 的私钥绝不得放在公开下载路径。Nginx 固定的 panel/admin/api 证书路径应指向这一组叶证书与私钥。

记录安装器与管理面板使用的精确文件指纹：

```bash
sha256sum /etc/probe-panel/tls/private-ca/ca.pem
```

指纹是该 PEM 文件完整字节的 64 位小写 SHA-256，不是证书文本摘要的其他表示。创建 IP 模式的活动 Nginx 片段：

```bash
install -o root -g root -m 0644 \
  /srv/probe/config/nginx/nginx-ip.conf.example \
  /srv/probe/config/nginx/nginx.conf
editor /srv/probe/config/nginx/nginx.conf
```

只把模板中全部 `PROBE_SETUP_SERVER_IP` 一致替换为同一个规范 IP；IPv6 在 Nginx Host 中使用方括号。IP 模式不监听 80/443，且 `certbot.timer` 必须同时为 disabled 和 inactive。

两种模式都不能修改模板的证书路径、Host/地址数量、监听、路由、限流、安全头、静态根或拒绝规则。部署器会按已选模式对三个域名或唯一 IP 归一化，再与对应的当前源码模板逐字节比较，任何其他漂移都会被拒绝。需要改变入口策略时，应先把源码模板和部署契约校验器作为一次可审查的代码变更共同更新。

保持以下边界：

- 游客入口只提供 `/srv/probe/web` 和匿名只读 Panel API，并对 `/downloads/*` 返回 `404`；
- 管理入口只提供 `/srv/probe/admin`、Auth/Admin API 和管理页需要的只读 Panel API，并对 `/downloads/*` 返回 `404`；
- Agent 入口提供 Agent API，以及 `/downloads/probe-agent/` 下严格文件名白名单内的公开安装器、systemd 单元、SHA256 清单和 amd64/arm64 二进制；IP 模式还且只有该入口公开固定 `ca.pem`，域名模式不公开它；其他下载、公共 API 和根路径默认 404；
- API upstream 固定为 `127.0.0.1:8080`；
- 游客和管理入口共用 `/etc/probe-panel/admin-allowlist.geo`。

`nginx -T` 全局校验在域名模式会要求三个真实域名各只出现于项目的 HTTP 跳转块和对应 HTTPS 块；其他 `/etc/nginx` 配置若重复声明其中任一域名，部署会失败。IP 模式则校验三个固定 HTTPS 端口、唯一规范 IP、Cookie 隔离和严格模板一致性。两种模式都必须先人工清理其他 Nginx 监听或站点冲突。

## A.8 完成首次安装

Debian Nginx 包通常启用默认站点。仅当 `/etc/nginx/sites-enabled/default` 仍是包自带、指向 `/etc/nginx/sites-available/default` 的符号链接时，可让安装脚本安全地移除该链接：

```bash
cd /var/tmp/probe-source-20260823
bash probe-api/deploy/scripts/install.sh \
  --source-root /var/tmp/probe-source-20260823 \
  --skip-packages \
  --disable-default-site
```

若该路径是自定义文件或指向其他位置，脚本会拒绝修改，必须先人工审查。

完整安装按以下顺序执行：

1. 校验 Debian 版本、同步源码、活动配置、备份凭据、systemd 安全项，以及 `PROBE_INGRESS_MODE` 对应的 Nginx 模板、路由、端口、TLS 和 Certbot timer 边界；
2. 把四个工程复制到独立临时构建目录，分别运行测试、静态检查和构建；
3. 交叉构建 Linux amd64/arm64 Agent，生成下载 SHA256 清单并校验安装器契约；同时校验两个前端产物没有符号链接或 Source Map；
4. 使用临时路径对新源码中的 API、备份 service 和 timer 执行 `systemd-analyze verify`，在数据库变化前拒绝坏单元；
5. 创建不可变发布目录和 SHA-256 清单；
6. 获取与定时备份、恢复共用的数据库维护锁，在迁移前创建并验证 PostgreSQL custom-format 备份；
7. 使用新 API 二进制执行 `migrate status`、`migrate up`、再次 `migrate status`；
8. 通过同文件系统符号链接替换，原子切换 API、Agent 下载目录、游客静态目录、管理静态目录和迁移清单；
9. 安装并启用 PostgreSQL 每日备份 timer；
10. 重启 API、重载 Nginx，并验证 readiness、当前模式证书、Certbot timer 状态、服务状态和监听地址。

活动环境文件、活动 Nginx 配置、TLS、白名单和数据库不会被发布覆盖。旧发布与迁移前备份会保留。

如果 `/srv/probe/agent`、`/srv/probe/web`、`/srv/probe/admin`、`/srv/probe/migrations` 或 `/srv/probe/api/probe-api` 已经是普通文件/目录而不是本脚本管理的发布链接，脚本会在数据库变动前停止。先人工确认并把旧部署移动到明确的归档目录，不要让脚本猜测或删除。

## A.9 创建首个管理员

安装成功后，用临时 systemd 单元读取受保护的环境文件，并在交互式终端中输入密码：

```bash
systemd-run --quiet --wait --collect --pty \
  --uid=probe-api --gid=probe-api \
  --property=EnvironmentFile=/srv/probe/config/probe-api.env \
  /srv/probe/api/probe-api user bootstrap-admin admin
```

密码不会显示。数据库已有任何管理员后，bootstrap 命令会拒绝再次创建。

## A.10 安装后验收

先运行只读生产校验：

```bash
bash /var/tmp/probe-source-20260823/probe-api/deploy/scripts/validate-production.sh all \
  --source-root /var/tmp/probe-source-20260823
```

再检查：

```bash
systemctl status probe-api nginx postgresql probe-postgres-backup.timer --no-pager
systemctl list-timers probe-postgres-backup.timer --no-pager
journalctl -u probe-api -n 100 --no-pager
ss -lntp
```

主机防火墙必须与已选模式一致：域名模式对外只开放 TCP 80/443，IP 模式对外只开放 TCP 18453/18454/18455。不要开放 8080、5432 或任何 Agent 监听端口，也不要为未选模式额外保留另一组入口端口。分别从白名单地址和非白名单地址验证游客、管理和 Agent 入口；确认跨入口 API、未知 API 和公网 `/internal/*` 都 fail closed。

## A.11 幂等和失败语义

- `--prepare-only` 可重复执行；它更新示例，不覆盖活动配置。
- 完整安装重复执行会形成新发布，行为与升级一致。
- 配置、Nginx 或构建校验失败时，不执行数据库迁移或发布切换。
- 数据库备份失败时，不执行迁移。
- 运行验证失败时，脚本恢复之前的应用链接；已经成功提交的前向数据库迁移不会被自动回滚，备份路径会明确打印。
