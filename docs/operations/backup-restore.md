# PostgreSQL 备份与恢复

本文适用于 Debian 13 上的 Probe Panel PostgreSQL。备份覆盖整个 `probe` 数据库，包括管理员、节点、Token 状态、探测配置、审计日志和探测历史。默认每天备份一次，保留最近 7 个每日归档，并在每周日额外保留一个副本，最多保留 4 个每周归档。

备份脚本生成 PostgreSQL custom-format 归档和对应的 SHA-256 文件。归档只有同时通过 `pg_restore --list` 与 SHA-256 校验后才会进入保留集。文件名中的时间使用 UTC；每周判定使用服务器本地时区，`1` 至 `7` 分别表示周一至周日。

## 1. 安装

安装 PostgreSQL 客户端、`flock` 和校验工具：

```bash
apt-get update
apt-get install --yes postgresql-client coreutils util-linux
```

完整执行 `install.sh` 时会原子安装以下脚本和 systemd 单元并启用 timer。若需要在已有部署上单独补装，以下命令均从源码仓库根目录执行；运行用户沿用 API 服务的 `probe-api` 用户：

```bash
install -d -o root -g probe-api -m 0750 /srv/probe/api/scripts
install -o root -g probe-api -m 0750 probe-api/deploy/scripts/backup-postgres.sh /srv/probe/api/scripts/backup-postgres.sh
install -o root -g probe-api -m 0750 probe-api/deploy/scripts/restore-postgres.sh /srv/probe/api/scripts/restore-postgres.sh

install -d -o probe-api -g probe-api -m 0700 /var/backups/probe-panel/postgres

install -o root -g root -m 0644 probe-api/deploy/systemd/probe-postgres-backup.service /etc/systemd/system/probe-postgres-backup.service
install -o root -g root -m 0644 probe-api/deploy/systemd/probe-postgres-backup.timer /etc/systemd/system/probe-postgres-backup.timer
systemctl daemon-reload
```

`/var/backups/probe-panel/postgres` 必须由 `probe-api` 用户拥有且权限为 `0700`。脚本首次运行时只会初始化空目录，并写入固定标记；如果目录非空但缺少标记，脚本会拒绝运行。不要手工伪造、移动或删除 `.probe-postgres-backup-root`。

## 2. 数据库凭据与参数

备份与恢复服务只接受离散的 libpq 环境变量和 `.pgpass`，不读取 API 的整条 `PROBE_DATABASE_URL`。这样可以确保数据库密码不会出现在 `pg_dump`、`pg_restore`、`psql` 的命令参数或进程列表中。先从 `/srv/probe/config/probe-postgres-backup.env.example` 创建权限为 `0600`、属主为 root 的活动文件；内容如下：

```ini
PGHOST=127.0.0.1
PGPORT=5432
PGDATABASE=probe
PGUSER=probe
PGPASSFILE=/srv/probe/config/probe-postgres.pgpass

PROBE_POSTGRES_BACKUP_DIR=/var/backups/probe-panel/postgres
PROBE_POSTGRES_DAILY_KEEP=7
PROBE_POSTGRES_WEEKLY_KEEP=4
PROBE_POSTGRES_WEEKLY_DAY=7
```

`PGHOST`、`PGPORT`、`PGDATABASE` 和 `PGUSER` 均为必填；`PGDATABASE` 必须是数据库名，不能填 URL 或关键字连接串。若修改备份根目录，还必须同步修改 systemd 单元中的 `RequiresMountsFor`、`ReadWritePaths`，以及恢复命令中的 `ReadWritePaths`；否则 `ProtectSystem=strict` 会按设计拒绝写入。不要为了绕过该错误而放宽整个文件系统权限。

保护配置文件：

```bash
chown root:root /srv/probe/config/probe-postgres-backup.env
chmod 0600 /srv/probe/config/probe-postgres-backup.env
```

`.pgpass` 每行格式为 `hostname:port:database:username:password`。把真实密码只写入该文件，不要写进脚本、systemd 单元、命令行或本文档：

```text
127.0.0.1:5432:probe:probe:<数据库密码>
```

```bash
chown probe-api:probe-api /srv/probe/config/probe-postgres.pgpass
chmod 0600 /srv/probe/config/probe-postgres.pgpass
```

