# 第 6 阶段安全测试报告

> 状态：本文是 v1.1 双入口修订之前的历史内网预览验收记录；2026-08-24 当时已增量复验 GitHub Raw 短安装命令、不可变版本 Release 与私有 CA 下载边界。这些真实执行结果不等于 v1.1 `domain` 或 `ip` 生产模式已验收；v1.1 双模式仍待新的 Debian 13 实测证据回填。

## 1. 测试元数据

| 字段 | 实际值 |
|---|---|
| 执行时间（UTC） | 2026-08-23 09:57–10:14；最终部署复验 12:16；Agent 安装闭环复验 2026-08-24 01:09；短命令提交版复验 02:40–02:45；不可变 `v1.0.0` 版复验 03:14–03:19；截断流修正版 `v1.0.1` 复验 03:27–03:48 |
| 执行位置 | 用户本地 Debian 13 虚拟机与 Windows 宿主机 |
| 源码版本/提交标识 | Stage 6 基线 + Agent GitHub 不可变 Release `v1.0.1`，指向提交 `6a7e5baa1922191dc424c9bd5243e8b3e099d31e`；当前总源码目录不是 Git 仓库 |
| Debian / 内核 | Debian 13.5 / `6.12.90+deb13.1-amd64` |
| Nginx | 1.26.3 |
| probe-api | `installer-v1.0.1`（2026-08-24 增量部署） |
| PostgreSQL | 17.11 |
| 游客入口 | `https://192.168.33.253:18453` |
| 管理入口 | `https://192.168.33.253:18455` |
| Agent 入口 | `https://192.168.33.253:18454` |
| 白名单内来源 | 部署 VM 本机 `192.168.33.253` |
| 非白名单来源 | VM 回环别名 `127.0.0.2` |
| TLS | 预览自签名证书；通过 `--cacert` 明确信任，未使用 `--insecure` |

报告未记录管理员口令、Session Cookie、CSRF Token、有效 Agent Token 或数据库连接串。

## 2. 可复现命令

```bash
bash probe-api/deploy/scripts/security-smoke.sh \
  --panel-url https://192.168.33.253:18453 \
  --admin-url https://192.168.33.253:18455 \
  --agent-url https://192.168.33.253:18454 \
  --cacert /srv/probe-stage5-preview/tls/server.crt \
  --expect-private-ca
```

脚本只生成并发送明确无效、非秘密的 Agent Token，不读取真实 Token。补充白名单检查使用 `curl --interface 127.0.0.2`，并同时伪造 `X-Forwarded-For` 与 `X-Real-IP` 验证这些客户端头不会被信任。

## 3. 自动检查结果

| # | 检查 | 预期 | 实际 | 结果 |
|---:|---|---:|---:|---|
| 1 | 游客入口静态根 | `200` | `200` | 通过 |
| 2 | 游客无凭据读取 panel API | `200` | `200` | 通过 |
| 3 | 游客读取不创建 Session | 无 `Set-Cookie` | 无 `Set-Cookie` | 通过 |
| 4 | 游客入口访问 `/api/v1/auth/me` | `404` | `404` | 通过 |
| 5 | 游客入口访问 `/api/v1/admin/users` | `404` | `404` | 通过 |
| 6 | 游客入口访问 Agent 配置路径 | `404` | `404` | 通过 |
| 7 | 游客入口访问 Agent 下载路径 | `404` | `404` | 通过 |
| 8 | 管理入口静态根 | `200` | `200` | 通过 |
| 9 | 管理入口无凭据读取 panel API | `200` | `200` | 通过 |
| 10 | 管理入口无 Session 访问 `/api/v1/auth/me` | `401` | `401` | 通过 |
| 11 | 管理入口无 Session 访问 `/api/v1/admin/users` | `401` | `401` | 通过 |
| 12 | 管理入口访问 Agent 配置路径 | `404` | `404` | 通过 |
| 13 | 管理入口访问 Agent 下载路径 | `404` | `404` | 通过 |
| 14 | Agent 入口使用错误 Bearer Token | `401` | `401` | 通过 |
| 15 | Agent 入口访问 panel API | `404` | `404` | 通过 |
| 16 | Agent 入口访问 `/api/v1/auth/me` | `404` | `404` | 通过 |
| 17 | Agent 入口访问 `/api/v1/admin/users` | `404` | `404` | 通过 |
| 18 | Agent 入口下载经审查的 `install.sh` | `200` | `200` | 通过 |
| 19 | Agent 下载不创建 Session | 无 `Set-Cookie` | 无 `Set-Cookie` | 通过 |
| 20 | Agent 入口下载 `SHA256SUMS` | `200` | `200` | 通过 |
| 21 | Agent 入口访问未知下载文件 | `404` | `404` | 通过 |
| 22 | Agent 下载路径写请求 | `403` | `403` | 通过 |
| 23 | Agent 入口访问静态根 | `404` | `404` | 通过 |

