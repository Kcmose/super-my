# 管理端故障排查

本文只排查独立 management 产品。IP 模式只有管理入口 `18455`；domain 模式只有
一个管理域名。游客 `18453`、Agent `18454`、Agent 下载站和三域名 full 配置不是
管理安装健康条件。未配置 Agent 时 `agent.status=not_configured` 是正常状态。

不要把管理员口令、Session Cookie、CSRF、数据库连接串、备份凭据或 Token 写入
工单、终端转储和聊天记录。

## 1. 先确认平台与发布边界

```bash
cat /etc/os-release
ps -p 1 -o comm=
systemctl --version | head -n 1
uname -m
bash /root/probe-panel-install.sh status
```

- 主机必须精确映射到 ABI v2 的 15 个 platform ID 之一，不能靠 `ID_LIKE`、
  包管理器或相邻版本猜测。
- Debian 9/10/11/12、Ubuntu 18.04/20.04、CentOS Linux 7/8、Stream 8 需要显式
  `--accept-eol`。Debian/Ubuntu 使用隔离的受管 live/archive 源，不覆盖用户源；
  Debian 9 缺 HTTPS method 或 arm64 PG14 包时失败关闭。CentOS 使用安装器专用
  `reposdir` 和精确 allowlist 读取 Vault/Stream、EPEL、PGDG 14，不会使用主机既有
  仓库或插件；若受管源、固定 key 或 RPM 签名来源校验失败，应修复精确候选契约，
  不能用任意第三方源绕过。
- CentOS SELinux Enforcing 当前应在主机变更前失败关闭。仓库中的 policy/helper
  未集成；关闭 SELinux 不是修复。
- v1.2.0 是 0-supported candidate baseline。分支存在或契约通过不代表正式支持。

## 2. 只读验证与服务状态

已完成 Setup 的主机先运行：

```bash
bash /root/probe-panel-install.sh validate
# Debian/Ubuntu 使用 postgresql.service；CentOS 使用 postgresql-14.service。
POSTGRES_UNIT=postgresql.service
systemctl is-active "$POSTGRES_UNIT" probe-api.service nginx.service \
  probe-postgres-backup.timer
systemctl status "$POSTGRES_UNIT" probe-api.service nginx.service \
  probe-postgres-backup.timer --no-pager
nginx -t
ss -H -lnt
curl --disable --silent --show-error --write-out '\nHTTP %{http_code}\n' \
  http://127.0.0.1:8080/internal/health/live
curl --disable --silent --show-error --write-out '\nHTTP %{http_code}\n' \
  http://127.0.0.1:8080/internal/health/ready
```

预期：

- live/ready 为 200；ready 503 通常表示数据库或迁移未就绪；
- API 只监听 `127.0.0.1:8080`；
- PostgreSQL 5432 只监听 `127.0.0.1` 和/或 `::1`；
- IP 模式监听管理 `18455`，domain 模式遵守 80/443 合同；
- Setup/Finalizer 完成后不再 active，正式入口不重新开放 `/install`。

日志只取最小时间窗，并使用 request ID 关联：

```bash
journalctl -u probe-api.service --since '-15 minutes' --no-pager
journalctl -u nginx.service --since '-15 minutes' --no-pager
journalctl -u "$POSTGRES_UNIT" --since '-15 minutes' --no-pager
```

不同平台的 PostgreSQL unit 由安装时记录的平台合同决定；RPM 为
`postgresql-14.service`，不要把 Debian unit 名硬套到 CentOS。

## 3. 管理入口无法访问

先设定唯一入口：

```bash
export ADMIN_URL='https://admin.example.invalid'
curl --disable --verbose --output /dev/null "$ADMIN_URL/"
```

### IP 模式

`ADMIN_URL` 应为 `https://IP:18455`（IPv6 使用方括号）。用安装生成的固定私有 CA：

```bash
curl --disable --fail --cacert /etc/probe-panel/tls/private-ca/ca.pem \
  "$ADMIN_URL/"
/srv/probe/api/probe-api config validate-ingress-tls ip '<规范IP>'
```

证书必须有唯一规范 IP SAN、ServerAuth 和正确签发关系。Certbot timer 应为
disabled/inactive。不要用 HTTP 或 `--insecure` 作为修复。

### Domain 模式

检查一个管理域名的 DNS、证书链、SAN、有效期和 ACME 续期：

```bash
getent ahosts admin.example.invalid
openssl s_client -connect admin.example.invalid:443 \
  -servername admin.example.invalid -verify_return_error </dev/null
systemctl is-enabled certbot.timer
systemctl is-active certbot.timer
```

