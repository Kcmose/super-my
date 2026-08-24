# V1 路由权限矩阵与浏览器安全方案（冻结）

状态：阶段 5 实现稿（按匿名游客、管理员账户模型修订）

相关决策：游客前端 `probe-web` 固定部署在 `panel.example.com`，管理前端 `probe-admin` 固定部署在 `admin.example.com`，两者使用各自的同源 `/api` 反代和独立静态根；Agent API 通过 `api.example.com` 对 Agent 开放。

## 1. 判定顺序与术语

管理请求按下列顺序判定，任一失败即停止：

1. Nginx 校验 Host、TLS、请求大小和有效客户端 IP。
2. Nginx 对 `panel.example.com` 与 `admin.example.com` 执行同一严格 IP/CIDR 白名单；白名单为空即拒绝。
3. Go API 再次使用可信代理传入的有效客户端 IP 复核管理白名单。
4. `/panel/*` 只允许安全的匿名读取，不要求账号、密码、Cookie 或 Session。
5. `/auth/access` 只返回已经通过白名单的服务端来源地址，不要求 Session；`/auth/login` 只校验管理员账号；其他 `/auth/*` 与全部 `/admin/*` 校验 `probe_session` 和 `admin` 角色。
6. 管理员状态修改请求校验同源 Origin 和 `X-CSRF-Token`。
7. 执行业务校验、数据库约束与审计。

缩写：

- **IP**：管理 IP/CIDR 白名单在 Nginx 和 API 两层均通过。
- **S**：有效的管理员服务端 Session Cookie `probe_session`。
- **A**：已认证账户的角色必须为 `admin`（管理员）。
- **C**：同源 Origin 检查及 CSRF Token 检查均通过。
- **ET**：未使用且未过期的一次性 Enrollment Token。
- **AT**：有效、未吊销且与节点绑定的 Agent Token。
- **PK**：公共 API 专用 Key；不能使用 Session 或 Agent Token 代替。

“游客”是 `panel.example.com` 白名单内浏览器的匿名只读访问模式，不是用户角色：游客没有数据库用户、用户名、密码、Cookie 或 Session。只有管理员是账户，数据库/API 角色值仅为 `admin`。匿名不等于公网开放；游客仍必须先通过 Nginx 与 Go API 的管理 IP/CIDR 白名单，并且只能调用 `/panel/*` 的只读 GET 路由。管理前端确需展示同一只读数据时，`admin.example.com` 也可同源代理 `/panel/*`，但这不会让 `panel.example.com` 获得任何认证或管理路由。可选 `/public/*` 是另一套默认关闭、使用独立 API Key 的边界，不能与匿名游客混同。

HEAD 只在对应 GET 路由明确支持时与 GET 同权限，且不得产生业务状态变更。未列出的路由和方法一律 `404` 或 `405`，不能由通配处理器意外放行。

## 2. 主机级暴露矩阵

| 主机与路径 | Nginx IP 白名单 | API 身份 | 默认状态 | 说明 |
|---|---:|---|---|---|
| `panel.example.com/*` 静态资源 | 必须 | 无 | 开启 | 只读取独立 `probe-web` 构建产物；非白名单无法加载 |
| `panel.example.com/api/v1/panel/*` | 必须 | 无（匿名） | 开启 | 白名单内游客匿名只读；不是公网 API |
| `panel.example.com/api/v1/{auth,admin,agent,public}/*` | 不适用 | 不适用 | 不暴露 | 返回 `404`，游客 Host 无认证或管理入口 |
| `admin.example.com/*` 静态资源 | 必须 | 无 | 开启 | 只读取独立 `probe-admin` 构建产物；非白名单连登录页也不能加载 |
| `admin.example.com/api/v1/auth/*` | 必须 | 路由决定 | 开启 | 同源浏览器认证 |
| `admin.example.com/api/v1/panel/*` | 必须 | 无（只读） | 开启 | 仅供管理页面复用面板数据；不改变管理员授权边界 |
| `admin.example.com/api/v1/admin/*` | 必须 | S + A，写操作再加 C | 开启 | 管理修改与审计 |
| `admin.example.com/api/v1/{agent,public}/*` | 不适用 | 不适用 | 不暴露 | 返回 `404` |
| `api.example.com/api/v1/agent/*` | 不使用管理白名单 | ET 或 AT | 开启 | Agent 可来自任意地址，但必须认证 |
| `api.example.com/api/v1/public/*` | 不使用管理白名单 | PK | 关闭 | 仅可显式开启只读 GET/HEAD |
| `api.example.com/api/v1/{auth,panel,admin}/*` | 不适用 | 不适用 | 不暴露 | 返回 `404`，浏览器 API 无第二入口 |
| `/internal/*` | 仅回环/运维私网 | 内部凭据（若跨主机） | 不对公网 | 健康检查不经过公网 server block |