摘要：基础模式复验退出码 `0`，`total=23 passed=23 failed=0`。私有 CA 模式另增加三个 `ca.pem` Host 隔离检查，最终结果见第 6 节。

## 4. 白名单与网络暴露补充检查

| 检查 | 预期 | 实际 | 结果 |
|---|---:|---:|---|
| 非白名单来源访问游客静态根 | `403` | `403` | 通过 |
| 非白名单来源访问管理静态根 | `403` | `403` | 通过 |
| 非白名单来源访问游客 panel API | `403` | `403` | 通过 |
| 非白名单来源访问管理 auth 与 admin API | 均为 `403` | `403` / `403` | 通过 |
| 非白名单来源以错误 Token 访问 Agent API | `401`，不是 `403` | `401` | 通过 |
| 伪造常见客户端来源头访问游客入口 | 仍为 `403` | `403` | 通过 |
| 宿主机连接预览 API `18090/tcp` | 不可达 | 不可达 | 通过 |
| 宿主机连接预览 PostgreSQL `55433/tcp` | 不可达 | 不可达 | 通过 |

宿主机同时确认三个预览入口 `18453/18454/18455` 可达。项目 API 只监听 `127.0.0.1:18090`；项目 PostgreSQL 使用 `/srv/probe-stage5-preview/pgsocket` Unix Socket，虽配置端口号 `55433`，但没有 TCP 监听。

虚拟机另有一套由 1Panel 管理、与本项目无关的 Docker PostgreSQL 将宿主 `5432` 绑定到 `0.0.0.0` 和 `[::]`，Windows 宿主可连接。它不包含本项目数据库，也不是本项目服务依赖；本次没有擅自调整未知业务。仍建议单独确认用途后限制其监听地址或防火墙白名单。生产验收必须在真实主机重新验证项目 API/数据库和同机其他服务的整体攻击面。

## 5. 安全响应头

| 响应头 | 游客入口 | 管理入口 | Agent 入口 |
|---|---|---|---|
| `Content-Security-Policy` | 存在 | 存在 | 存在 |
| `Permissions-Policy` | 存在 | 存在 | 存在 |
| `Cross-Origin-Opener-Policy: same-origin` | 存在 | 存在 | 存在 |
| `Cross-Origin-Resource-Policy: same-origin` | 存在 | 存在 | 存在 |
| `X-Content-Type-Options: nosniff` | 存在 | 存在 | 存在 |
| `X-Frame-Options: DENY` | 存在 | 存在 | 存在 |
| `Referrer-Policy: no-referrer` | 存在 | 存在 | 存在 |
| CORS 允许头 | 不存在 | 不存在 | 不存在 |
| `Strict-Transport-Security` | 未设置 | 未设置 | 未设置 |

当时的预览配置按设计不设置 HSTS；因此本结果不能写成当前生产 HSTS 已验收。v1.1 需分别复验域名模式的公信证书/HSTS/80/443 契约，以及 IP 模式的固定私有 CA、IP SAN、指纹和 18453/18454/18455 契约；本历史报告不宣称任一项已完成。

## 6. 历史 GitHub Raw 短命令与私有 CA 增量复验

