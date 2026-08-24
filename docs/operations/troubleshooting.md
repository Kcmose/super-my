# Probe Panel 故障排查

本文适用于 Debian 13 上的 Probe Panel V1。排查时始终区分三个入口：游客面板、管理面板和 Agent API。不要把管理员口令、Session Cookie、CSRF Token、Agent Token、数据库连接串或包含这些值的完整环境文件写入工单和测试报告。

## 1. 先确认故障边界

准备三个不含路径的入口地址：

```bash
export PANEL_URL='https://panel.example.invalid'
export ADMIN_URL='https://admin.example.invalid'
export AGENT_URL='https://api.example.invalid'
```

`.invalid` 只是占位符，执行前必须替换为部署的真实地址。然后从预期位于管理白名单内的测试机执行：

```bash
bash probe-api/deploy/scripts/security-smoke.sh \
  --panel-url "$PANEL_URL" \
  --admin-url "$ADMIN_URL" \
  --agent-url "$AGENT_URL"
```

预览环境应优先通过 `--cacert /absolute/path/to/ca.pem` 显式信任证书；确认 Agent Host 发布私有 CA 时再增加 `--expect-private-ca`，脚本会额外验证游客/管理 Host 为 `404`、Agent Host 为 `200`。只有临时排障且没有 CA 文件时才能使用 `--insecure`，正式环境不得使用。脚本不发送管理员凭据或有效 Agent Token，其输出可以直接作为第 6 阶段安全验收的原始证据。

三个入口的正常边界如下：

| 请求 | 游客入口 | 管理入口 | Agent 入口 |
|---|---:|---:|---:|
| 静态根 `/` | `200` | `200` | `404` |
| `/api/v1/panel/nodes?limit=1` | `200`，匿名 | `200`，只读 | `404` |
| `/api/v1/auth/me`（无 Session） | `404` | `401` | `404` |
| `/api/v1/admin/users`（无 Session） | `404` | `401` | `404` |
| `/api/v1/agent/config?version=0`（错误 Token） | `404` | `404` | `401` |

如果整个游客入口或管理入口返回 `403`，先查 IP/CIDR 白名单；如果跨入口路径意外返回 `200`、SPA HTML 或应用层 `401`，优先查 Nginx 是否装载了错误的 server 配置或静态根。

## 2. 服务和本机健康检查

在 API 所在的 Debian 主机上执行：

```bash
sudo systemctl is-active probe-api nginx postgresql
sudo systemctl status probe-api nginx postgresql --no-pager
curl --disable --silent --show-error \
  --write-out '\nHTTP %{http_code}\n' \
  http://127.0.0.1:8080/internal/health/live
curl --disable --silent --show-error \
  --write-out '\nHTTP %{http_code}\n' \
  http://127.0.0.1:8080/internal/health/ready
sudo nginx -t
sudo ss -ltnp
```

预期结果：

- `live` 返回 `200` 和 `{"status":"ok"}`。
- `ready` 返回 `200` 和 `{"status":"ready"}`；`503` 表示数据库不可用或迁移状态未就绪。
- API 只监听 `127.0.0.1:8080`（或明确配置的内部地址）。
- PostgreSQL 不监听公网地址的 `5432`。
- Nginx 对外监听部署所需的 `80/443`；阶段预览端口以预览配置为准。

查看最近日志时控制时间范围，避免无关信息和敏感数据扩散：

```bash
sudo journalctl -u probe-api --since '-15 minutes' --no-pager
sudo journalctl -u nginx --since '-15 minutes' --no-pager
sudo journalctl -u postgresql --since '-15 minutes' --no-pager
```

API 使用结构化日志。用 `request_id` 关联 Nginx 请求、API 错误和审计记录，不要通过打印完整请求头来排查认证问题。

## 3. DNS、代理和 TLS

### 地址无法连接

```bash
getent ahosts panel.example.invalid
getent ahosts admin.example.invalid
getent ahosts api.example.invalid
curl --disable --verbose --output /dev/null "$PANEL_URL/"
```

把占位域名替换为实际域名。检查 DNS 是否指向预期主机、主机防火墙是否只开放约定端口，以及 Nginx 是否绑定了正确的 IPv4/IPv6 地址。

curl 会读取标准代理环境变量。测试内网预览入口时，代理可能改变 Nginx 看到的来源地址并触发 `403`；为内网主机正确设置 `NO_PROXY`，不要在报告中抄录含认证信息的代理 URL。测试正式公网入口时则按部署网络策略决定是否使用代理。

### TLS 校验失败

正式环境检查证书链、域名和有效期：

```bash
openssl s_client -connect panel.example.invalid:443 \
  -servername panel.example.invalid -verify_return_error </dev/null
```

生产 Nginx 模板分别读取：

