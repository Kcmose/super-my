# 管理端手工验收清单

本文只验收独立 management 产品：`probe-admin + probe-api + PostgreSQL`。Agent 与
访客前端不属于本清单，不要求 `18453`、`18454`、Agent 下载站或游客页面存在。
未配置 Agent 时，`agent.status=not_configured` 以及 Agent 路由/令牌失败关闭是正确结果。

当前 v1.2.0 是 `promotion_eligible=false` 的 candidate baseline，机器账本为
60 candidate、0 supported。本清单通过只能形成候选证据，不能直接改写正式状态。

## 1. 记录精确 cell

验收前记录且不要横向外推：

- 精确 platform ID、`/etc/os-release` 哈希、systemd 版本与 PID 1；
- `uname -m` 对应的 `amd64` 或 `arm64`；
- 本 cell 唯一入口：`ip` 或 `domain`；
- VM image ID/哈希、Release tag、source commit、外层资产 SHA-256 与内层 manifest SHA；
- 测试开始 UTC 时间和测试人。

一个 cell 只执行所选入口的 `fresh`。平台级正式支持必须另有
`amd64/ip`、`amd64/domain`、`arm64/ip`、`arm64/domain` 四格完整证据。

## 2. 无修改预检

- [ ] 未列出的发行版、错误 init、错误架构和低于下限的 systemd 在主机变更前退出。
- [ ] 旧版/EOL/延长维护层缺少 `--accept-eol` 时退出；Debian/Ubuntu 仅创建隔离受管
  source 且不覆盖用户源，缺 keyring/HTTPS method/目标架构 PG14 时失败关闭。
- [ ] 缺少 Bash、Python 3、CA、安全 HTTPS 下载器或校验工具时列明缺项并退出。
- [ ] 外层校验失败、危险 tar 成员、额外根目录、内层清单错误或平台 ABI 不匹配时退出。
- [ ] 外露 PostgreSQL、第三方 Web 栈、归属不明 Probe 路径及入口冲突按承诺失败。
- [ ] 失败前后比较包、账号、unit、服务状态、Nginx 配置和 Probe 永久路径，确认无修改。
- [ ] CentOS SELinux Enforcing 当前在任何主机变更前失败关闭；Permissive/Disabled
      结果不能成为正式支持证据。

## 3. 首次 Setup

- [ ] bootstrap 只安装 management bundle，不出现 Agent/游客二进制、服务或入口。
- [ ] 安装终端和 `/install` 都不显示或要求安装码。
- [ ] 使用 root SSH 把 `/run/probe-panel-setup/setup.sock` 转发到本机
      `127.0.0.1:18080`；服务器没有 TCP 18080 监听。
- [ ] Setup 父目录是 `root:root 0700`，Socket 是 `root:root 0600`。
- [ ] `pending/configuring` 刷新或重启后可安全重签短期会话；
      `finalizing/installed/recovery_required` 不会重新开放配置。
- [ ] 只配置本机 PostgreSQL、一个管理入口、TLS、白名单和首个管理员。
- [ ] 管理员/数据库密码不会进入命令行、日志、环境转储或验收附件。

## 4. 入口专项

### IP cell

- [ ] 只生成 `https://IP:18455/login`，`18453/18454` 不是成功条件。
- [ ] 私有 CA、叶证书与私钥权限正确，叶证书只有规范 IP SAN 和 ServerAuth。
- [ ] 显式信任 CA 后 TLS 校验成功；不使用明文 HTTP 或 `--insecure`。
- [ ] Certbot timer 保持 disabled/inactive。

### Domain cell

- [ ] 只接受一个规范管理 FQDN，管理入口使用 80/443 和公信 ACME。
- [ ] DNS、ACME、证书链、SAN、有效期和续期 timer 均通过验证。
- [ ] 当前 domain 合同为 Probe 独占 80/443；活动 Nginx/既有站点冲突时失败关闭，
      不伪装成共存成功。

