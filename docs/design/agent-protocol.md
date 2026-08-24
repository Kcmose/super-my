# V1 Agent 协议（冻结）

状态：阶段 0 冻结稿

基础 URL：`https://api.example.com/api/v1/agent`

传输：TLS + JSON UTF-8；仅 report 请求允许 `Content-Encoding: gzip`

## 1. 通用约定

- Agent 只主动发起 HTTPS 请求，不监听端口；服务端不得反连 Agent。
- 除 enroll 外，使用 `Authorization: Bearer <agent-token>`。Token 至少含 32 字节密码学随机熵，数据库只保存哈希，并与唯一节点绑定。
- 节点身份只由已认证 Token 的绑定关系确定。Agent 请求中不接受可信 `node_id`；出现未知字段时严格拒绝，不能让客户端覆盖身份。
- ID 使用规范的小写 UUID 字符串。时间使用带时区的 RFC 3339，发送时统一 UTC `Z`；实现应接受合法小数秒。所有最终写入 PostgreSQL `BIGINT` 的非负整数（序列、字节计数和延迟微秒值）上限统一为 `9,223,372,036,854,775,807`，越界由 Agent 本地校验和 API 以 `422` 拒绝。
- JSON 字段名和枚举值区分大小写。请求采用严格 schema；未知字段、重复 JSON key、NaN/Infinity、越界数值或尾随 JSON 均返回校验错误。
- 默认请求超时 15 秒；配置轮询在周期后增加 0%..10% 随机抖动，常规上报在周期的 80%..90% 窗口触发，为内存队列留下首次请求余量；失败时指数退避，最大间隔 60 秒。
- Agent 离线缓存只存在内存，按 `sampled_at` 保留最近最多 5 分钟，并以本地单调入队时钟强制 300 秒内存寿命；超过任一上限丢弃最旧数据，不写本地 SQLite。`effective_at` 只能由 API 收到批次后计算，Agent 不预判该值。
- API 最大 Agent 请求体 256 KiB（按解压后的正文计），gzip 同时限制压缩与解压大小，防止压缩炸弹。
- 完整 Agent 配置响应使用独立的 2 MiB 硬上限；该上限只用于有界地容纳已冻结的字段最坏序列化体积，不放宽 report 的 256 KiB 请求上限，也不允许自由载荷。

统一错误响应：

```json
{
  "error": "invalid_request",
  "message": "request validation failed",
  "request_id": "01K...",
  "details": {
    "field": "metrics[0].cpu_percent"
  }
}
```

错误响应不得回显 Enrollment Token、Agent Token 或完整上报正文。

## 2. 注册 `POST /enroll`

### 2.1 请求

该路由不使用 Agent Token；`enrollment_token` 是管理员生成的一次性、短期注册凭据。

```json
{
  "enrollment_token": "opaque-one-time-token",
  "hostname": "server-01",
  "agent_version": "1.0.0",
  "os": "linux",
  "arch": "amd64"
}
```

| 字段 | 类型 | 必填 | 约束与含义 |
|---|---|---:|---|
| `enrollment_token` | string | 是 | 不透明敏感值；不得写入日志 |
| `hostname` | string | 是 | 1..253 字符；去除首尾空白后非空，仅作节点元数据，不作为身份 |
| `agent_version` | string | 是 | 1..64 字符的构建版本标识 |
| `os` | string | 是 | V1 固定为 `linux` |
| `arch` | string | 是 | V1 为 `amd64` 或 `arm64` |

不得提交 `node_id`、角色、权限、命令、回连地址或自定义配置。

### 2.2 成功响应

成功返回 `201 Created`：

```json
{
  "node_id": "0191f6d0-35c8-7f31-a165-3f418377e8d8",
  "agent_token": "opaque-device-token-shown-once",
  "config_version": 1
}
```

| 字段 | 语义 |
|---|---|
| `node_id` | 服务端创建或注册令牌预绑定的节点 UUID |
| `agent_token` | 设备凭据，只在本响应展示一次；服务端不保存明文 |
| `config_version` | 当前完整配置版本，正整数且单调递增 |

