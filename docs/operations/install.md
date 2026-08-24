# Debian 13 生产安装

推荐方式是在一台干净、专用的 Debian 13 主机上运行 GitHub 固定版本的一键安装器，再通过 SSH 隧道完成浏览器向导。目标服务器只安装运行时组件，不安装 Go、Node.js 或 npm。Windows 本地目录仍只保存源码和文档；源码构建和手工发布见本文末尾的高级流程。

## 1. 一键安装前准备

一键安装仅支持 Debian 13 的 Linux `amd64`/`arm64`。执行前确认：

- 使用全新或已人工清点的专用主机，并具有 `root` 或 `sudo` 权限。安装器发现既有/残留 Probe Panel 文件或 systemd 单元会拒绝覆盖；发现正在运行的 Nginx 也会拒绝继续。
- 准备三个不同且互不包含的真实域名：游客面板、管理面板、Agent API。向导只填写裸 FQDN，例如 `panel.monitor.your-domain.tld`，不要填写 `https://`、端口、路径、IP 或结尾点。
- 三个域名的 DNS `A`/`AAAA` 都已经解析到这台服务器。若发布了 `AAAA`，IPv6 也必须能到达服务器；否则先删除错误的 `AAAA`。
- 公网 TCP `80`、`443` 能直达服务器，云安全组、主机防火墙和 NAT 已正确放行。TCP 80/443 在安装开始前不能被其他进程占用。
- ACME HTTP-01 验证期间不要让 CDN、反向代理、端口映射或其他 Web 服务接管请求；使用 Cloudflare 等服务时先使用“仅 DNS”解析。
- 服务器能通过 HTTPS 访问 GitHub Release、GitHub Raw、Debian 软件源和 ACME 服务，并能从你的电脑通过 SSH 登录。网络需要代理时可临时使用部署环境自己的 SOCKS5 或 HTTP 代理，但不要把内网代理地址写入生产配置、公开仓库或日志。

安装器会用 APT 安装以下运行时依赖：

```text
ca-certificates  curl  python3  nginx  postgresql  postgresql-client
certbot          iproute2  util-linux
```

## 2. 执行一键安装

使用固定且不可变的服务端 `v1.0.0` 标签：

```bash
curl -fsSL --proto '=https' --tlsv1.2 \
  https://raw.githubusercontent.com/Kcmose/super-my/refs/tags/v1.0.0/install.sh \
  | sudo bash
```

脚本会识别架构，下载对应的预编译 GitHub Release 和 `SHA256SUMS`，校验 SHA-256 后安全解包。它不会从 `main` 构建，也不会接收数据库密码、管理员密码或域名参数。安装过程中 Nginx 保持停止，PostgreSQL 只允许本机监听；向导最终校验通过前，TCP 80/443 不会成为正式站点入口。

成功后终端只显示一次 64 位小写十六进制安装码。该码具有 256-bit 随机强度，创建后 30 分钟过期；磁盘只保存它的 SHA-256、到期时间和消费状态，无法再次查询明文。终端还会显示 SSH 隧道和浏览器地址。

## 3. 通过 SSH 隧道打开首次向导

在你自己的电脑执行，替换服务器地址；SSH 端口或用户不同时按实际修改：

```bash
ssh -L 18080:127.0.0.1:18080 root@<服务器IP>
```

保持该 SSH 会话打开，然后在本机浏览器访问：

```text
http://127.0.0.1:18080/install
```

初始化服务只绑定服务器的 `127.0.0.1:18080`，不得在防火墙开放 18080，不得改为 `0.0.0.0`，也不得通过临时或正式 Nginx 反代。浏览器输入安装码后，安装码会被一次性消费，并换取一个只保存在服务内存中的 15 分钟 Session 和 CSRF Token；应在响应的 `expires_at` 前提交向导。

## 4. 首次向导字段

向导提交的是一个严格 JSON 对象；未知字段、重复字段、缺少字段、尾随数据以及超过 64 KiB 的正文都会被拒绝。首版字段固定如下：

