# 第 6 阶段安全测试报告

> 状态：内网持久化预览验收通过；2026-08-24 已增量复验 GitHub Raw 短安装命令、不可变版本 Release 与私有 CA 下载边界，生产部署尚未验收。本文记录真实执行结果，不把预览端口或自签名证书等同于生产边界。

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

预览配置按设计不设置 HSTS；因此本结果不能写成生产 HSTS 已验收。生产模板包含 HSTS，仍须在正式域名与 CA 证书上线后实测。

## 6. GitHub Raw 短命令与私有 CA 增量复验

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

命令不再内嵌 CA Base64；私有 CA 预览只携带 64 位 SHA-256。安装器在完整解析文件末尾入口及指纹校验成功前不会产生安装副作用或发送一次性注册令牌。为保持 Komari 式单行体验，令牌通过 `-t` 参数传入，因此会短暂出现在进程参数，并可能进入 Shell 历史或剪贴板；管理面板已明确提示这一权衡。`v1.0.0` 作为历史不可变 Release 原样保留且没有移动标签，最终安全复核后发布的 `v1.0.1` 才是当前允许项和默认版本。

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

生产验收仍必须使用真实三个域名、正式 CA 证书和生产端口，复验 HSTS、外部 `8080/5432` 不可达、防火墙、真实来源白名单及部署后的依赖版本。本报告不替代依赖漏洞扫描、代码审计、长时间攻击面监测或容量测试。