注册令牌的检查、节点锁定、消费、兄弟令牌失效、旧 Agent Token 吊销和新 Agent Token 哈希写入必须在一个数据库事务中完成，锁顺序固定为节点后令牌。签发新注册令牌时也先锁节点并废止同节点此前所有未使用令牌。事务提交后当前及兄弟注册令牌立即失效；重复或并发使用返回 `409 enrollment_token_used`，无效/过期令牌返回 `401`。enroll 本身不是可安全重放的幂等接口，Agent 必须在开始常规请求前以权限 `0600` 原子保存成功响应中的 Token；若响应丢失，应由管理员吊销未确认凭据并生成新注册令牌，API 不得为了重试保存或再次展示明文设备 Token。

## 3. 拉取配置 `GET /config?version={n}`

请求示例：

```http
GET /api/v1/agent/config?version=12 HTTP/1.1
Authorization: Bearer <agent-token>
Accept: application/json
```

`version` 必填，为 Agent 当前已成功应用的非负整数；首次拉取传 `0`。

- `version == current`：返回 `304 Not Modified`，无响应体。
- `version < current`：返回 `200 OK` 和完整配置，绝不返回部分补丁。
- `version > current`：返回 `409 config_version_ahead`；Agent 保留最后已验证配置并在下一刷新周期重试。

200 响应：

```json
{
  "config_version": 13,
  "issued_at": "2026-08-20T09:30:00Z",
  "metrics": {
    "collect_interval_seconds": 5,
    "report_interval_seconds": 10,
    "mountpoints": ["/", "/data"],
    "include_virtual_interfaces": false
  },
  "agent": {
    "config_refresh_interval_seconds": 60,
    "max_memory_queue_seconds": 300
  },
  "limits": {
    "max_batch_samples": 120
  },
  "probe_targets": [
    {
      "id": "0191f70a-a340-75b4-8d44-0d79b06e206a",
      "name": "Homepage TLS",
      "type": "https",
      "host": "example.com",
      "port": 443,
      "path": "/health",
      "interval_seconds": 30,
      "timeout_seconds": 3,
      "retention_seconds": 604800,
      "enabled": true,
      "config_version": 4
    }
  ]
}
```

### 3.1 配置字段

| 字段 | 类型 | 约束 |
|---|---|---|
| `config_version` | integer | `>=1`，节点配置每次变化后单调递增 |
| `issued_at` | timestamp | 服务端生成配置的 UTC 时间 |
| `metrics.collect_interval_seconds` | integer | `5..300`；默认 5，且不能大于 report interval |
| `metrics.report_interval_seconds` | integer | `5..300`；默认 10，且不能小于 collect interval |
| `metrics.mountpoints` | string[] | 1..32 项；至少包含 `/`；去重的绝对挂载点 |
| `metrics.include_virtual_interfaces` | boolean | 是否把容器/虚拟网卡计入汇总；`lo` 始终排除 |
| `agent.config_refresh_interval_seconds` | integer | `10..86400`；默认 60 |
| `agent.max_memory_queue_seconds` | integer | `1..300`；V1 硬上限 300，且不得小于 `metrics.report_interval_seconds` |
| `limits.max_batch_samples` | integer | `1..120`；服务端最终上限仍为 120 |
| `probe_targets` | array | 每节点最多 32 项 |

探测目标字段：