`probe-api` 用户必须能够遍历 `/srv/probe/config` 并读取 `.pgpass`。脚本会拒绝符号链接、非当前服务用户所有或向组/其他用户开放的 `PGPASSFILE`。

## 3. 启用每日备份

```bash
systemctl enable --now probe-postgres-backup.timer
systemctl list-timers probe-postgres-backup.timer
```

默认每天本地时间 `02:15` 触发，并随机延迟最多 15 分钟，避免多个定时任务同时启动；关机错过的任务会在下次启动后补跑。每周日成功生成每日备份后，还会复制同一归档到 `weekly/`。

手工触发并查看结果：

```bash
systemctl start probe-postgres-backup.service
systemctl status probe-postgres-backup.service
journalctl -u probe-postgres-backup.service --since today
```

成功后的目录结构：

```text
/var/backups/probe-panel/postgres/
├── .probe-postgres-backup-root
├── daily/
│   ├── probe-YYYYMMDDTHHMMSSZ.dump
│   └── probe-YYYYMMDDTHHMMSSZ.dump.sha256
└── weekly/
    ├── probe-YYYYMMDDTHHMMSSZ.dump
    └── probe-YYYYMMDDTHHMMSSZ.dump.sha256
```

保留轮换只删除上述两个目录内、且严格匹配 `probe-YYYYMMDDTHHMMSSZ.dump` 的旧归档及其同名校验文件。脚本锁定备份根目录 inode；安装和升级在构建、备份、迁移与切换期间获取同一把数据库维护锁，因此备份、恢复和部署三类任务不能并行。

本地备份无法防止整机或磁盘损坏。应把已经校验的 `.dump` 与 `.sha256` 成对复制到受访问控制和加密保护的异机存储，并在复制后再次验证校验和。

## 4. 从指定归档恢复

恢复会清理归档中包含的现有数据库对象并重新写入数据，属于破坏性操作。脚本必须同时满足以下条件才会开始：

- 归档位于受标记备份根下的 `daily/` 或 `weekly/`，不是符号链接；
- 归档和校验文件都归运行用户所有，且没有组或其他用户权限；
- SHA-256 与 custom-format 归档目录均校验通过；
- `--confirm-database` 与数据库实际返回的名称完全一致；
- 目标不是 `postgres`、`template0` 或 `template1`；
- 除恢复脚本自身外，目标数据库没有其他会话；
- 没有备份、其他恢复或安装/升级进程持有数据库维护锁。

恢复前先确认已存在一份可用的当前安全备份；随后停止定时器和 API，并等待正在运行的备份自然完成：

```bash
systemctl stop probe-postgres-backup.timer
systemctl is-active probe-postgres-backup.service
systemctl stop probe-api.service
```

第二条命令必须返回 `inactive`。如果返回 `activating`，等待备份完成，不要强制终止；同时确认没有 `install.sh`、`upgrade.sh` 或另一恢复任务正在运行。即使人工漏查，共用锁也会在改动数据库前拒绝并发任务，不要绕过该锁。

通过临时 systemd 单元加载受保护的环境文件，并以 `probe-api` 用户恢复。下面的数据库名和归档路径必须替换为已经人工核对的精确值：

```bash
systemd-run \
  --unit=probe-postgres-restore \
  --wait \
  --pipe \
  --collect \
  --property=User=probe-api \
  --property=Group=probe-api \
  --property=UMask=0077 \
  --property=NoNewPrivileges=true \
  --property=ProtectSystem=strict \
  --property=ReadOnlyPaths=/srv/probe/config \
  --property=ReadWritePaths=/var/backups/probe-panel/postgres \
  --property=EnvironmentFile=/srv/probe/config/probe-postgres-backup.env \
  /srv/probe/api/scripts/restore-postgres.sh \
  --confirm-database probe \
  /var/backups/probe-panel/postgres/daily/probe-YYYYMMDDTHHMMSSZ.dump
```

脚本通过 `pg_restore` 生成清理与恢复 SQL，再由 `psql --single-transaction` 执行；任何 SQL 错误都会中止并回滚该事务。数据库密码只从 systemd 加载的环境或 `PGPASSFILE` 读取。

恢复成功后先检查迁移状态，再启动 API：