静态部署边界同样 fail closed：游客入口的根目录固定为 `/srv/probe/web`，管理入口固定为 `/srv/probe/admin`；入口可使用三域名或同一 IP 的固定端口。两个目录分别来自 `probe-web` 与 `probe-admin` 的独立构建/安装流程，不允许复制、软链接或 fallback 到另一套产物。`/api`、`/internal`、`/downloads`、跨入口 API 与隐藏文件由 Nginx 在 SPA fallback 前明确返回 `404`。Agent 入口另有只读的 `/srv/probe/agent` 发布链接，但只允许精确匹配五项发布资产，以及私有 CA IP 模式的固定 `ca.pem`，不能列目录或读取其他路径。

## 3. 完整 V1 路由权限矩阵

### 3.1 面板只读路由（panel 主机；admin 主机按管理页面需要复用）

| 方法 | 路由 | 条件 | CSRF | 角色与语义 |
|---|---|---|---:|---|
| GET | `/api/v1/panel/nodes` | IP | 否 | 游客匿名读取节点列表和当前状态 |
| GET | `/api/v1/panel/nodes/{node_id}` | IP | 否 | 游客匿名读取节点详情 |
| GET | `/api/v1/panel/nodes/{node_id}/metrics` | IP | 否 | 游客匿名读取最近 5 分钟基础指标 |
| GET | `/api/v1/panel/nodes/{node_id}/disks` | IP | 否 | 游客匿名读取当前磁盘及最近 5 分钟磁盘数据 |
| GET | `/api/v1/panel/nodes/{node_id}/probe-targets` | IP | 否 | 游客匿名读取该节点可展示的 TCP/HTTP(S) 探测目标 |
| GET | `/api/v1/panel/nodes/{node_id}/probes` | IP | 否 | 游客匿名读取目标探测状态与历史趋势 |

所有 `/panel/*` 都是白名单内匿名只读路由，不得因缺少或携带无效 Session 而要求游客登录。`panel.example.com` 只暴露本节路由；`admin.example.com` 可复用本节路由以渲染管理页面。若将来增加导出，必须仍限制为查询数据，不能用 GET 触发任务或修改状态。匿名游客不能在 panel Host 访问 `/auth/me`、`/auth/logout` 或任何 `/admin/*` 路由。

### 3.2 管理员认证与管理路由（仅 admin 主机）

| 方法 | 路由 | 条件 | 强制审计事件或语义 |
|---|---|---|---|
| GET | `/api/v1/auth/access` | IP | 返回服务端可信入口解析的当前来源 IP；成功固定 `allowed=true`，不接受客户端转发头作为来源 |
| POST | `/api/v1/auth/login` | IP + 同源 Origin；JSON | 仅管理员登录；成功返回 CSRF Token 并设置 Session |
| POST | `/api/v1/auth/logout` | IP + S + A + C | 管理员注销自己的 Session |
| GET | `/api/v1/auth/me` | IP + S + A | 返回当前管理员及当前 Session 稳定绑定的 CSRF Token；不轮换 Token |