| 字段 | 类型 | 约束 |
|---|---|---|
| `id` | UUID | 服务端目标 ID |
| `name` | string | 1..128 字符，仅展示 |
| `type` | enum | 当前版本仅 `tcp`、`http`、`https`；`icmp` 保留到后续实现，本版本 API 不下发且 Agent 必须拒绝 |
| `host` | string | DNS 名或 IP，不含 URL scheme、userinfo、路径或 Shell 语法 |
| `port` | integer/null | 键必需；TCP 值必填 1..65535；HTTP(S) 值可为 null，表示默认 80/443 |
| `path` | string/null | 键必需；HTTP(S) 值可为 null，null 等价默认 `/`；非空必须以 `/` 开头；TCP 值必须为 null |
| `interval_seconds` | integer | 10..86400；默认 30 |
| `timeout_seconds` | integer | 1..`min(interval_seconds, 60)`；默认 3 |
| `retention_seconds` | integer | 1..7,776,000；API 和数据库再次校验 |
| `enabled` | boolean | false 时 Agent 不执行该目标 |
| `config_version` | integer | 单目标单调递增版本，`>=1` |

基础采集与运行参数持久化在 PostgreSQL 的强类型 `node_agent_settings` 列中，挂载点使用受约束且保序的 `TEXT[]`，不保存自由 JSON。创建节点时若管理员未提交设置，API 必须在同一事务落库默认值：collect=5、report=10、mountpoints=`["/"]`、include_virtual=false、refresh=60、max_queue=300、max_batch=120。管理员替换设置时必须完整校验并在同一事务递增 `nodes.config_version`、更新时间并记录审计；探测目标变化也递增节点配置版本。`GET /agent/config` 只从这些持久设置和该节点的探测目标组装完整响应，禁止靠进程内硬编码产生不可恢复配置。

Agent 只有在完整配置通过严格 schema、全部范围校验以及 `max_memory_queue_seconds >= report_interval_seconds` 的跨字段校验后才原子替换 last-known-good 配置；否则保留旧配置并记录本地结构化错误。不得通过容错逻辑解释未知字段。

## 4. 批量上报 `POST /report`

请求头：

```http
Authorization: Bearer <agent-token>
Content-Type: application/json
Content-Encoding: gzip   # 可选
```

请求：

```json
{
  "batch_id": "0191f724-4cf8-7d71-917a-6468f58cb17d",
  "sequence": 1842,
  "agent_time": "2026-08-20T09:31:10.250Z",
  "agent_version": "1.0.0",
  "config_version": 13,
  "metrics": [
    {
      "sampled_at": "2026-08-20T09:31:10Z",
      "cpu_percent": 23.4,
      "load_1": 0.42,
      "load_5": 0.37,
      "load_15": 0.31,
      "uptime_seconds": 86400.5,
      "memory_total_bytes": 8589934592,
      "memory_used_bytes": 4294967296,
      "memory_available_bytes": 4034920448,
      "swap_total_bytes": 2147483648,
      "swap_used_bytes": 0,
      "network_rx_bps": 125000,
      "network_tx_bps": 38000,
      "network_rx_bytes": 986542311,
      "network_tx_bytes": 486521003
    }
  ],
  "disks": [
    {
      "sampled_at": "2026-08-20T09:31:10Z",
      "mountpoint": "/",
      "total_bytes": 107374182400,
      "used_bytes": 53687091200,
      "available_bytes": 48318382080
    }
  ],
  "probe_results": [
    {
      "target_id": "0191f70a-a340-75b4-8d44-0d79b06e206a",
      "sampled_at": "2026-08-20T09:31:09Z",
      "sent_count": 1,
      "received_count": 1,
      "latency_sum_us": 21840,
      "latency_min_us": 21840,
      "latency_max_us": 21840,
      "http_status_code": null,
      "error_code": null
    }
  ]
}
```

### 4.1 顶层字段

| 字段 | 类型 | 必填 | 约束与用途 |
|---|---|---:|---|
| `batch_id` | UUID | 是 | 每次新建批次随机生成；重试必须复用原值 |
| `sequence` | positive int64 | 是 | `1..9,223,372,036,854,775,807`；节点级严格递增并持久化，服务端以高水位拒绝回退或重放；`batch_id` 仍是精确幂等键 |
| `agent_time` | timestamp | 是 | 生成请求时的 Agent 当前时间，用于估算时钟偏差 |
| `agent_version` | string | 是 | 1..64 字符 |
| `config_version` | integer | 是 | Agent 已成功应用的节点配置版本，`>=1`；滞后只触发观测，不触发命令 |
| `metrics` | array | 是 | 可为空；最多 `max_batch_samples` 项 |
| `disks` | array | 是 | 可为空；整个请求仍受 256 KiB 和批次限制 |
| `probe_results` | array | 是 | 可为空；整个请求仍受 256 KiB 和批次限制 |

