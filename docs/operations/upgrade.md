# 生产升级

升级只从同步到 Debian 13 的新源码目录开始。不要在 Windows 本地安装依赖或生成构建产物，也不要把新源码直接覆盖 `/srv/probe` 中的活动发布。

## 1. 升级前检查

1. 把新版本同步到新的只含源码目录，例如 `/var/tmp/probe-source-20260824/`。
2. 阅读版本的数据库迁移和配置变化；确认当前备份空间充足。
3. 确认 `/srv/probe/config/probe-api.env`、活动 Nginx 配置、TLS 和白名单已由配置管理或离线方式另行备份。
4. 确认 PostgreSQL、API 和 Nginx 当前健康：

```bash
bash /var/tmp/probe-source-20260824/probe-api/deploy/scripts/validate-production.sh runtime
```

脚本不会更新系统包，也不会修改防火墙。若新版本需要新的 Debian 包，先按变更说明人工安装。

## 2. 只验证、不切换

建议先运行：

```bash
cd /var/tmp/probe-source-20260824
bash probe-api/deploy/scripts/upgrade.sh \
  --source-root /var/tmp/probe-source-20260824 \
  --validate-only
```

该模式会在临时目录中分别测试并构建 `probe-api`、`probe-agent`、`probe-web` 和 `probe-admin`，并检查当前活动配置、systemd 与 Nginx，但不会：

- 创建数据库备份；
- 执行迁移；
- 切换发布链接；
- 重启或重载服务。

它会解析新源码中的三个 systemd 单元，并按新源码模板核对活动 Nginx 三 Host 与全部路由；不会只检查旧的已安装单元。验证全程持有与备份/恢复共用的数据库维护锁，因此不能与定时备份或恢复演练并行。

依赖下载产生的 Go/npm 缓存仅存在于 Debian 构建环境。两个前端的 `node_modules` 和 `dist` 位于临时构建副本，不写回同步源码目录。

## 3. 执行升级

验证通过后执行：

```bash
bash probe-api/deploy/scripts/upgrade.sh \
  --source-root /var/tmp/probe-source-20260824
```

默认必须通过四个工程的测试和检查。`--skip-tests` 只用于已经在同一份同步源码上完成等价验证的紧急维护窗口，不能作为常规流程。

升级顺序固定为：

1. 源码和活动配置预检；
2. 四工程独立测试与构建，其中 Agent 同时生成 amd64/arm64 首次安装资产和 SHA256 清单；
3. 新源码 systemd 单元、Nginx 三 Host/路由、白名单和发布产物校验；
4. 新发布写入 `/srv/probe/releases/<release-id>/` 并生成 SHA-256 清单；
5. `pg_dump` 写入 `/srv/probe/backups/pre-upgrade-<release-id>.dump`，再用 `pg_restore --list` 验证；
6. 新 API 二进制执行前向迁移；
7. 原子替换 API、Agent 下载、游客前端、管理前端和迁移清单五个活动链接；
8. API 重启、Nginx reload-or-restart、readiness 和监听地址验收。

升级不会覆盖：

```text
/srv/probe/config/probe-api.env
/srv/probe/config/nginx/nginx.conf
/etc/probe-panel/admin-allowlist.geo
/etc/probe-panel/tls/
/srv/probe/backups/
PostgreSQL 数据目录
```

升级不会把新源码中的示例复制到 `/srv/probe/config`。比较配置时直接使用本次同步源码中的 `probe-api/config/*.example` 和 `probe-api/deploy/nginx/nginx.conf`，避免把旧 `.example` 误认为当前版本契约。

## 4. 升级后验收

```bash
bash probe-api/deploy/scripts/validate-production.sh all \
  --source-root /var/tmp/probe-source-20260824

systemctl status probe-api nginx postgresql --no-pager
journalctl -u probe-api --since '-10 minutes' --no-pager
```

还应实际验证：

- 游客 Host 无需登录即可读取允许的 Panel API，但没有登录页或管理 API；
- 管理 Host 未登录 API 返回 401，管理员登录和写操作正常；
- API Host 的 Agent 注册、配置和上报路由正常，五项固定发布资产可下载且 SHA256 通过；私有 CA 预览还应验证 `ca.pem` 与命令指纹一致。未知下载和浏览器管理路由返回 404；panel/admin Host 的下载路径返回 404；
- 非白名单来源不能读取两个前端的 HTML、JS、CSS；
- Nginx 只监听 80/443，API 仅监听 `127.0.0.1:8080`，PostgreSQL 未暴露公网；
- Agent 在升级窗口后继续上报，且没有任何反向连接或远程升级行为。

## 5. 失败和回退

构建、配置、Nginx 或备份失败发生在迁移和切换之前，不会改变活动发布。迁移成功后若新服务未通过 readiness，脚本会恢复旧的 API、Agent 下载和前端链接，并重启旧 API；同时保留：

- 失败的新发布目录；
- 迁移前 PostgreSQL 备份；
- 已经提交的前向数据库迁移。

数据库迁移不会自动执行 down。旧二进制是否兼容新结构必须以该版本迁移说明为准。若必须恢复数据库，应进入维护窗口，停止写入并按照[备份与恢复](backup-restore.md)使用脚本打印的 custom-format 备份；不要只恢复数据库而保留不匹配的应用发布。

旧发布不会自动删除。确认新版本稳定并且备份保留策略满足要求后，才可人工清理不再需要且不被任何活动链接引用的旧发布目录。

## 6. 配置变更

升级脚本永远不把新示例覆盖到活动配置。比较时应避免把密码打印到终端或日志：

```bash
diff -u \
  /var/tmp/probe-source-20260824/probe-api/config/probe-api.env.example \
  /path/to/offline-redacted-active-env
```

修改活动配置后先执行 `upgrade.sh --validate-only`。活动 Nginx 片段只允许基于当前源码模板替换三个域名；证书继续放在模板固定路径。修改白名单时，必须先通过同一份 API 二进制的 allowlist 校验和 `nginx -t`，再重启 API、重载 Nginx，避免两个白名单层漂移。