| 区域 | 字段 | 规则 |
| --- | --- | --- |
| 本机 PostgreSQL | `database.mode` | 固定为 `local`，V1 不接受远程数据库配置 |
| 本机 PostgreSQL | `database.name`、`database.username` | 小写 PostgreSQL 标识符；不使用 `postgres`、`template0`、`template1` 等保留值 |
| 本机 PostgreSQL | `database.password`、`password_confirmation` | 两次一致；12–1024 UTF-8 字节；禁止 NUL、CR/LF 和其他控制字符 |
| 三个入口 | `domains.panel`、`domains.admin`、`domains.agent` | 三个小写裸 FQDN，必须不同且不得互相包含；服务端自行构造 HTTPS Origin |
| TLS | `tls.mode`、`tls.email` | 模式固定为 `acme`；邮箱用于 ACME 账户和证书到期通知 |
| 管理白名单 | `allowlist` | 游客面板与管理面板共用；1–128 个 IPv4/IPv6 或规范 CIDR；裸 IP 自动转成 `/32` 或 `/128`；禁止 `/0`、重复项和带主机位 CIDR |
| 首个管理员 | `administrator.username` | 非空、无首尾空白或控制字符，最长 128 个字符 |
| 首个管理员 | `administrator.password`、`password_confirmation` | 两次一致；12–1024 UTF-8 字节 |

三个域名都要通过 ACME HTTP-01 取得公信证书。因此 DNS 必须在提交前生效，TCP 80 必须能从 ACME 验证节点访问，TCP 443 必须能供安装后的用户访问。Finalizer 只在签发证书时临时使用 80，随后由经过校验的 Nginx 接管 80/443。

## 5. 临时 Setup API 契约

Setup API 属于独立的 `probe-setup` 进程，不是正式 `probe-api serve` 的公开路由。它只有一个临时 Server：`http://127.0.0.1:18080`。TCP 对端必须是 loopback，`Host` 只能为 `127.0.0.1:18080` 或 `localhost:18080`。浏览器 POST 的 `Origin` 必须精确为 `http://127.0.0.1:18080` 或 `http://localhost:18080`；GET 可不带 Origin，但带了就必须匹配。全部响应使用 `Cache-Control: no-store`。

状态机只有五态：

| `status` | 含义 |
| --- | --- |
| `pending` | 等待使用一次性安装码 |
| `configuring` | 安装码已消费，等待当前内存 Session 提交配置 |
| `finalizing` | 严格校验完成，独立的特权 Finalizer 正在异步执行 |
| `installed` | 安装成功；响应同时包含 `admin_url`，随后临时服务关闭 |
| `recovery_required` | 失败关闭；不能重新打开首次注册，必须由服务器管理员恢复 |

三个接口及精确凭据头如下：

| 方法与路径 | 必需请求头 | 请求正文 | 成功响应 |
| --- | --- | --- | --- |
| `GET /api/v1/setup/status` | 无；浏览器自动发送的 `Host`/`Origin` 仍受上述限制 | 无 | `200 {"status":"..."}`；安装成功时为 `200 {"admin_url":"https://<管理域名>/login","status":"installed"}` |
| `POST /api/v1/setup/session` | `Content-Type: application/json`、允许的 `Origin` | `{"setup_code":"<64位小写十六进制安装码>"}` | `200 {"session_token":"<64hex>","csrf_token":"<64hex>","expires_at":"<RFC3339>"}` |
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
  "domains": {
    "panel": "panel.monitor.your-domain.tld",
    "admin": "admin.monitor.your-domain.tld",
    "agent": "agent.monitor.your-domain.tld"
  },
  "tls": {
    "mode": "acme",
    "email": "operations@your-domain.tld"
  },
  "allowlist": ["203.0.113.25", "2001:db8:1234::/48"],
  "administrator": {
    "username": "admin",
    "password": "<至少12字节>",
    "password_confirmation": "<与password一致>"
  }
}
```

精确的机器可读定义见 [`probe-api/api/openapi.yaml`](../../probe-api/api/openapi.yaml)。OpenAPI 中每条 Setup 路径都使用独立的 path-level loopback Server，不会继承三个生产 HTTPS Server。

## 6. 成功、失败与恢复

`POST /complete` 返回 `202` 只表示 Finalizer 已开始，不表示安装已经成功。页面必须持续轮询 `/status`：

- `installed`：读取 `admin_url` 并打开 `https://<管理域名>/login`。初始化服务会在短暂展示结果后自动关闭，setup 码记录被销毁，setup/finalizer 单元被禁用。
- `recovery_required`：Finalizer 或持久状态出现失败。系统保持 fail closed，正式 API/Nginx 不应对外提供半完成站点；已经消费的安装码和内存 Session 不会恢复，也不能从浏览器重新生成管理员。

出现 `recovery_required` 时，不要重复提交、手工改状态文件或删除数据库来绕过保护。先在服务器查看不含秘密的服务状态和日志：