三个数据数组不能同时为空。每个批次最多包含 120 个不同 `sampled_at` 采样时刻；每个数组最多 120 项，且磁盘行与探测行的组合还受请求体和每节点目标/挂载点上限约束。

### 4.2 基础指标字段

- `sampled_at`：必填 RFC 3339 时间。
- `cpu_percent`：有限数值，0..100。
- `load_1`、`load_5`、`load_15`、`uptime_seconds`：有限非负数值。
- `memory_total_bytes`、`memory_used_bytes`、`memory_available_bytes`、`swap_total_bytes`、`swap_used_bytes`：非负整数；used/available 不得超过对应 total。
- `network_rx_bps`、`network_tx_bps`：有限非负数值，单位 bytes/s。
- `network_rx_bytes`、`network_tx_bytes`：非负累计字节整数；计数器重置可以使新值变小，API 不据此拒绝批次。

网络值为所有选中且非 `lo` 接口的汇总。磁盘项包含 `sampled_at`、规范化绝对 `mountpoint`、`total_bytes`、`used_bytes`、`available_bytes`；所有字节字段是非负整数，used 和 available 均不得超过 total。

CPU 使用率和网络速率依赖两次累计计数器的差值。Agent 启动后第一次采集、计数器重置或时间间隔无效时，不得把尚未形成有效差值的基础指标样本加入上报批次；协议中不使用伪造的 `0` 或额外 `*_valid` 字段代替有效数据。

### 4.3 探测结果字段

| 字段 | 类型 | 约束 |
|---|---|---|
| `target_id` | UUID | 必须属于当前 Token 节点且存在于已下发配置 |
| `sampled_at` | timestamp | 原始 Agent 探测时间 |
| `sent_count` | integer | 当前仅启用单次 TCP/HTTP(S) 探测，固定为 `1`；未来引入多包协议时必须同步升级契约 |
| `received_count` | integer | 当前只允许 `0` 或 `1`，且不得大于 `sent_count` |
| `latency_sum_us` | integer | 非负；所有成功响应延迟之和 |
| `latency_min_us` | integer/null | 无成功响应时必须 null |
| `latency_max_us` | integer/null | 无成功响应时必须 null |
| `http_status_code` | integer/null | 键必需；HTTP(S) 的 `received_count=1` 时必须填 100..599；200..399 为应用层成功，其余合法状态计入 HTTP 失败；HTTP(S) 未收到响应或 TCP 探测时必须为 null |
| `error_code` | string/null | 键必需；`received_count=0` 时必须填 1..128 字符的稳定、非敏感错误码；`received_count=1` 时必须为 null；不得有首尾空白、换行、目标响应正文或凭据 |

`received_count=0` 时 `latency_sum_us=0`、min/max/status 为 null 且 `error_code` 非 null；`received_count=1` 时 min/max 非负、`min <= sum <= max` 且 `error_code` 为 null。HTTP 4xx/5xx 等合法状态表示传输层已接收响应，因此保留延迟和状态码，服务端另行累计应用层失败；范围外状态必须转换为 `invalid_response`，且 `http_status_code=null`。丢包率、HTTP 失败率和平均延迟不由 Agent 作为浮点字段上报，服务端从可加计数与总和准确计算。

### 4.4 成功响应

首次接受返回 `202 Accepted`：

```json
{
  "batch_id": "0191f724-4cf8-7d71-917a-6468f58cb17d",
  "status": "accepted",
  "received_at": "2026-08-20T09:31:10.410Z",
  "clock_status": "ok",
  "current_config_version": 13
}
```