| 方法 | 路由 | 条件 | 强制审计事件 |
|---|---|---|---|
| POST | `/api/v1/admin/nodes` | IP + S + A + C | `node.create` |
| PATCH | `/api/v1/admin/nodes/{node_id}` | IP + S + A + C | `node.update` |
| DELETE | `/api/v1/admin/nodes/{node_id}` | IP + S + A + C | `node.delete`、立即吊销 Agent Token |
| POST | `/api/v1/admin/nodes/{node_id}/enrollment-token` | IP + S + A + C | `enrollment_token.create`；明文令牌与含该令牌的 `install_command` 只返回一次且 `no-store`；新令牌废止旧安装命令 |
| POST | `/api/v1/admin/nodes/{node_id}/rotate-token` | IP + S + A + C | `agent_token.rotate` |
| POST | `/api/v1/admin/nodes/{node_id}/revoke-token` | IP + S + A + C | `agent_token.revoke` |
| GET | `/api/v1/admin/probe-targets` | IP + S + A | 无状态变更；读取可记访问日志 |
| POST | `/api/v1/admin/probe-targets` | IP + S + A + C | `probe_target.create` |
| PATCH | `/api/v1/admin/probe-targets/{target_id}` | IP + S + A + C | `probe_target.update`，含保留期前后值 |
| DELETE | `/api/v1/admin/probe-targets/{target_id}` | IP + S + A + C | `probe_target.delete` |
| GET | `/api/v1/admin/users` | IP + S + A | 仅列出管理员账户；不得返回密码哈希 |
| POST | `/api/v1/admin/users` | IP + S + A + C | `user.create`；只允许创建管理员 |
| PATCH | `/api/v1/admin/users/{user_id}` | IP + S + A + C | `user.update`；角色只能保持 `admin`，摘要包含启用状态及密码是否变更 |
| DELETE | `/api/v1/admin/users/{user_id}` | IP + S + A + C | `user.delete`；撤销该管理员的 Session |
| GET | `/api/v1/admin/audit-logs` | IP + S + A | 只读、分页 |
| GET | `/api/v1/admin/system/status` | IP + S + A | 只返回 API/数据库粗粒度就绪状态、UTC 检查时间和 API 进程内可强制的安全契约；不把 Nginx 拓扑写成已验证事实，不返回错误详情、环境、凭据或控制能力 |

游客没有 Session 或角色，对上表所有 `/admin/*` 路由均返回 `401`；伪造 Cookie 或直接构造请求不能获得管理员权限。不能删除或停用最后一个可登录的管理员，也不能让管理员通过一次请求删除或停用自己而使系统失去管理员；这些是 API 业务约束。

### 3.3 Agent 路由（仅 api 主机）

| 方法 | 路由 | 条件 | IP 白名单 | 语义 |
|---|---|---|---:|---|
| POST | `/api/v1/agent/enroll` | ET + 独立限流 | 否 | 原子消费一次性令牌；设备 Token 仅返回一次 |
| GET | `/api/v1/agent/config?version={n}` | AT | 否 | 配置相同返回 304，否则返回完整结构化配置 |
| POST | `/api/v1/agent/report` | AT + 请求大小/批次限制 | 否 | 批量上报；`node_id + batch_id` 幂等 |

Agent 不提交可信 `node_id`；API 从 Token 绑定关系取得节点身份。Agent Token 不能调用管理、面板、公共或其他节点的路由。Agent 路由只在 `api.example.com` 暴露。

### 3.4 Agent 首次安装静态文件（仅 api 主机）

| 方法 | 路由 | 条件 | 内容 |
|---|---|---|---|
| GET/HEAD | `/downloads/probe-agent/install.sh` | 精确路径 | 公开首次安装器 |
| GET/HEAD | `/downloads/probe-agent/ca.pem` | 精确路径 | 仅私有 CA IP 模式发布的公开证书；三域名模式不存在 |
| GET/HEAD | `/downloads/probe-agent/probe-agent.service` | 精确路径 | 公开 systemd 单元 |
| GET/HEAD | `/downloads/probe-agent/SHA256SUMS` | 精确路径 | 当前原子发布的校验清单 |
| GET/HEAD | `/downloads/probe-agent/linux-amd64/probe-agent` | 精确路径 | Linux amd64 Agent |
| GET/HEAD | `/downloads/probe-agent/linux-arm64/probe-agent` | 精确路径 | Linux arm64 Agent |

这些文件不需要 Agent Token，因为它们不含节点身份或秘密；令牌只在管理员接口响应中出现，绝不能进入下载 URL。主命令从 GitHub Raw 当前明确允许的 `refs/tags/v1.0.1` 不可变 Release 取得安装器，再用 `-e/-t` 调用；安装器完整解析前不得产生副作用，短参数令牌可能短暂出现在进程参数和 Shell 历史，界面必须警告。私有 CA 模式只对固定 `ca.pem` 做一次无秘密下载并校验命令内的精确 SHA-256，匹配前不得发送令牌。Nginx 禁止其他文件名、目录列表和写方法，panel/admin Host 对全部 `/downloads/*` 返回 `404`。发布器同时原子切换目录和清单，切换竞争最多导致校验失败并要求重新签发，不能降级放行。

### 3.5 可选公共只读路由（仅 api 主机）

公共 API 默认整体关闭并返回 `404`。启用开关不是网页可写配置；启用后只允许下列只读路由，并全部要求 PK 与独立限流：