当前 domain 模式独占 80/443，不支持与活动既有 Nginx 站点共存。若安装器因冲突
失败，不要手工停业务绕过；选择 IP 模式或另行评审停机/共存方案。

curl 会读取代理环境变量。内网/IP 排查时正确设置 `NO_PROXY`，避免代理改变 Nginx
看到的来源地址；不要在报告中复制带认证信息的代理 URL。

## 4. 403、401、404 与 429

管理入口常见状态：

| 状态 | 含义 | 优先检查 |
| ---: | --- | --- |
| 401 | 凭据错误，或 Session 缺失/过期/撤销 | 登录、账户启用、Session 生命周期 |
| 403 | 白名单、Origin 或 CSRF 失败 | 来源 IP、`PROBE_ADMIN_ORIGIN`、CSRF |
| 404 | 路由不属于管理入口，或配置未加载 | URL、Nginx include、Setup 是否关闭 |
| 429 | 登录限流 | 停止重试，等待 `Retry-After` |

白名单真源是 `/etc/probe-panel/admin-allowlist.geo`。先让 API 验证，再测试 Nginx：

```bash
/srv/probe/api/probe-api \
  config validate-admin-allowlist /etc/probe-panel/admin-allowlist.geo
nginx -t
systemctl reload nginx.service
```

空白名单会有意拒绝浏览器请求。Nginx/API 依据直接连接来源，客户端伪造
`X-Forwarded-For` 不能绕过。存在可信前置代理时必须重新评审整体信任边界，不能
临时放宽为 `/0`。

无 Cookie 的 `/api/v1/auth/me` 与 `/api/v1/admin/users` 应返回 401。写请求还要求
完全匹配的同源 Origin 和 Session 绑定 CSRF；不要关闭这些校验来处理 403。

## 5. 数据库、迁移与备份

readiness 为 503 时检查 PostgreSQL/API 最近日志和平台精确客户端路径。部署器使用：

```text
probe-api migrate status
probe-api migrate up
probe-api migrate status
```

不要在交互历史或报告中展开数据库 URL，也不要手工改写 `schema_migrations`。

恢复只能用安装的 root 协调器，并先复制备份到异机：

```bash
/usr/local/lib/probe-panel/restore-management.sh \
  --confirm-database '<数据库名>' \
  /absolute/path/to/probe-TIMESTAMP.dump
```

恢复开始后失败会让 API 和备份 timer 保持 stopped/disabled，等待人工检查；不要
反复启动部分迁移的 API。归档必须有匹配 SHA、`pg_restore --list` 可读且属于受管
daily/weekly 路径。

## 6. 升级失败与事务恢复

升级在数据库备份前后会报告保留路径。迁移是 forward-only：即使链接/服务资产
回滚，已成功的数据库迁移不会自动降级。

安装器按阶段恢复：

- 备份/迁移前后但未切应用时，只恢复 PostgreSQL 原活动状态；
- 链接或服务资产失败时，恢复旧 API/admin/migrations 链接、精确 unit/脚本、
  Nginx include 和 enablement；
- API/timer/Nginx 已开始切换后，还恢复原服务活动状态；
- HUP/INT/TERM 走相同 EXIT journal，原退出码保留。

若看到“snapshot cleanup requires manual removal”，运行态已经提交成功；按消息中的
root-only `/var/tmp/probe-service-assets.*` 路径人工审查，不要把它当成需自动回滚的
失败。若回滚步骤本身告警，保留路径、unit、链接、`systemctl status` 和最小日志，
停止再次升级，先比对备份和旧 release。

## 7. 卸载问题

普通卸载：

```bash
bash /root/probe-panel-install.sh uninstall
```

它只停用/移除 Probe 管理端的九个激活资产，保留配置、TLS、数据库、备份、inactive
release、probe-api 账号以及共享 Nginx/PostgreSQL。`purge` 未实现。遇到未知
enablement 状态或无法证明资产归属时会在变更前失败；不要用递归删除代替。

## 8. 交付证据

问题报告至少包含：UTC 时间、精确 platform/架构/入口、Release/tag/commit、外层与
内层 SHA、`validate` 退出码、`nginx -t`、live/ready、最小监听摘要、故障窗口日志和
变更前后状态。不得包含秘密。

静态契约、容器结果和临时 Actions Artifact 只可作为回归信息。正式 cell 证据必须
来自 full-system VM、真实 reboot 以及不可变 Release；当前账本仍是 60/0。