幂等重试返回 `200 OK`，`status` 为 `duplicate`；`received_at` 和 `clock_status` 复用首次接受时写入台账的值，`current_config_version` 在响应时重新读取节点当前版本，因此配置变化后可以高于原响应。该字段仅提示 Agent 在正常轮询时拉取配置，不构成 action 或命令。

## 5. 批次幂等与事务

精确幂等键固定为数据库唯一键 `(node_id, batch_id)`；`node_id` 由 Token 得到。每个节点另有永久保存的 `last_accepted_sequence` 高水位。Agent 同一时刻只允许一个在途 report：新批次先原子持久化递增 sequence，再发送；失败或响应丢失时必须用完全相同的 `batch_id`、sequence 和正文重试，确认成功前不能越过该批次发送后续序列。处理顺序：

1. 认证 Token 并取得 node_id。
2. 解压和严格校验完整请求，计算服务端 `received_at` 与时间归一化结果。
3. 开启数据库事务并以 `SELECT ... FOR UPDATE` 锁定节点行；先按 `(node_id,batch_id)` 查询台账。
4. 已有批次且规范化正文的 SHA-256 与 `payload_checksum` 相同：不再次写任何 current、ring 或 probe 行，返回 duplicate；同一 batch_id 但 checksum 不同则返回 `409 idempotency_key_reused`，并记录不含正文的安全审计/日志。
5. 台账不存在时要求 `sequence > nodes.last_accepted_sequence`；否则返回 `409 stale_sequence`。序列允许有缺口但绝不允许倒退或复用。
6. 首次接受时在同一事务插入包含 payload checksum、received_at 和 clock_status 的 `processed_batches`、写入全部数据与节点接收状态，并把 `last_accepted_sequence` 更新为本批 sequence，然后提交；任一步失败全部回滚。

不得部分接受数组后返回成功；任一字段非法时整个批次回滚。网络重试必须原样复用 `batch_id`、sequence 和正文，不能为相同样本生成新 ID。Agent 必须把下一个 sequence 以权限 `0600` 原子持久化；状态遗失时不得猜测或回退，必须走管理员批准的重新注册流程。重新注册会在同一事务吊销旧 Agent Token、重置节点高水位并签发新 Token，旧 Token 请求因认证失败不能跨 epoch 重放；普通 Token 轮换不重置 sequence。

`processed_batches` 至少保留 24 小时，用于对常规重试返回 exact duplicate；清理后同一正文仍会因 sequence 不高于永久节点高水位而被拒绝，不能重新污染 current、ring 或 raw。仅靠 `sampled_at` 相对客户端 `agent_time` 的时间窗不能防重放，因此不得把时间窗当作幂等替代品。

## 6. 时间偏差与 `effective_at`

服务端为每批固定：

```text
received_at = 数据库/API 的服务端 UTC 接收时间
skew        = agent_time - received_at
```

冻结阈值和算法：

1. 每项必须满足 `agent_time - 300s <= sampled_at <= agent_time + 30s`；否则整个批次返回 `422 sample_time_out_of_window`。
2. `abs(skew) <= 120s` 时 `clock_status=ok`，每项 `effective_at = sampled_at`。
3. `abs(skew) > 120s` 时 `clock_status=skewed`，每项 `effective_at = received_at + (sampled_at - agent_time)`。这保留批次内相对时间，同时消除错误绝对时钟。
4. 原始 `sampled_at`、批次 `agent_time`、服务端 `received_at` 与判定后的 `effective_at` 分别保存；不得覆写原始时间。
5. API 用 `effective_at` 选择 5 秒槽、聚合桶和保留 cutoff；节点在线/离线只使用最新成功认证请求的 `received_at`，绝不使用 sampled_at、effective_at 或 Agent 自报状态。

Agent 时钟恢复到阈值内后，节点 `clock_status` 可回到 ok；状态变化应有结构化日志。异常时钟批次经归一化后仍可用于曲线，但不能创建遥远过去/未来的桶。认证成功但正文校验失败的请求不得更新“最后成功上报”时间。