| 检查 | 实际 | 结果 |
|---|---|---|
| Agent GitHub 不可变 Release | `v1.0.1`，`immutable=true`，指向 `6a7e5baa1922191dc424c9bd5243e8b3e099d31e` | 通过 |
| 匿名读取显式版本 Raw 安装器 | `refs/tags/v1.0.1` 返回 HTTP `200` | 通过 |
| GitHub Raw / 本地 / Agent Host 安装器 SHA-256 | 均为 `2504d3ecbaf3c162f6d0b707f7d053f4bdd4d2142ac415c0eb350e8dc1a1e143` | 通过 |
| 私有 CA 指纹及 Agent Host 下载 SHA-256 | 均为 `92de2937e46b2fe8e43c7ea4ba6598c99b650666fa5c9ebbcb62e27f6f12f891` | 通过 |
| `ca.pem` Host 路由 | 游客 `404`；管理 `404`；Agent `200` | 通过 |
| 当前预览短命令长度 | 366 个字符、单行、一个管道 | 通过 |
| 典型公信 TLS 短命令长度 | 291 个字符，无 `-c` | 通过 |
| 安装器完整解析屏障 | 函数体截断与缺少最终入口均未执行外部命令；契约测试通过 | 通过 |
| 安装器 URL 配置约束 | 只允许已核验 `refs/tags/v1.0.1` 或完整 40 位小写提交；拒绝其他标签、分支和歧义短路径 | 通过 |
| API / Agent / 预览 Nginx | 均为 active，`NRestarts=0` | 通过 |
| 真实 Agent 上报 | 配置 `200/304`、报告 `202`，面板存在 online 节点 | 通过 |
| 私有 CA 安全冒烟 | `total=26 passed=26 failed=0` | 通过 |

当时的命令已不再内嵌 CA Base64，而是只携带 64 位 SHA-256。安装器在完整解析文件末尾入口及指纹校验成功前不会产生安装副作用或发送一次性注册令牌。为保持 Komari 式单行体验，令牌通过 `-t` 参数传入，因此会短暂出现在进程参数，并可能进入 Shell 历史或剪贴板；管理面板已明确提示这一权衡。`v1.0.0` 作为历史不可变 Release 原样保留且没有移动标签，最终安全复核后发布的 `v1.0.1` 是当时允许项和默认版本。v1.1 将同样的指纹链路用于正式 IP 模式，但本段历史证据不代替 v1.1 的新验证。

## 7. 证据

| 文件/证据 | SHA-256 |
|---|---|
| `/srv/probe-stage5-preview/artifacts/stage6/security-smoke-20260823T095711Z.log` | `0150b58796043f01efaace0d88b8c7cae7d9f7a087d96dd581b748f7aa5ee849` |
| `/srv/probe-stage5-preview/artifacts/stage6/security-supplemental-20260823T101326Z.log` | `e196eb4edebac7b995e557240faba648dfc1c8c6a36c7b7aea6118dae12ae871` |
| `/srv/probe-stage5-preview/artifacts/stage6/security-headers-20260823T101326Z.log` | `65f576e1cc2437da0d887d60ecc03d9ac5513a5275af971f18216a61112c928e` |
| `/srv/probe-stage5-preview/artifacts/stage6/security-smoke-final-20260823T121652Z.log` | `82a22447459c0b43cd88097ba301f8a42aa77fea459a003b7d12c1306d9eba2e` |
| `/srv/probe-stage5-preview/artifacts/agent-bootstrap/security-smoke-20260824T010957Z.log` | `98686936a2fda1a0a6bbe88dd8a828df1141756323f6f93caf9b31d95c111c06` |
| `/srv/probe-stage5-preview/artifacts/short-command-4198996-20260824T024047Z/security-smoke.log` | `98686936a2fda1a0a6bbe88dd8a828df1141756323f6f93caf9b31d95c111c06` |
| `/srv/probe-stage5-preview/artifacts/short-command-4198996-20260824T024047Z/security-smoke-private-ca.log` | `39c5a723dc16df4875e74b04fb934a4ab4ad60a46182c35dd0cb35faf7c347ed` |
| `/srv/probe-stage5-preview/artifacts/installer-v1.0.0-20260824T031942Z/security-smoke-private-ca.log` | `39c5a723dc16df4875e74b04fb934a4ab4ad60a46182c35dd0cb35faf7c347ed` |
| `/srv/probe-stage5-preview/artifacts/installer-v1.0.1-20260824T034731Z/release-summary.json` | `ff5f9baf6d81b55e796027b4f890a1bfd5fec9aff58d2b30d2d87560ded35befe` |
| `/srv/probe-stage5-preview/artifacts/installer-v1.0.1-20260824T034731Z/installer-sha256.log` | `8702805e68e6bd381fbb18ec67d62d1907e052b04487fff3b88a53047fdc1408` |
| `/srv/probe-stage5-preview/artifacts/installer-v1.0.1-20260824T034731Z/security-smoke-private-ca.log` | `39c5a723dc16df4875e74b04fb934a4ab4ad60a46182c35dd0cb35faf7c347ed` |
| Windows 宿主机 TCP 检查 | `18453/18454/18455=true`，`18090/55433=false`；结果已固化在本报告 |