```text
/etc/probe-panel/tls/panel/fullchain.pem
/etc/probe-panel/tls/admin/fullchain.pem
/etc/probe-panel/tls/api/fullchain.pem
```

证书修复后先执行 `sudo nginx -t`，成功后再 reload。不要把 `--insecure` 当作生产修复方案。

## 4. IP/CIDR 白名单返回 403

生产环境的单一白名单源是：

```text
/etc/probe-panel/admin-allowlist.geo
```

每一行只能是经过确认的显式 IP/CIDR 和值 `1;`；空文件会有意拒绝所有游客及管理员浏览器请求。修改后的安全顺序是：

```bash
sudo /srv/probe/api/probe-api \
  config validate-admin-allowlist /etc/probe-panel/admin-allowlist.geo
sudo systemctl restart probe-api
curl --disable --silent --show-error --fail \
  http://127.0.0.1:8080/internal/health/ready
sudo nginx -t
sudo systemctl reload nginx
```

任一步失败都停止，不继续 reload。Nginx 和 API 必须读取同一份已验证文件。入口依据直接连接到 Nginx 的来源地址做判断，客户端提供的 `X-Forwarded-For` 等请求头会被清空，不能用这些头“修复”白名单。

还要确认测试流量没有经过未列入白名单的反向代理或出口 NAT。确实存在可信前置代理时，应先更新并重新评审整体信任边界，不能临时放宽为全网 CIDR。

## 5. 游客面板问题

### 页面能打开，但列表请求失败

直接请求匿名只读接口：

```bash
curl --disable --silent --show-error \
  --header 'Cookie:' \
  --write-out '\nHTTP %{http_code}\n' \
  "$PANEL_URL/api/v1/panel/nodes?limit=1"
```

- `200`：入口和 API 正常，继续检查浏览器控制台中的静态资源路径或响应结构。
- `403`：来源不在白名单，或 API 收到的可信来源地址与 Nginx 不一致。
- `404`：访问了错误域名、错误端口，或 Nginx 没有载入游客入口配置。
- `5xx`：结合 `request_id` 检查 API 和 PostgreSQL。

游客没有账号、口令、Cookie 或 Session。页面出现登录入口、访问 `/auth/me`，或匿名读取产生 `Set-Cookie`，都说明部署了错误的前端产物或出现了入口混用。生产静态根应严格分开：游客为 `/srv/probe/web`，管理端为 `/srv/probe/admin`。

### 页面刷新后落到错误内容

检查游客 server 的 SPA fallback 只使用 `/srv/probe/web`，并确认 `/api/`、`/internal/`、`/login` 和 `/admin` 在 fallback 之前显式拒绝。不要通过把未知 API 路径回退到 `index.html` 来掩盖 404。

## 6. 管理面板问题

管理入口常见状态码含义：

| 状态 | 含义 | 优先检查 |
|---:|---|---|
| `401` | 凭据错误，或 Session 缺失、过期、被撤销 | 登录状态、Session 生命周期、账户是否启用 |
| `403` | IP/CIDR、Origin 或 CSRF 校验失败 | 白名单、`PROBE_ADMIN_ORIGIN`、浏览器来源和 CSRF Token |
| `404` | 路由不属于该入口 | 域名、端口、Nginx server 配置 |
| `429` | 登录限流触发 | 停止重试，等待 `Retry-After`，再查失败原因 |

不带 Cookie 请求 `/api/v1/auth/me` 和 `/api/v1/admin/users` 应返回 `401`，这能证明管理命名空间已暴露但仍受认证保护。写操作还要求同源 `Origin` 和与当前 Session 绑定的 CSRF Token；不要用关闭这些校验的方式处理 `403`。

`PROBE_ADMIN_ORIGIN` 必须与浏览器实际使用的协议、主机和端口完全一致。修改配置后重启 API，再检查 readiness 和安全冒烟结果。

## 7. Agent API 问题

用一个明确无效且不属于部署的值检查认证边界：

```bash
INVALID_AGENT_TOKEN="diagnostic-invalid-$(date -u +%s)-$$"
curl --disable --silent --show-error \
  --header "Authorization: Bearer $INVALID_AGENT_TOKEN" \
  --write-out '\nHTTP %{http_code}\n' \
  "$AGENT_URL/api/v1/agent/config?version=0"
unset INVALID_AGENT_TOKEN
```

预期为 `401`。不要在诊断输出中使用或回显真实 Token。

| 状态 | 含义 |
|---:|---|
| `400` | 请求参数、JSON 或可信来源头无效 |
| `401` | 注册令牌或 Agent Token 缺失、错误、过期或被撤销 |
| `409` | 配置版本超前、批次键复用或序列过旧 |
| `413` | 压缩前/解压后请求体超过限制 |
| `422` | 上报结构有效，但字段或采样规则不满足协议 |
| `429` | 单 IP 或单节点速率限制触发 |