| 方法 | 路由 | 可返回内容 |
|---|---|---|
| GET/HEAD | `/api/v1/public/nodes` | 管理员明确标记为公开的节点摘要 |
| GET/HEAD | `/api/v1/public/nodes/{node_id}` | 单个公开节点的非敏感摘要 |
| GET/HEAD | `/api/v1/public/nodes/{node_id}/metrics` | 公开节点最近 5 分钟的脱敏指标 |
| GET/HEAD | `/api/v1/public/nodes/{node_id}/probes` | 公开目标的只读趋势 |

公共响应不得包含内部地址、Agent Token、注册令牌、用户名、审计日志或非公开目标。公共路由永远不接受 POST、PUT、PATCH 或 DELETE。若 V1 未实现上述显式开关和字段级脱敏，则保持全部关闭。

### 3.6 内部路由

| 方法 | 路由 | 条件 | 内容 |
|---|---|---|---|
| GET | `/internal/health/live` | 仅回环/受信运维网 | 进程存活，不探测外部依赖 |
| GET | `/internal/health/ready` | 仅回环/受信运维网 | 就绪状态，可检查数据库连通和迁移版本 |

内部响应不得泄露密钥、DSN、完整配置、堆栈或环境变量。若经网络访问，必须使用独立内部凭据；公网 Nginx server block 不配置这些 location。

## 4. 白名单与真实客户端 IP

### 4.1 白名单配置约束

- 同时支持单个 IPv4/IPv6 与 CIDR，单 IP 在加载时规范化为 `/32` 或 `/128`。
- 空列表语义固定为拒绝所有管理访问。
- 配置加载时拒绝 `0.0.0.0/0` 和 `::/0`，拒绝非法 CIDR、带端口的地址和无法解析的主机名；白名单只接受 IP/CIDR，不做运行时 DNS 解析。
- IPv4-mapped IPv6 先规范化再比较，避免同一地址以不同文本形式绕过规则。
- Nginx 配置必须先 `nginx -t`，校验失败不得 reload；管理白名单不允许通过 Web/API 修改。
- `panel.example.com`、`admin.example.com` 和 Go API 必须从同一部署源生成白名单，避免三处人工配置漂移。各层取交集，任何一层拒绝都不能降级放行。

V1 的部署源固定为 `/etc/probe-panel/admin-allowlist.geo`。文件只允许注释、空行和下列形式，不接受 Nginx 其他指令：

```text
192.0.2.10/32 1;
2001:db8:1234::/48 1;
```

API 通过 `PROBE_ADMIN_ALLOWLIST_FILE` 读取该文件；变量为空等同空白名单，所有管理请求在 Go 层拒绝。每次修改后必须依次执行：

```text
probe-api config validate-admin-allowlist /etc/probe-panel/admin-allowlist.geo
systemctl restart probe-api
nginx -t
systemctl reload nginx
```

验证、API 重启或 `nginx -t` 任一步失败都不得 reload。API 在启动时加载并冻结内存中的白名单，因此只 reload Nginx 不足以新增管理来源；删除来源时任一层先拒绝都保持安全闭合。

### 4.2 无上游代理模式

Nginx 直接接收客户端连接时，有效客户端 IP 只能取 socket 对端地址 `$remote_addr`。进入 Nginx 的 `X-Forwarded-For`、`X-Real-IP`、`Forwarded`、`CF-Connecting-IP` 全部视为不可信，不参与白名单。

代理给 Go API 时覆盖而不是追加：

```nginx
proxy_set_header X-Probe-Client-IP $remote_addr;
proxy_set_header X-Forwarded-For "";
proxy_set_header X-Real-IP "";
proxy_set_header Forwarded "";
proxy_set_header CF-Connecting-IP "";
proxy_set_header True-Client-IP "";
proxy_set_header X-Client-IP "";
```

Go API 仅当 socket 对端属于 `trusted_proxy_cidrs`（通常为 `127.0.0.1/32`、`::1/128` 或明确容器网段）时才读取 `X-Probe-Client-IP`。否则使用自己的 socket 对端地址，并拒绝带有该内部头的外部请求。

### 4.3 有明确上游代理模式

只有在确实部署负载均衡器/CDN 时才配置 `set_real_ip_from`，且必须逐项列出该代理的固定 CIDR，并使用该代理官方保证覆盖的单一真实 IP 头。禁止信任任意来源，禁止用客户端可追加的链中“第一个地址”自行猜测真实 IP。