## 5. 管理产品功能

- [ ] 管理登录成功；错误口令使用统一提示，限流和 `Retry-After` 生效。
- [ ] Session 刷新可恢复，退出后受保护路由回到登录页。
- [ ] 写请求同时要求同源 Origin、当前 Session 和 CSRF；缺一项均失败。
- [ ] 管理白名单内可访问，白名单外返回 403；客户端伪造转发头不能绕过。
- [ ] 系统状态中 API/PostgreSQL 为 ready，管理安全边界显示已启用。
- [ ] 可创建、编辑、禁用和删除节点配置；非法周期/挂载点由前端和 API 双重拒绝。
- [ ] 未配置 Agent 时状态明确为 `not_configured`，Agent 路由不注册，安装命令和
      Token 签发返回失败状态且不产生秘密。
- [ ] 管理员创建、禁用、改密、删除和“保留最后一个可用管理员”约束正确。
- [ ] 审计记录关键管理操作，但不含密码、Cookie、CSRF、数据库凭据或 Token。
- [ ] 页面中不存在 SSH、终端、命令执行、文件管理或远程服务控制能力。

## 6. 本机运行边界

```bash
# Debian/Ubuntu 使用 postgresql.service；CentOS 使用 postgresql-14.service。
POSTGRES_UNIT=postgresql.service
systemctl is-active "$POSTGRES_UNIT" probe-api.service nginx.service \
  probe-postgres-backup.timer
curl --fail http://127.0.0.1:8080/internal/health/live
curl --fail http://127.0.0.1:8080/internal/health/ready
nginx -t
ss -H -lnt
```

- [ ] API 只监听 `127.0.0.1:8080`。
- [ ] PostgreSQL 5432 只监听 `127.0.0.1` 和/或 `::1`。
- [ ] IP cell 只有管理 `18455`；domain cell 使用其 80/443 合同。
- [ ] Setup 与 Finalizer 完成后停用，正式入口的 `/install` 和
      `/api/v1/setup/*` 永久失败关闭。
- [ ] 原生 Nginx/PostgreSQL 包归属、unit 和实际运行命令与平台档案一致。

## 7. 生命周期和故障注入

- [ ] 真实重启前后 boot ID 不同；重启后 API、Nginx、PostgreSQL、备份 timer
      恢复，Setup 不重开。
- [ ] v1.2.1 及以后从同平台、同架构、同入口的不可变前序 management 版本升级。
      v1.2.0 没有合法前序，不能用历史 full v1.1.0 伪造升级证据。
- [ ] 在数据库备份、迁移、链接切换、服务资产安装和运行验证各阶段注入失败。
- [ ] 失败时旧链接、精确 unit/脚本/Nginx include、enablement 和原服务活动状态恢复；
      已完成的前向迁移及备份路径被明确报告。
- [ ] HUP/INT/TERM 走同一事务回滚，退出码保真；二次信号不会中断回滚。
- [ ] 生成 custom-format PostgreSQL 备份、校验 SHA 和 `pg_restore --list`，并在
      隔离环境完成恢复和迁移后验证。
- [ ] 普通卸载只移除九个 Probe 激活资产，保留配置、TLS、数据库、备份、release
      目录及共享 Nginx/PostgreSQL；不提供隐式 purge。

## 8. 正式证据

每个场景保存原始输出、退出码、前后状态摘要和 SHA-256，至少包括 `fresh`、
`coexistence`、`conflict`、`no_mutation`、`reboot`、`upgrade`、`fault`、
`backup_restore`、`uninstall`；EOL cell 另有 `eol_repository`，CentOS 另有真实
`selinux_enforcing`。证据必须来自 full-system VM 和不可变 GitHub Release 资产。

容器、静态契约、一次安装成功、Actions 临时 Artifact 或相邻版本结果都不能代替
正式证据。v1.2.0 即使通过除升级外的候选验收，也仍保持 0 supported。
