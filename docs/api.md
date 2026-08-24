# Probe Panel API 使用说明

`probe-api/api/openapi.yaml` 是 V1 HTTP 契约的唯一机器可读来源；本文只说明入口、认证方式和常用调用流程。实现、Nginx 与客户端若和本文出现差异，以 OpenAPI 契约及实际测试结果为准，并应立即修正文档。

## 入口与暴露边界

| 入口 | 允许的 API | 访问条件 |
|---|---|---|
| `https://panel.example.com` | 匿名只读 `/api/v1/panel/*` | 来源 IP/CIDR 在管理白名单中；没有账号、密码、Cookie 或 Session |
| `https://admin.example.com` | `/api/v1/auth/*`、`/api/v1/admin/*` 和管理页面需要的 `/api/v1/panel/*` | 来源白名单；管理写操作还需要管理员 Session、同源 `Origin` 和 CSRF Token |
| `https://api.example.com` | `/api/v1/agent/*`；默认关闭的 `/api/v1/public/*` | Agent 使用一次性注册令牌或设备 Bearer Token；Public API 当前固定关闭 |
| `http://127.0.0.1:8080` | `/internal/health/live`、`/internal/health/ready` | 仅服务器本机健康检查，不经公网入口暴露 |

三个 HTTPS Host 不能互换使用。未知 Host、未知 API、跨入口 API 和内部健康检查均由 Nginx 拒绝。Go API 还会对管理请求执行第二层来源校验，并且只允许监听 loopback 地址。

## 通用约定

- 请求和响应正文使用 UTF-8 JSON；没有正文的成功响应使用 `204 No Content`。
- 时间使用 RFC 3339，服务端生成时间统一为 UTC。
- 每个响应都带 `X-Request-ID`；排查问题时应同时记录该值、状态码和时间。
- 错误正文使用 `{"error":"<code>","message":"<safe message>","request_id":"<id>"}` 形态；客户端应判断 `error`，不要解析自然语言 `message`。
- 游客请求不得发送凭据。管理 Session Cookie 为 host-only、`Secure`、`HttpOnly`、`SameSite=Strict`。
- 管理写请求需要登录响应或 `/api/v1/auth/me` 返回的 CSRF Token，并通过 `X-CSRF-Token` 发送。
- 登录与 Agent API 都有限流。收到 `429` 时遵守 `Retry-After`，不要紧密重试。
- 游标分页使用 `limit` 和服务端返回的 `next_cursor`；游标是不可解释的临时令牌。
- 基础指标只能查询最近 5 分钟。探测趋势保留期限最多 90 天，支持 `raw`、`5m`、`1h` 和 `auto` 精度。

## 游客只读 API

游客前端只调用以下 GET 路由：

```text
/api/v1/panel/nodes
/api/v1/panel/nodes/{node_id}
/api/v1/panel/nodes/{node_id}/metrics
/api/v1/panel/nodes/{node_id}/disks
/api/v1/panel/nodes/{node_id}/probe-targets
/api/v1/panel/nodes/{node_id}/probes
```

示例：

```bash
curl --fail-with-body \
  'https://panel.example.com/api/v1/panel/nodes?limit=50'
```

该请求只在白名单来源成功，不需要也不接受游客登录。游客 Host 上的 `/login`、`/admin/*`、`/api/v1/auth/*` 和 `/api/v1/admin/*` 固定不可用。

## 管理员认证与 CSRF

打开登录页后，管理前端会先读取服务端判定的来源地址：

```bash
curl --fail-with-body \
  https://admin.example.com/api/v1/auth/access
```

成功响应固定包含 `allowed: true`，因为 Nginx 和 Go API 白名单都已经先行通过；`source_ip` 来自 socket 对端或仅受信反向代理可设置的内部头。客户端提交的 `Forwarded`、`X-Forwarded-For` 或 `X-Real-IP` 不参与判定。非白名单来源无法加载管理页面，也不会通过该接口取得地址回显。

登录只接受管理员账号：

```bash
curl --fail-with-body \
  --cookie-jar admin.cookies \
  --header 'Content-Type: application/json' \
  --header 'Origin: https://admin.example.com' \
  --data '{"username":"admin","password":"<password>"}' \
  https://admin.example.com/api/v1/auth/login
```

响应中的 CSRF Token 只保存在管理前端内存中，不应写入日志或持久存储。恢复已有 Session 时：

```bash
curl --fail-with-body \
  --cookie admin.cookies \
  https://admin.example.com/api/v1/auth/me
```

管理写请求同时发送 Cookie、精确管理 Origin 和 CSRF Token：

```bash
curl --fail-with-body \
  --cookie admin.cookies \
  --header 'Content-Type: application/json' \
  --header 'Origin: https://admin.example.com' \
  --header 'X-CSRF-Token: <csrf-token>' \
  --data '{"name":"server-01"}' \
  https://admin.example.com/api/v1/admin/nodes
```

管理员 API 包含：