Agent host 上的 `/panel/*`、`/auth/*`、`/admin/*` 和根路径都应返回 `404`；只有 `/downloads/probe-agent/` 下五项固定发布资产和私有 CA 预览可选的公开 `ca.pem` 可下载。Agent 无需进入管理 IP 白名单；若错误 Token 得到 `403`，说明请求误入浏览器入口或代理规则不符合设计。

一键安装失败时先不要重新粘贴同一条命令。按错误阶段检查：

- `sudo` 或 `bash` 不存在：安装前补齐命令明确要求的基础工具，或进入 root Shell 后按文档手工下载固定 GitHub 安装器；不要把 URL 改成未固定的分支。
- 初始 GitHub Raw `curl` 失败：核对目标机 DNS、时间、系统 CA 和外网访问；不能手工加 `-k/--insecure`。网络恢复后重新执行仍未过期的命令，或重新签发。
- `private CA SHA256 verification failed`：Agent Host 的固定 `ca.pem` 与 API 启动时读取的证书不一致，必须修复发布文件并重新签发；禁止删除 `-c` 或跳过校验。
- `SHA256 verification failed` 或清单项不唯一：可能正好遇到发布切换或文件不一致；等待发布完成，在管理面板重新签发命令，不要跳过校验。
- 提示 `state.json`、二进制、环境文件、CA、systemd 单元已存在，或服务正在运行：目标机已有完整/部分 Agent 安装，首次安装器故意拒绝覆盖；按手动升级或恢复流程处理，不能删除状态文件来强装。
- 等待注册超时或服务失败：令牌会从环境文件清理且服务被停止/禁用；用 `journalctl -u probe-agent.service -n 50 --no-pager` 查看不含凭据的错误，再为该节点签发新命令。
- 安装成功但面板未上线：确认服务 active、环境文件中已无一次性令牌、状态文件为 `probe-agent:probe-agent 0600`，再检查 Agent API 网络和服务端审计；不要把状态文件内容贴到工单。

## 8. 数据库和迁移

readiness 为 `503` 时先检查：

```bash
sudo systemctl status postgresql --no-pager
sudo journalctl -u postgresql --since '-15 minutes' --no-pager
sudo journalctl -u probe-api --since '-15 minutes' --no-pager
```

部署流程必须在启动新 API 前执行 `probe-api migrate status` 和 `probe-api migrate up`。这两个子命令需要与服务相同的数据库环境，但不要在交互历史、进程参数或报告中展开 `PROBE_DATABASE_URL`。迁移失败时保留错误、迁移版本和数据库版本证据；不要直接手工改写 `schema_migrations`。

如果 API 可连接数据库但面板为空，分别确认是否已有节点、Agent 是否成功注册、最近上报是否被接受，以及服务器 UTC 时间是否正确。节点在线状态以服务端 `received_at` 为准。

## 9. 负载冒烟失败

只对游客只读 API 执行负载检查：

```bash
export LOAD_URL="$PANEL_URL/api/v1/panel/nodes?limit=50"
bash probe-api/deploy/scripts/load-smoke.sh \
  --url "$LOAD_URL" \
  --requests 100 \
  --concurrency 10 \
  --max-error-rate 1 \
  --max-p95-ms 1000
```

失败时按输出区分：

- curl failure：DNS、TLS、代理、连接或超时问题。
- unexpected status：先看状态计数；`403` 多为白名单，`429` 表示入口限流或测试参数过激，`5xx` 需查 API/数据库。
- P95 超阈值：同时采集 CPU、内存、连接数、PostgreSQL 活跃会话和慢查询证据，再用相同参数复测。
- invalid measurement：脚本运行环境或 curl 输出异常，该次结果不可作为验收证据。

该脚本是小规模回归冒烟，不等同于容量规划。不要用它压测写接口、登录接口或 Agent 上报接口。

## 10. 交付证据清单

在关闭故障或完成验收前，至少保存：

- UTC 时间、源码版本/提交标识、Debian、Nginx、Go API、PostgreSQL 版本。
- 三个入口地址（可以脱敏域名，不包含 URL 凭据）。
- `security-smoke.sh` 完整输出和退出码。
- `load-smoke.sh` 参数、完整输出和退出码。
- `nginx -t` 结果、API live/ready 结果。
- 故障时间窗内按 `request_id` 筛选的最小日志片段。
- 变更前后配置摘要，但不包含口令、Token、Cookie、CSRF 值或数据库连接串。

安全测试和负载测试分别回填到 `docs/reports/security-test.md` 与 `docs/reports/load-test.md`。没有实际执行证据时保持“待回填”，不得把模板当作已通过报告。
