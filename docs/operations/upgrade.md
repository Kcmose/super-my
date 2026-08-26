# 管理端升级、验证与卸载

本文只适用于独立的 management 产品，即 `probe-admin` 管理界面与 `probe-api` API。
它不升级、验证或卸载 Agent 和访客前端，也不会接管无关 Nginx 站点或 PostgreSQL
数据库。

`management v1.2.0` 是尚未发布的第一个独立 management candidate baseline，
没有合法的 management 前序版本，所以它不能作为升级目标取得 `upgrade` 证据，
机器账本固定为 60 candidate、0 supported。第一个有效升级目标是 v1.2.1：每个
cell 必须从同平台、同架构、同入口的不可变 v1.2.0 升级。下面的 v1.2.1 命令只是
未来晋级合同，不是当前可执行的生产升级通知。

## 1. 统一使用根 `install.sh`

v1.2 起，管理端生命周期入口统一为完整的 standalone 根 `install.sh`：

```text
install [--accept-eol]   首次安装管理端 Setup
upgrade [--accept-eol]   升级已完成 Setup 的 v1.2+ 管理端
validate                 只读验证已安装的管理端
status                   查看 bootstrap/Setup 状态，不输出秘密
uninstall                普通卸载并保留数据
```

`purge` 不受支持。历史三产品整包 `v1.1.0` 不能直接使用这一管理端升级路径；必须按
其不可变旧版本文档处理，再设计经过备份验证的独立迁移。

不要从 `main`、工作树或未经校验的临时 URL 执行生产升级。正式发布后，应先把完整
脚本保存到 root-only 路径，核对发布方给出的 SHA-256，再运行；不要把数据库密码、
管理员密码或 Token 放进参数。下文用 `/root/probe-panel-install-v1.2.1.sh` 表示这份
已校验脚本。

## 2. 升级前检查

升级只接受已经完成 Setup、且安装了 v1.2+ management lifecycle 工具的主机。先执行：

```bash
sudo bash /root/probe-panel-install-v1.2.1.sh validate
sudo bash /root/probe-panel-install-v1.2.1.sh status
```

`validate` 调用已安装的 `/usr/local/lib/probe-panel/validate-management.sh all`，只读
检查以下内容：

- 当前主机仍精确匹配安装时记录的 ABI v2 平台 ID 和 systemd profile；
- 活动 API、管理 SPA、迁移目录和 release 内层 SHA 清单未漂移；
- 配置、白名单、TLS、Nginx 管理端片段和 lifecycle 工具安全且归属正确；
- PostgreSQL、`probe-api`、Nginx、备份 timer、TLS、readiness 和监听边界健康；
- IP 模式仍只使用管理 IP 入口，domain 模式仍遵守其独占 80/443 与 Certbot 契约。

同时确认：

1. 最近一次 custom-format PostgreSQL 备份及 `.sha256` 已复制到异机存储并完成恢复
   演练；
2. `/srv/probe/config`、`/etc/probe-panel` 和必要的 TLS/白名单材料已有离线备份；
3. 当前没有备份、恢复、另一安装或升级任务；
4. 已阅读目标 Release 的数据库迁移说明和失败边界；
5. EOL candidate 已确认受管仓库和密钥仍可用，并明确接受 `--accept-eol` 风险。该参数
   不恢复厂商安全维护；Debian/Ubuntu 只维护自己的隔离 source，不覆盖用户源，CentOS
   在完整 Vault/EPEL 隔离链完成前不得升级。

验证失败时不要绕过。`upgrade` 会再次执行同类 host 与平台检查，并持有部署锁和
数据库维护锁。

## 3. 执行 management-only 升级

不可变 v1.2.0 baseline 与正式 v1.2.1 目标资产均发布后，常规候选平台执行：

```bash
sudo bash /root/probe-panel-install-v1.2.1.sh upgrade
```

账本中标记为 EOL 的候选平台必须显式执行：

```bash
sudo bash /root/probe-panel-install-v1.2.1.sh upgrade --accept-eol
```

当前 v1.2.0 baseline 与 v1.2.1 目标都未发布，因此现在运行会因缺少匹配的不可变 Release 资产而失败关闭。
不要通过修改版本常量、替换下载地址或跳过校验来使它继续。

升级流程固定为：