```text
/api/v1/admin/nodes
/api/v1/admin/nodes/{node_id}
/api/v1/admin/nodes/{node_id}/enrollment-token
/api/v1/admin/nodes/{node_id}/rotate-token
/api/v1/admin/nodes/{node_id}/revoke-token
/api/v1/admin/probe-targets
/api/v1/admin/probe-targets/{target_id}
/api/v1/admin/users
/api/v1/admin/users/{user_id}
/api/v1/admin/audit-logs
/api/v1/admin/system/status
```

安装命令和轮换后的 Agent Token 只展示一次。管理面板使用默认 `900` 秒（15 分钟）有效期创建只能消费一次的注册令牌；创建注册令牌的 `no-store` 响应为兼容手动部署保留独立 `enrollment_token`，同时返回含同一令牌的 `install_command`。管理 UI 只使用命令，不持久化或记录任一明文。新命令会原子废止该节点此前所有未使用命令；注册成功会废止其余命令并吊销旧 Agent Token。短命令从 GitHub Raw 的不可变版本 Release 读取安装器，以 `-e` 传入 Agent HTTPS Origin、以 `-t` 传入一次性令牌；令牌不进入下载 URL，但可能短暂出现在进程参数、Shell 历史和剪贴板中，管理界面必须明确提示。旧的标准输入方式仍供手工部署兼容。

注册响应字段：

```json
{
  "node_id": "11111111-1111-4111-8111-111111111111",
  "enrollment_token": "<one-time-token for manual deployment compatibility>",
  "expires_at": "2026-08-24T08:15:00Z",
  "install_command": "<one-time GitHub Raw command containing the token once>"
}
```

命令使用严格 HTTPS 从显式配置的 `PROBE_AGENT_INSTALLER_URL` 下载安装器；默认使用 `Kcmose/my-agent/refs/tags/v1.0.1`，对应已核验的 GitHub `immutable=true` Release。部署器只接受当前源码明确允许的这个 Release，并为旧配置兼容完整 40 位小写提交；它拒绝其他未经核验的版本标签、`main`、`refs/heads/*` 和省略 `refs/tags/` 的歧义形式。安装器必须完整解析到最终入口后才产生安装副作用，再从 Agent 入口下载 `SHA256SUMS`、systemd 单元和当前架构二进制并逐项校验。私有 CA IP 模式只在命令中加入 64 位证书 SHA-256；安装器可在不携带秘密的固定 `ca.pem` 请求中暂时跳过常规 PKI 链验证，但必须先校验精确哈希，随后才允许下载清单、发送令牌或注册，并使用该证书严格验证所有连接。生成命令本身不含 `-k/--insecure`。`probe-api serve` 必须显式设置真实 `PROBE_AGENT_PUBLIC_URL`；禁用节点请求该端点返回 `409`。

`GET /api/v1/admin/system/status` 只供管理员读取，返回 API 与数据库的粗粒度就绪状态、V1 契约版本、UTC 检查时间，以及 API 进程能够强制或定义的管理白名单、管理员 Session、管理写请求 CSRF 和远程操作禁用边界。它不声称在运行时验证 Nginx Host/Agent 入口拓扑，也不返回 DSN、环境变量、主机信息、底层错误、凭据或服务控制入口；该 GET 不需要 CSRF Token，但仍要求有效管理员 Session 和来源白名单。

管理员 Session 本身由数据库认证，认证发生在状态处理器之前。如果数据库在认证阶段已经不可用，请求可能由认证层返回通用 `401` 或 `500`，而不是状态正文；`degraded` 只表示认证成功后紧接着执行的 readiness 检查失败。

## Agent API

Agent 只主动连接 API，不监听端口，也没有命令执行、文件管理、隧道或远程升级接口。

首次注册：

```http
POST /api/v1/agent/enroll
Content-Type: application/json

{
  "enrollment_token": "<one-time-token>",
  "hostname": "server-01",
  "agent_version": "1.0.0",
  "os": "linux",
  "arch": "amd64"
}
```

注册成功后，Agent 将返回的设备 Token 原子写入权限为 `0600` 的状态文件。随后使用：

```text
GET  /api/v1/agent/config?version=<current-version>
POST /api/v1/agent/report
Authorization: Bearer <agent-token>
```

上报支持 gzip，使用 `node_id + batch_id` 幂等去重，并以单调 `sequence` 防止乱序污染。API 只下发结构化采集与 TCP/HTTP(S) 探测配置；ICMP 当前按项目决定暂缓，不能创建，也没有 `CAP_NET_RAW`。

## 健康检查

服务器本机可执行：

```bash
curl --fail --silent http://127.0.0.1:8080/internal/health/live
curl --fail --silent http://127.0.0.1:8080/internal/health/ready
```

`live` 只表示进程可响应；`ready` 还要求数据库可用。监控不应通过任一外部 HTTPS Host 探测 `/internal/*`。

## 契约校验

在 Debian 构建环境执行：

```bash
npx --yes @redocly/cli lint probe-api/api/openapi.yaml
```

完整字段、请求体、响应体、错误码和分页 schema 请直接查看 [`probe-api/api/openapi.yaml`](../probe-api/api/openapi.yaml)。
