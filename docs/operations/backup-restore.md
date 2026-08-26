# Management PostgreSQL 备份与恢复

本文适用于 `probe-linux-systemd-v2` management 产品，是与发行版包管理器无关的
运维契约。安装器会根据精确平台 ID 选择并验证原生包管理、systemd profile、
PostgreSQL service 和客户端绝对路径；备份与恢复始终通过渲染后的已安装资产执行。
操作者不要按本文手工安装包、复制 unit 或替换平台映射。

`management v1.2.0` 当前仍未发布，60-cell ledger 为 60 candidate、0 supported。
下面描述的是待发布 management bundle 的运维契约，不是当前可下载资产或正式支持
声明。发布后，完整根 `install.sh` 会安装并验证备份/恢复工具。

## 1. 安装后的资产与边界

完成 management Setup 后，安装器负责安装：

```text
/srv/probe/api/scripts/backup-postgres.sh
/srv/probe/api/scripts/restore-postgres.sh
/usr/local/lib/probe-panel/restore-management.sh
/usr/local/lib/probe-panel/validate-management.sh
/usr/local/lib/probe-panel/uninstall-management.sh
/usr/local/lib/probe-panel/deploy-common.sh
/usr/local/lib/probe-panel/management-lifecycle.sha256
/etc/systemd/system/probe-postgres-backup.service
/etc/systemd/system/probe-postgres-backup.timer
```

其中：

- `backup-postgres.sh` 由受限的 `probe-api` one-shot service 调用；
- `restore-postgres.sh` 是 coordinator 使用的低权限数据库执行器，不是生产人工入口；
- `restore-management.sh` 是 root-only 生产恢复入口，负责部署锁、服务静默、恢复、
  forward migration 和运行态复验；
- `validate-management.sh`、`restore-management.sh`、`uninstall-management.sh` 与
  `deploy-common.sh` 由 `management-lifecycle.sha256` 绑定为同一组生命周期闭包；三个
  root 入口在加载公共代码前都会先校验四个文件的 owner、mode、语法和 SHA-256，拒绝
  单文件替换或版本混装；
- service 中的 PostgreSQL 依赖和工具路径由 ABI v2 安装过程按平台渲染；
- 普通 `uninstall` 保留 `/var/backups/probe-panel` 和 PostgreSQL 数据库。

不要从源码目录手工补装这些文件，也不要自行拼装临时 transient unit 来替代 coordinator。
若安装资产缺失或校验失败，应停止并修复/重新部署匹配的不可变 management Release。

## 2. 备份内容、格式与保留策略

备份覆盖安装配置指定的整个 management 数据库，包括管理员、节点、Token 状态、
探测配置、审计日志和探测历史。脚本生成 PostgreSQL custom-format `.dump` 与同名
`.dump.sha256`。归档只有同时通过 `pg_restore --list` 和 SHA-256 校验后才会提交。

默认策略为：

- 每天本地时间 `02:15` 触发；modern systemd timer 随机延迟最多 15 分钟，
  systemd 219/232 使用的 legacy timer 不设置 `RandomizedDelaySec`；
- 保留最近 7 个 daily 归档；
- 每周日把已验证 daily 归档复制为 weekly，保留最近 4 个；
- 文件时间戳使用 UTC，格式为 `probe-YYYYMMDDTHHMMSSZ.dump`；
- 关机错过的 timer 会在下次启动后补跑。

默认目录结构：

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

备份根、daily/weekly、归档与校验文件都必须是无符号链接的受管路径，并满足脚本要求
的 owner/mode。不要伪造、删除或移动 `.probe-postgres-backup-root`；不要把其他文件
放进受管 daily/weekly 目录。轮换只处理严格匹配上述命名的归档。

## 3. 数据库凭据

备份和恢复只使用离散 libpq 环境变量及 `PGPASSFILE`，不会把整条数据库 URL 或密码
放进命令行。活动配置位于：

```text
/srv/probe/config/probe-postgres-backup.env
```

其契约为：

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

环境文件必须是 `root:root 0600`。`.pgpass` 必须是 `probe-api:probe-api 0600`、绝对
路径、普通文件且不是符号链接；每行格式为：

```text
hostname:port:database:username:password
```

不要把真实密码写进脚本、文档、shell history、命令参数或日志。`PGDATABASE` 必须是
普通数据库名，不能是 URL 或关键字连接串。默认 backup root 与 unit sandbox 是同一
契约；不要只改环境文件中的目录而不经过对应 Release 的 unit/权限评审。

## 4. 日常操作与异机副本

检查 timer 和最近结果：

```bash
systemctl status probe-postgres-backup.timer --no-pager
systemctl list-timers probe-postgres-backup.timer --no-pager
journalctl -u probe-postgres-backup.service --since today --no-pager
```

手工生成一份受管备份：

```bash
sudo systemctl start probe-postgres-backup.service
sudo systemctl status probe-postgres-backup.service --no-pager
```

备份、恢复、安装和升级共用数据库维护锁，不能并行；遇到锁冲突应等待当前任务结束，
不得删除 lock、marker 或放宽 unit sandbox。

本机备份无法抵御整机或磁盘损坏。应把 `.dump` 与 `.dump.sha256` 成对复制到加密、
有访问控制的异机存储，复制后重新校验 SHA-256。只有实际恢复演练通过，才能证明
归档可恢复；成功的 `pg_dump` 日志或单独校验和都不够。

## 5. 使用 management coordinator 恢复生产数据库