```text
连接对端不在 trusted_proxy_cidrs → 忽略所有转发头
连接对端在 trusted_proxy_cidrs   → 按已配置的唯一头解析并规范化
解析缺失或非法                   → 管理请求默认拒绝
```

必须用伪造各类转发头的请求验证：未受信来源的判定结果始终等于 socket 对端地址。

## 5. 管理员 Session 与 Cookie

- 游客绝不创建 Session。以下 Cookie 规则仅适用于管理员。
- Cookie 名固定为 `probe_session`，值至少含 32 字节密码学随机熵；数据库只保存 Token 哈希。
- 属性固定包含 `HttpOnly; Secure; SameSite=Strict; Path=/`。不设置 `Domain`，从而形成 `admin.example.com` 专用 host-only Cookie；浏览器不会向 `panel.example.com` 发送该 Cookie。
- V1 默认使用 12 小时绝对有效期，每个管理员最多 5 个未撤销且未过期的 Session；部署配置只允许把有效期设置在 5 分钟至 7 天、并发数设置在 1 至 20 之间。创建新 Session 时在同一事务撤销该管理员最旧的超额 Session。
- 管理员登录后旋转 Session，注销、管理员删除、密码或启用状态等敏感变更后撤销相关 Session。
- API 返回认证数据时使用 `Cache-Control: no-store`；Session 不得出现在 URL、日志或前端持久存储中。
- Session 到期和并发数量由 API 强制执行；前端路由守卫仅改善体验，不构成授权边界。

### 5.1 初始管理员自举

- 系统不内置默认账号或默认密码，游客也没有账号。空数据库完成迁移后，只能在 API 主机的交互式终端执行 `probe-api user bootstrap-admin <username>` 创建首个管理员。
- 密码必须由终端无回显读取并二次确认，不允许放在命令行参数、环境变量、文件或管道中；新密码至少 12 个 UTF-8 字节，使用冻结的 Argon2id 参数散列后才写入数据库。
- 自举事务使用数据库互斥锁，并且只在 `users` 表完全为空时成功；并发调用至多一个成功。数据库已有管理员后，该入口永久失败闭合，后续管理员管理只能走受权限与审计保护的管理接口。
- 成功自举写入 `local-bootstrap` 审计事件，但日志、审计和命令输出均不得包含密码或密码散列。

## 6. CSRF 与 Origin

同源部署不需要 CORS，但 Cookie 认证的状态修改仍必须防 CSRF：

1. `POST /auth/login` 仅用于管理员登录。它没有预先 Session，因此不要求 CSRF Token；它必须是 `Content-Type: application/json`，且 `Origin` 必须精确等于 `https://admin.example.com`。若浏览器提供 `Sec-Fetch-Site`，只接受 `same-origin` 或 `none`。
2. 登录成功响应和 `GET /auth/me` 返回使用域分隔 HMAC-SHA256 从高熵 Session Token 稳定派生的 32 字节 `csrf_token`；数据库只保存其哈希，前端只保存在内存，不写入 localStorage、URL 或 Cookie。同一 Session 的多个标签页得到相同 Token；`GET /auth/me` 重新校验 Session 哈希和已存 CSRF 哈希后返回该值，不轮换也不改写哈希。浏览器提供 `Sec-Fetch-Site` 时只接受 `same-origin` 或 `none`，仍拒绝同站不同源页面。
3. 除安全的 GET/HEAD 外，所有 Cookie 认证请求必须携带 `X-CSRF-Token`。API 对其进行恒定时间比较，并再次精确校验 `Origin`。
4. Origin 缺失时，浏览器管理写请求默认拒绝。仅受控的非浏览器运维调用可使用另一种显式认证机制；V1 不提供此例外。
5. CSRF Token 与 Session 生命周期一致：新登录或 Session 轮换会得到新 Token；权限变化撤销 Session 后旧 Token 随之失效；重复调用 `GET /auth/me` 不轮换 Token，注销后立即失效。

CSRF 失败统一返回 `403`，不泄露是 Origin、Session 还是 Token 的哪一项不匹配。

## 7. CORS 冻结方案

- `panel.example.com` 与 `admin.example.com` 都只调用各自同源暴露的路由，不返回 `Access-Control-Allow-Origin`，也不允许两套前端跨域调用对方 Host。管理员 Origin/CSRF 精确值仍固定为 `https://admin.example.com`。
- `api.example.com` 的 Agent 调用不是浏览器调用，不启用 CORS，也不允许凭据跨域。
- 公共 API 即使开启，默认也不返回 CORS 响应头；独立 API Key 不应放入公开浏览器代码。未来若确需浏览器公共访问，只能新增精确的只读 Origin allowlist，禁止 `*` 与 credentials 组合，并形成新的冻结决策。
- 对意外的跨域 OPTIONS 预检不做通配放行；未配置路由返回 `404/405`。