```bash
systemctl status probe-panel-setup.service probe-panel-finalizer.service --no-pager
journalctl -u probe-panel-setup.service -u probe-panel-finalizer.service -n 200 --no-pager
```

根据首个明确错误修复 DNS、80/443 可达性、PostgreSQL 或文件系统问题，并按恢复流程处理。状态不会自动退回 `pending`；若尚无需要保留的数据且主机可重装，最安全的恢复方式是重建干净 Debian 13 后重新安装。若已有配置、数据库或备份，必须先验证备份，再由管理员审查和恢复；普通 `uninstall` 有意保留 `/etc/probe-panel`、`/var/lib/probe-panel`、数据库和备份，也不会充当 `purge`。

## 7. 安装后的固定边界

生产 Nginx 的三个 HTTPS Host 都永久对 `/install`、`/install/*`、`/api/v1/setup` 和 `/api/v1/setup/*` 返回 `404`。不要在安装后重新启动 loopback Setup 服务，也不要向公网开放 `18080`、API upstream `8080` 或 PostgreSQL `5432`。游客面板、管理面板与 Agent API 继续使用三个独立 HTTPS 域名；安装向导本身不是其中任何一个面板。

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

公网 Nginx 只能监听 TCP 80/443。Go API 固定监听 `127.0.0.1:8080`，PostgreSQL 只能监听本机或私有地址，Agent 不监听端口。

## A.2 安装前准备

先完成以下外部条件：

- 三个真实域名已经解析到生产主机：游客面板、管理面板、Agent API。
- 三个域名的 TLS 证书和私钥已经安全取得。
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
- 创建无登录权限的 `probe-api` 系统账户；
- 创建 `/srv/probe`、配置目录、发布目录和备份目录；
- 准备由 `probe-api` 用户拥有的 `/var/backups/probe-panel/postgres` 定时备份根目录；
- 更新 `probe-api.env.example` 与 `nginx.conf.example`；
- 仅在不存在时创建空的 `/etc/probe-panel/admin-allowlist.geo`。空白名单会安全地拒绝所有游客和管理员访问。

它不会创建数据库密码、管理员密码、TLS 密钥或活动配置，也不会覆盖已有活动配置和数据库。

## A.4 创建 PostgreSQL 数据库

使用 PostgreSQL 自带的交互式密码提示，避免密码进入命令历史：

```bash
sudo -u postgres createuser --pwprompt probe
sudo -u postgres createdb --owner=probe probe
```

确认 PostgreSQL 没有监听公网地址。Debian 默认通常只监听本机，仍需实际检查：

```bash
sudo -u postgres psql -Atc 'show listen_addresses'
ss -lntp | grep ':5432' || true
```

不要在安装脚本、源码、文档或命令行参数中写明文数据库密码。

## A.5 创建 API 活动环境文件

复制示例并只在虚拟机上编辑：

```bash
install -o root -g probe-api -m 0640 \
  /srv/probe/config/probe-api.env.example \
  /srv/probe/config/probe-api.env
editor /srv/probe/config/probe-api.env
```

至少替换：

- `PROBE_DATABASE_URL`：真实 PostgreSQL URL；密码中的保留字符必须进行 URL 百分号编码；
- `PROBE_ADMIN_ORIGIN`：真实管理面板 HTTPS Origin；
- `PROBE_AGENT_PUBLIC_URL`：真实 Agent HTTPS Origin，必须与活动 Nginx 片段中的第三个 Host 完全一致，例如 `https://agent.example.net`；
- `PROBE_AGENT_INSTALLER_URL`：当前源码已核验并明确允许的 `Kcmose/my-agent/refs/tags/v1.0.1` Raw URL；完整 40 位小写提交仅保留兼容，不要使用其他未经核验的版本标签、可变的 `main` 或 `refs/heads/*`；
- `PROBE_ADMIN_ALLOWLIST_FILE=/etc/probe-panel/admin-allowlist.geo`。

生产固定值：

```text
PROBE_API_LISTEN_ADDR=127.0.0.1:8080
PROBE_TRUSTED_PROXY_CIDRS=127.0.0.1/32,::1/128
```