## 8. 结论与边界

| 字段 | 结果 |
|---|---|
| 自动检查 | 基础模式 23/23；私有 CA 模式 26/26 通过 |
| 补充检查 | 8/8 通过 |
| 失败项 | 无 |
| 最终结论 | 内网持久化预览安全验收通过；生产部署尚未验收 |

v1.1 生产验收必须二选一：域名模式使用三个真实域名、公信证书和 80/443；IP 模式使用同一规范 IP、固定私有 CA/IP SAN 证书和 18453/18454/18455。两种模式都要复验外部 `8080/5432` 不可达、PostgreSQL 只监听 loopback、当前模式防火墙、真实来源白名单及部署后的依赖版本；域名模式还需复验 HSTS 和 Certbot timer，IP 模式还需复验 CA 下载隔离和指纹。本报告不替代依赖漏洞扫描、代码审计、长时间攻击面监测或容量测试。

## 9. v1.1.0 免安装码、IP 模式与正式发布复验（2026-08-24）

本节记录修订后服务端 `v1.1.0` 和 Agent `v1.0.2` 的最终证据，取代前文“v1.1 仍待验证”的历史状态。所有构建和运行测试均在用户本地 Debian 13 虚拟机的隔离环境完成，未访问用户生产服务器。域名模式使用受控测试 CA 验证完整 ACME 流程、SAN、HSTS 和 Certbot 生命周期，不冒充真实公信 CA；真实域名解析和公信签发仍需生产上线时复验。

### 9.1 固定源码与不可变发布

| 项目 | 最终值 | 结果 |
|---|---|---|
| `super-my v1.1.0` annotated tag | tag object `1fa25f2d1477c3e7ce2666e053d8ab026bc26a77`；commit `55c9b026d41658b57d32cfb876968b6b12c2824a` | 通过 |
| `super-my v1.1.0` Release | `draft=false`、`prerelease=false`、`immutable=true`、恰好 3 项资产 | 通过 |
| Raw 服务端安装器 | `7ad41558d2c6a48ad2a87b087fa741799ccbe71176aee671f2c96c8f7b7e1280` | 通过 |
| amd64 Release | `13,377,223` bytes；`629a42d8328bf61316280fd7a858a8066ffcb82c6f8ff506b9a7cedc3c4644b4` | 通过 |
| arm64 Release | `12,659,742` bytes；`0e09b7190364f7c359f9ab3da24f92ef6953e797fabfe0e2e6fc380ecdfcd2ff` | 通过 |
| 外层 `SHA256SUMS` | `208` bytes；GitHub digest `2aba7791c30e1aa75d82fb052e3bcdf744a1f3920512230510676c6b6ea26e6a` | 通过 |
| GitHub 发布工作流 | run `32729160908`，commit `55c9b026…`，`completed/success` | 通过 |
| `my-agent v1.0.2` | commit `b72a68dbbd9fcf6fc83bdf8ea09ebd0b8f7d9a88`；Release `immutable=true` | 通过 |
| Raw Agent 安装器 | `163d45b4c1c23059fa73082fccf1b9fa8d08f8a678a2fa4385948272a0b28308` | 通过 |