```bash
systemd-run \
  --unit=probe-postgres-migration-check \
  --wait \
  --pipe \
  --collect \
  --property=User=probe-api \
  --property=Group=probe-api \
  --property=EnvironmentFile=/srv/probe/config/probe-api.env \
  /srv/probe/api/probe-api migrate status

systemctl start probe-api.service
systemctl enable --now probe-postgres-backup.timer
systemctl status probe-api.service probe-postgres-backup.timer
```

如果归档版本早于当前程序版本，应在启动 API 前使用同样的受保护环境执行 `/srv/probe/api/probe-api migrate up`。迁移失败时不要启动 API，应保留日志和原归档排查。

## 5. 定期恢复演练

至少每月把一个最近备份恢复到隔离测试数据库，不要直接拿生产库做演练。以下示例给临时数据库使用独立环境文件和精确匹配的 `.pgpass`，避免生产 `.pgpass` 只匹配 `probe` 时认证失败。

先由 PostgreSQL 管理员创建隔离库，再创建两个临时配置文件：

```bash
sudo -u postgres createdb --owner=probe probe_restore_test

install -o probe-api -g probe-api -m 0600 /dev/null \
  /srv/probe/config/probe-postgres-restore-test.pgpass
editor /srv/probe/config/probe-postgres-restore-test.pgpass
# 只写一行：127.0.0.1:5432:probe_restore_test:probe:<数据库密码>

install -o root -g root -m 0600 \
  /srv/probe/config/probe-postgres-backup.env \
  /srv/probe/config/probe-postgres-restore-test.env
editor /srv/probe/config/probe-postgres-restore-test.env
# 只修改：
# PGDATABASE=probe_restore_test
# PGPASSFILE=/srv/probe/config/probe-postgres-restore-test.pgpass
```

人工复核临时环境文件仍包含全部 9 个键后，以隔离环境执行恢复：

```bash
systemd-run \
  --unit=probe-postgres-restore-test \
  --wait \
  --pipe \
  --collect \
  --property=User=probe-api \
  --property=Group=probe-api \
  --property=UMask=0077 \
  --property=NoNewPrivileges=true \
  --property=ProtectSystem=strict \
  --property=ReadOnlyPaths=/srv/probe/config \
  --property=ReadWritePaths=/var/backups/probe-panel/postgres \
  --property=EnvironmentFile=/srv/probe/config/probe-postgres-restore-test.env \
  /srv/probe/api/scripts/restore-postgres.sh \
  --confirm-database probe_restore_test \
  /var/backups/probe-panel/postgres/daily/probe-YYYYMMDDTHHMMSSZ.dump
```

随后使用同一环境运行 `psql -X --no-password`，验证迁移表、管理员/节点/探测配置数量和关键历史时间范围，并记录归档名、校验值、恢复耗时和结果。演练完成后，由 PostgreSQL 管理员再次核对精确数据库名再删除隔离库；确认不再需要临时凭据后删除两个精确路径：

```bash
sudo -u postgres dropdb probe_restore_test
rm -f -- \
  /srv/probe/config/probe-postgres-restore-test.env \
  /srv/probe/config/probe-postgres-restore-test.pgpass
```

恢复演练成功才证明归档可用于恢复；仅有 `pg_dump` 成功日志或校验和不足以替代恢复测试。

## 6. 常见失败处理

- `refusing to initialize a non-empty backup root`：目录中已有未知内容，不能直接启用轮换。迁移旧文件到其他位置，确认目录为空后再运行。
- `another backup or restore operation is running` 或 `a PostgreSQL backup, restore, or another deployment is running`：等待当前备份、恢复或部署任务结束；不要尝试绕过目录互斥锁。
- `backup checksum verification failed`：归档可能损坏，禁止恢复；改用另一份已验证归档，并检查存储介质。
- `target database still has ... session(s)`：确认 API 和其他客户端都已停止，再重试。
- `PGPASSFILE must not grant group or other permissions`：将文件所有者改为任务运行用户，并设置 `0600`。
- timer 失败：使用 `systemctl status probe-postgres-backup.service` 和 `journalctl -u probe-postgres-backup.service` 查看原因。不要为了让定时器变绿而删除未核对的备份目录或标记文件。