恢复会清理归档所含的现有对象并重新写入目标数据库，是破坏性维护操作。先确认：

1. 已保存一份当前状态的最终备份及异机副本；
2. 选择的 `.dump` 与 `.sha256` 位于受管 backup root 的 `daily/` 或 `weekly/`；
3. 已核对归档时间、SHA-256、目标数据库名和应用 Release/迁移兼容性；
4. 当前 management host 可通过已安装 validator 的 `host` 检查；API 健康时还应通过
   根 `install.sh validate`，但 API 因升级失败而 `inactive`/`failed` 不会阻断受控恢复；
5. 没有安装、升级、备份或另一恢复任务正在运行。

待 v1.2.0 正式发布并安装 coordinator 后，生产恢复优先且只使用：

```bash
sudo /usr/local/lib/probe-panel/restore-management.sh \
  --confirm-database probe \
  /var/backups/probe-panel/postgres/daily/probe-YYYYMMDDTHHMMSSZ.dump
```

把 `probe` 和归档绝对路径替换为已人工核对的精确值。数据库确认值必须与安装配置及
数据库返回的 `current_database()` 完全一致；`postgres`、`template0`、`template1`
不能作为目标。命令参数中不包含密码。

coordinator 会：

1. 以 root 身份验证精确 ABI v2 平台、整组 lifecycle 哈希、已安装 management host，
   并要求 PostgreSQL 与 Nginx 稳定运行；
2. 获取部署锁，精确记录 API 与 backup timer 的 enablement 和活动状态；API 可为
   `active`、`inactive` 或 `failed`，异常过渡态和 DBus 读取错误会失败关闭；
3. 先 disable timer/API，等待正在执行的备份最多 60 秒，再停止 API；
4. 以无额外环境的 `probe-api` 用户加载已验证的离散 PG 参数，调用底层恢复器；
5. 校验受管根、文件归属与权限、归档名、配对 SHA、`pg_restore --list`、数据库确认
   和其他活动会话；
6. 在单个数据库事务中执行 clean/restore，随后用当前不可变 API 执行 forward migration；
7. 恢复原 enablement，启动 API 与 timer，并验证 management 服务、TLS、readiness
   和监听边界。

不要为了常规演练预先手工停止服务后直接执行低层脚本，也不要使用旧文档中的临时
unit 命令。coordinator 会观察已有状态并自己控制维护窗口；升级失败后 API 已经停止或
failed 是明确支持的恢复入口，不要求它先通过 readiness。

## 6. 恢复失败边界

- 若在 coordinator 委托低权限恢复器之前失败，它会恢复先前 API/timer enablement，
  并只重启原先处于 active 的服务；原先 inactive/failed 的服务继续保持停止；
- 一旦已经委托低权限恢复器，归档校验、数据库恢复或 post-restore migration 任一
  步骤失败都会让
  `probe-api.service` 和 `probe-postgres-backup.timer` 保持 stopped + disabled，防止
  重启后运行半恢复数据库；
- 发生后一种失败时，不要手工启动 API。保留原归档、校验文件和日志，核对当前数据库
  状态与 migration，再决定重试、使用另一归档或恢复匹配的应用 Release；
- forward migration 不会自动 down。只恢复数据库而保留不兼容的应用二进制不是
  有效回退。

成功返回后再次执行：

```bash
sudo bash /root/probe-panel-install-v1.2.0.sh validate
journalctl -u probe-api.service --since '-15 minutes' --no-pager
journalctl -u probe-postgres-backup.service --since '-15 minutes' --no-pager
```

## 7. 隔离恢复演练

至少每月在隔离 full-system VM 中演练，不能把生产数据库当作测试目标。推荐流程：

1. 使用与生产相同的不可变 management bundle、精确 OS/架构/ingress candidate 建立
   隔离安装，网络与生产入口完全分离；
2. 为隔离数据库配置独立凭据和 `.pgpass`，先让备份 service 初始化自己的受管根；
3. 从异机存储复制一对已验证 `.dump`/`.sha256` 到隔离主机受管 daily/weekly 目录，
   保留严格文件名并设置为 `probe-api:probe-api 0600`；
4. 使用隔离主机安装的 `restore-management.sh --confirm-database <隔离库名>` 恢复；
5. 验证 migration、管理员/节点/探测配置数量、关键历史时间范围、登录、readiness 和
   listener 边界，记录归档名、SHA、镜像、耗时与结果；
6. 演练完成后按隔离环境销毁流程清理，绝不把测试凭据或数据库接回生产。

该演练也是 60-cell formal-support gate 中 `backup_restore` 场景的一部分，但单次成功
不会自动把 candidate 提升为 supported。

## 8. 常见失败

- `backup root marker is missing or unsafe`：路径不是脚本初始化的受管根；不要伪造
  marker，应检查安装和目录归属；
- `another backup or restore operation is running` 或数据库维护锁冲突：等待当前任务，
  不要绕过锁；
- `backup checksum verification failed`：归档可能损坏，禁止恢复，改用另一份经验证
  归档并检查存储介质；
- `target database still has ... session(s)`：存在其他客户端；让 coordinator 保持维护
  状态，查明并结束非恢复会话后重试；
- `PGPASSFILE must not grant group or other permissions`：核对精确文件属主并设为 `0600`；
- 恢复后 migration 或 runtime validation 失败：保持 API/timer stopped + disabled，按
  coordinator 错误信息处理，不能为“恢复绿色”而强制启动。