环境文件只接受无空格、无引号的 `PROBE_NAME=value` 行，不能写 Shell 表达式。脚本会拒绝示例凭据、示例管理/Agent 域名、Agent Origin 与 Nginx Host 不一致、非 `Kcmose/my-agent` 完整提交或严格不可变版本路径的安装器 URL、宽泛代理信任和不安全权限。生产必须使用公开可信的 Agent TLS 证书，不能设置 `PROBE_AGENT_INSTALL_CA_FILE`；该可选项只供受控私有 CA 预览使用。

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
chown root:probe-api /etc/probe-panel/admin-allowlist.geo
chmod 0640 /etc/probe-panel/admin-allowlist.geo
```

把三个域名的证书分别放入活动 Nginx 配置固定引用的位置：

```text
/etc/probe-panel/tls/panel/fullchain.pem
/etc/probe-panel/tls/panel/privkey.pem
/etc/probe-panel/tls/admin/fullchain.pem
/etc/probe-panel/tls/admin/privkey.pem
/etc/probe-panel/tls/api/fullchain.pem
/etc/probe-panel/tls/api/privkey.pem
```

私钥应由 root 持有并设置为 `0600`。

创建活动 Nginx 片段：

```bash
install -o root -g root -m 0644 \
  /srv/probe/config/nginx/nginx.conf.example \
  /srv/probe/config/nginx/nginx.conf
editor /srv/probe/config/nginx/nginx.conf
```

只把 `panel.example.com`、`admin.example.com`、`api.example.com` 一致替换为三个互不包含的真实域名。证书路径、Host 数量、监听、路由、限流、安全头、静态根和拒绝规则均不得在活动文件中另行编辑；部署器会把活动片段按域名归一化后与当前源码模板逐字节比较，并拒绝任何其他漂移。需要改变入口策略时，应先把源码模板和部署契约校验器作为一次可审查的代码变更共同更新。

保持以下边界：

- `panel` Host 只提供 `/srv/probe/web` 和匿名只读 Panel API，并对 `/downloads/*` 返回 `404`；
- `admin` Host 只提供 `/srv/probe/admin`、Auth/Admin API 和管理页需要的只读 Panel API，并对 `/downloads/*` 返回 `404`；
- `api` Host 提供 Agent API，以及 `/downloads/probe-agent/` 下严格文件名白名单内的公开安装器、systemd 单元、SHA256 清单、amd64/arm64 二进制和私有 CA 预览可选的 `ca.pem`；其他下载、公共 API和根路径默认 404；
- API upstream 固定为 `127.0.0.1:8080`；
- Panel 和 Admin Host 共用 `/etc/probe-panel/admin-allowlist.geo`。

`nginx -T` 全局校验还会要求三个真实域名各只出现于项目的 HTTP 跳转块和对应 HTTPS 块；其他 `/etc/nginx` 配置若重复声明其中任一域名，部署会失败，必须先人工清理冲突站点。

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

1. 校验 Debian 版本、同步源码、活动配置、备份凭据、systemd 安全项和 Nginx 三 Host/路由/端口边界；
2. 把四个工程复制到独立临时构建目录，分别运行测试、静态检查和构建；
3. 交叉构建 Linux amd64/arm64 Agent，生成下载 SHA256 清单并校验安装器契约；同时校验两个前端产物没有符号链接或 Source Map；
4. 使用临时路径对新源码中的 API、备份 service 和 timer 执行 `systemd-analyze verify`，在数据库变化前拒绝坏单元；
5. 创建不可变发布目录和 SHA-256 清单；
6. 获取与定时备份、恢复共用的数据库维护锁，在迁移前创建并验证 PostgreSQL custom-format 备份；
7. 使用新 API 二进制执行 `migrate status`、`migrate up`、再次 `migrate status`；
8. 通过同文件系统符号链接替换，原子切换 API、Agent 下载目录、游客静态目录、管理静态目录和迁移清单；
9. 安装并启用 PostgreSQL 每日备份 timer；
10. 重启 API、重载 Nginx，并验证 readiness、服务状态和监听地址。

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

主机防火墙只允许外部进入 TCP 80/443。不要开放 8080、5432 或任何 Agent 监听端口。分别从白名单地址和非白名单地址验证三个 Host；确认跨 Host API、未知 API 和公网 `/internal/*` 都 fail closed。

## A.11 幂等和失败语义

- `--prepare-only` 可重复执行；它更新示例，不覆盖活动配置。
- 完整安装重复执行会形成新发布，行为与升级一致。
- 配置、Nginx 或构建校验失败时，不执行数据库迁移或发布切换。
- 数据库备份失败时，不执行迁移。
- 运行验证失败时，脚本恢复之前的应用链接；已经成功提交的前向数据库迁移不会被自动回滚，备份路径会明确打印。