公开发布后使用无授权 GitHub API、Raw 和 Release 下载地址重新获取全部对象。标签对象、剥离提交、资产名称/大小/API digest、外层清单和本地 Debian 构建产物逐项一致。两个 tar 包均拒绝绝对路径、`..`、重复成员、链接和特殊文件；安全解包后各自 75 项内层 `BUNDLE-SHA256SUMS` 全部通过，Manifest 精确固定 `super-my v1.1.0`、`my v1.0.0`、`my-agent v1.0.2`。

### 9.2 首次安装与入口模式

| 检查 | 实际 | 结果 |
|---|---|---|
| 全新公网安装 | Debian 13 无任何 Probe 文件；原样执行固定 Raw 命令，退出码 `0` | 通过 |
| 安装码 | 终端、安装页、日志和状态目录均没有安装码值或 code/token 文件 | 通过 |
| Setup 传输 | `/run/probe-panel-setup` `root:root 0700`；`setup.sock` `root:root 0600` | 通过 |
| Setup TCP 暴露 | TCP `18080` 无监听；浏览器只能经 root SSH 转发私有 UDS | 通过 |
| 空域名默认值 | 服务端返回规范 IP 的游客 `18453`、Agent `18454`、管理 `18455` HTTPS URL | 通过 |
| 部分域名 | 前端和 Setup API 均拒绝只填写一项或两项 | 通过 |
| IP 模式 | 私有 CA、精确 IP SAN、CA 下载 Host 隔离、Agent 指纹和三固定端口通过 | 通过 |
| 域名模式 | 受控测试 CA 下三 SAN、80/443、HSTS、重定向和 Certbot timer 通过 | 通过 |
| 正式入口关闭 Setup | Finalize 后 UDS/Setup/Finalizer 关闭，三个正式入口的安装与 Setup 路由失败关闭 | 通过 |

IP 模式完整干净安装在重启前后均通过安全冒烟 `31/31`；外部仅开放 `18453/18454/18455`，API `8080` 和 PostgreSQL `5432` 仅回环，备份 SHA 与 `pg_restore --list` 均通过。域名模式在受控 CA 环境中重启前后均通过 `28/28`；外部只开放 80/443，三个 HTTP Host 分别重定向到自身 HTTPS，HSTS 为 `max-age=31536000`，未知 Host 返回 `421`。浏览器经真实 UDS/SSH 转发确认安装页没有安装码文本或输入框，空域名可继续，深色/浅色均保持既有布局且控制台错误为 0。

### 9.3 迁移、Agent 与构建测试

| 检查 | 实际 | 结果 |
|---|---|---|
| v1.0.0 `pending` / `configuring` 迁移 | 使用最终 v1.1.0 canonical bundle 均成功；旧 code 哈希记录只在新 UDS ready 后删除 | 通过 |
| 非法迁移状态 | `finalizing`、`installed`、`recovery_required` 明确拒绝；故障注入按状态恢复或保持关闭 | 通过 |
| API | 最终 commit 20 个测试包通过；PostgreSQL 17 集成共 460 个 test/subtest，0 skip/0 fail；vet/race 通过 | 通过 |
| 前端 | 管理 `55/55`、游客 `37/37`；两端生产/全量 npm audit 均 0 漏洞；Vite 构建通过 | 通过 |
| Agent v1.0.2 | Bash 语法、ShellCheck、安装契约、Go 全包测试/vet和三种 Origin 形式通过 | 通过 |
| 面板生成命令 | 359 字符、Raw `v1.0.2`、`bash -s --`、无 `sudo`、无 `--insecure` | 通过 |
| 全新 Agent 安装 | 最小 Debian 13 明确无 `sudo`；root 原样执行命令成功，资产/CA SHA、注册、systemd 和重启通过 | 通过 |
| 一次性令牌 | 注册后复用返回 `409`；环境、磁盘和日志无 enrollment token | 通过 |
| Agent 暴露面 | `probe-agent` 低权限运行、active/enabled、`NRestarts=0`、监听端口数 0 | 通过 |

最终结论：用户指出的两个问题已经闭环。全新部署不再生成、显示、保存或要求安装码；三个域名全部留空就是正式支持的 IP+固定端口模式，不再强制域名。真实生产仍须按所选模式配置防火墙和来源白名单；选择域名模式时还须在真实 DNS 环境复验公信 ACME，选择 IP 模式时客户端须安全信任生成的私有 CA。