1. 精确识别 OS、版本、架构、systemd 和 EOL 层级，验证已安装 management host；
2. 下载该架构唯一的 management bundle 与 `SHA256SUMS`，校验外层 SHA、限制大小，
   再用安全解包器拒绝链接、设备、路径逃逸和额外根目录；
3. 校验 `RELEASE-MANIFEST` 的 management profile、ABI v2、精确平台集合，以及包内
   `BUNDLE-SHA256SUMS` 白名单；目标机不会安装 Go/Node 或现场构建源码；
4. 再次确认平台指纹未变化，调用已校验 bundle 内的 `install-release.sh`；
5. 验证当前配置、TLS、白名单、Nginx、备份凭据和可切换链接，写入唯一的不可变
   `/srv/probe/releases/<release-id>/`；
6. 记录服务活动状态，先停下 API 与备份 timer，再创建并用 `pg_restore --list`
   校验 PostgreSQL 备份，然后执行新 API 的 forward-only migration，避免旧 API 在
   迁移窗口继续写库；
7. 只原子切换 API、管理 SPA 和 migrations 三个 management 链接，安装对应平台的
   service/backup/lifecycle 资产；
8. 重启 API、恢复备份 timer、reload-or-restart Nginx，并重新验证 TLS、readiness、
   服务和监听边界。

升级不会安装或切换 Agent、访客前端，也不会改变 ingress 模式、重签证书、覆盖活动
环境文件或清理旧 release。它保留配置、TLS、白名单、PostgreSQL 数据和既有备份。

## 4. 升级后验收

脚本成功返回后再次执行：

```bash
sudo bash /root/probe-panel-install-v1.2.1.sh validate

systemctl status probe-api.service nginx.service \
  probe-postgres-backup.timer --no-pager
journalctl -u probe-api.service --since '-10 minutes' --no-pager
```

还应实际确认管理员登录和写操作正常、非白名单来源被拒绝、API 只监听
`127.0.0.1:8080`、PostgreSQL 没有公网监听，并检查当前 ingress 对应的管理入口。
Agent 与访客前端是独立产品；它们不属于本次成功条件，也不应因管理端升级被改动。

## 5. 失败和恢复边界

下载、外层/内层校验、平台复核、配置、TLS、Nginx 或迁移前备份失败时，不会切换
活动 release。数据库迁移成功后发生链接、service 资产或 runtime 验证失败，升级器
会从 root-only 快照恢复旧的 API、管理 SPA、migrations 链接、systemd/备份/lifecycle
资产和原服务活动状态；这仍不是数据库回滚。升级器同时保留：

- 失败的新 release 目录；
- 迁移前 PostgreSQL 备份；
- 已提交的 forward-only 数据库迁移。

数据库不会自动执行 down migration，旧二进制也不一定兼容新结构。错误信息给出的
备份路径必须保留；若确需恢复数据库，进入维护窗口并按[备份与恢复](backup-restore.md)
使用安装好的 management restore coordinator。不要只恢复数据库而继续运行与其结构
不匹配的应用 release。该 coordinator 不要求 API 在恢复前通过 readiness；API 已经
`inactive` 或 `failed` 时，仍可在 PostgreSQL、Nginx、宿主资产和配置验证通过后进入
受控维护窗口，恢复后再执行当前不可变 API 的 forward migration 与完整 runtime 验证。

## 6. 只读验证与普通卸载

任意维护窗口都可运行：

```bash
sudo bash /root/probe-panel-install-v1.2.1.sh validate
```

普通卸载前先完成并异机保存最终备份，然后执行：

```bash
sudo bash /root/probe-panel-install-v1.2.1.sh uninstall
```

根入口会委托安装好的 `uninstall-management.sh` 停止并移除 Probe 自有的 API、备份
timer、管理 SPA 链接、Nginx include、systemd 单元和脚本；失败时会尽力恢复暂存路径
和原服务状态。普通卸载明确保留：

```text
/srv/probe/config/
/etc/probe-panel/
/var/lib/probe-panel/
/var/backups/probe-panel/
PostgreSQL 数据库
不可变 release 目录
共享的 Nginx/PostgreSQL 软件包与无关站点
```

删除上述数据属于 purge，必须另行评审、核对精确目标并验证最终备份；不能把
`uninstall` 当作数据清除命令。