## 7. 重试、队列与配置一致性

- enroll 不自动盲重试；见注册的一次性语义。
- config GET 是幂等的，可按退避策略重试；304 不改变本地配置。
- report 在超时、连接失败、429 或 5xx 时重试同一 batch；收到 2xx 后移除队列。除非响应明确可重试，4xx 不盲重试。
- 内存队列按采样时间排序并限制为 300 秒；满时先丢最旧样本。进程重启允许丢失队列，V1 不写盘。
- Agent 必须在持久化新 sequence 之前选出同时满足数组条数和压缩/未压缩 256 KiB 上限的最大非空稳定队首。若单次采集因合法挂载点的 JSON 转义仍无法放入一批，可把其磁盘项拆为多个连续、各自幂等的批次；基础指标只发送一次，未发磁盘余量保留原入队 ID、`sampled_at` 和单调寿命，不得丢失或延长 5 分钟期限。
- Agent 仅在完整新配置验证成功后更新本地 `config_version`；report 中的版本让服务端观察配置滞后。服务端不得以“版本滞后”为由下发即时任务或远程操作。
- 配置切换后，队列中尚未过期的旧版本磁盘样本仍可上报，但 `mountpoint` 必须属于节点当前配置，或已存在于该节点的 `node_disk_current`；这既允许通常的五分钟旧队列收尾，也阻止持有 Agent Token 的异常客户端跨批次制造无限新挂载点。已由管理员硬删除的探测目标不再接受旧队列结果，整批返回 `422`，Agent 按非重试 4xx 丢弃该批并记录结构化错误。

## 8. 命令字段与通用执行能力永久禁止

配置采用字段白名单，不存在扩展 action 对象。以下键及其任何大小写/命名变体都不属于协议，服务端不得产生，Agent 收到后必须拒绝整份新配置并保留 last-known-good：

```text
command, cmd, exec, shell, script, pty, ssh, terminal
file, upload, download, service, restart, reboot, upgrade
tunnel, reverse_tunnel, port_forward, plugin, action, args, env
```

允许的 `path` 仅表示 HTTP(S) URL path，不能解释为文件路径；`host` 仅是探测目标，不能拼接进 Shell。HTTP(S) 探测固定为无请求体的 GET，V1 配置不允许任意 Header、凭据、模板或脚本钩子。

实现约束：

- Go Agent 使用强类型结构并拒绝未知 JSON 字段，不使用 `map[string]any` 驱动行为。
- 不链接 SSH、PTY、Shell、远程升级或通用插件执行依赖。
- 不调用 `/bin/sh`、`exec.Command` 或等价系统命令完成探测。
- Agent 进程使用低权限用户；ICMP 需要时只授予 `CAP_NET_RAW`，不整体以 root 运行。
- 不实现文件浏览/传输、服务控制、系统重启、端口监听或反向连接。

## 9. 协议验收用例

- 注册令牌并发使用只有一个事务成功；设备 Token 只出现一次且数据库无明文。
- Token 对节点 A 签发时，不能上报节点 B 的 target_id，也不能通过正文伪造 node_id。
- 同一 `(node_id,batch_id)` 重试不会重复写入探测结果或覆盖 current；不同节点可独立使用相同 UUID 而不冲突。
- exact duplicate、同 ID 异正文、sequence 缺口/回退、台账清理后的旧请求和重新注册 epoch 都按冻结状态机处理；配置版本滞后只产生状态提示。
- 正常、±120 秒边界、超过阈值、超过队列窗口和未来采样时间均按冻结算法处理。
- gzip 解压后超过 256 KiB、数组超限、未知字段、重复 key、无效数值和部分非法数组均整批失败。
- config 返回任何禁止字段时 Agent 拒绝配置且继续使用 last-known-good。
- 代码扫描与运行检查证明 Agent 无监听端口、无命令/脚本/SSH/PTY/文件/隧道/远程升级能力。