CORS 不是认证、IP 白名单或 CSRF 的替代品。

## 8. 限流

- Nginx 以直接 socket 来源 IP 为 key，分别限制登录、Agent 注册和 Agent 运行时请求；禁止改用任何转发头或明文 Token 作为 key。Nginx 模板值是入口硬上限，Go 环境变量不能把外层额度调高；调整容量时必须同时审查两层。
- 登录在 Go 层再使用 PostgreSQL 原子固定窗口，按规范化来源 IP 与完整用户名两个维度计数，并在密码散列校验前执行。默认 IP 为每分钟 10 次、用户名为每 5 分钟 5 次；成功登录只清除该用户名窗口，不清除来源 IP 窗口。
- Agent 在 Go 层对注册先按来源 IP 限制；配置与上报分别先按来源 IP粗限制，认证成功后再按 Token 绑定的节点 ID 独立限制，避免同一 NAT 下的节点互相占用节点额度。
- Go 的 Agent 限流每个独立桶都受 `PROBE_RATE_LIMIT_MAX_KEYS_PER_BUCKET` 硬上限约束；满容量且没有过期项时对未知 key 直接返回 429，不淘汰仍在窗口内的记录，避免随机来源重置已有配额。登录窗口保存在数据库，过期记录随 Session 清理任务删除。
- 所有 Go 限流响应固定为 `429`、统一 JSON 错误 `rate_limited`，并返回整数秒 `Retry-After`；Nginx 自己拒绝的 `429` 也返回同一基本 JSON 结构与 `Retry-After`。

## 9. 返回码与拒绝语义

| 场景 | 状态码 |
|---|---:|
| Nginx 管理白名单拒绝 | 403 |
| 缺少、无效或过期的管理员/Agent/API Key 凭据 | 401 |
| 已认证但角色不足、CSRF/Origin 失败 | 403 |
| 主机上不应暴露的路由或关闭的公共 API | 404 |
| 方法不允许 | 405 |
| JSON、字段或业务校验失败 | 400 或 422（按 OpenAPI 定义） |
| 冲突或已失效的一次性资源 | 409 |
| 请求体或批次超过限制 | 413 |
| 限流 | 429 |

API 错误体使用统一字段 `error`、`message`、`request_id`、`details`；日志和响应均不得回显明文 Token、密码、Session 或完整敏感请求体。

## 10. 必测用例

- 空白名单、非法 CIDR、IPv4、IPv6、IPv4-mapped IPv6 和禁止的全网 CIDR。
- 从非白名单来源请求 HTML、JS、CSS、auth、panel、admin 均被拒绝。
- 非白名单 Agent 带有效 AT 可上报；无效 AT 返回 401。
- 在三个 Host 间交叉请求必须 fail closed：panel Host 的 auth/admin/agent/public、admin Host 的 agent/public、api Host 的 auth/panel/admin 均返回 404。
- `panel.example.com` 与 `admin.example.com` 分别只能读取 `probe-web` 与 `probe-admin` 的独立静态根；使用同名哨兵文件验证不能跨根读取。
- `panel.example.com/login`、`panel.example.com/admin/*` 以及 `admin.example.com/overview`、`admin.example.com/nodes/*`、`admin.example.com/probes` 均必须在 SPA fallback 前返回 `404`。
- 未知 API 路径和 `/internal/*` 在三个公网 Host 上均返回 404；SPA fallback 不得吞掉 `/api` 或 `/internal` 路径。
- 伪造 `X-Forwarded-For`、`X-Real-IP`、`Forwarded`、`CF-Connecting-IP`、`X-Probe-Client-IP` 均不能绕过。
- 白名单内游客不携带 Cookie 即可读取全部冻结的 `/panel/*` GET 路由。
- 游客没有账号、密码或 Session；匿名请求任何 `/admin/*` 路由均返回 401。
- 非白名单来源即使只请求匿名面板路由仍返回 403，匿名不能绕过 IP/CIDR 白名单。
- 缺失/错误 CSRF、错误 Origin、跨域预检、非 JSON 登录均失败。
- 公共 API 默认关闭；开启后所有写方法仍失败，且非公开字段不出现在响应中。
