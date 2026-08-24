# V1 数据保留与聚合语义（冻结）

状态：阶段 0 冻结稿

时间基准：数据库和 API 全部使用 UTC；所有区间均为半开区间 `[from, to)`，除非本文另有说明。

## 1. 核心定义

- **基础指标**：CPU、Load、内存、Swap、网络和磁盘历史。
- **探测结果**：当前阶段每个 TCP/HTTP(S) 目标的发送数、接收数、延迟与 HTTP 状态统计；`icmp` 仅是后续保留协议值，本阶段不接受、不下发、不执行。
- **`sampled_at`**：Agent 原始采样时间，永久保留其原始含义，仅用于诊断和审计。
- **`received_at`**：API 收到批次的服务端时间，也是节点在线状态的唯一时间依据。
- **`effective_at`**：服务端校验/归一化后的事件时间；环形槽位、历史查询、聚合和保留清理一律使用它。
- **`as_of`**：一次查询或清理事务开始时固定取得的数据库时间，整个操作中不重复取“现在”。
- **保留期 `R`**：探测目标的 `retention_seconds`，必须满足 `0 < R <= 7,776,000`。`7,776,000` 秒精确定义为 90×24×60×60，不是自然月。

5 分钟是基础指标的历史保留期限，不是采集周期。默认采集周期为 5 秒，所以通常每节点约 60 个点。

## 2. 基础指标 5 分钟环形表

### 2.1 槽位算法

`node_metric_ring` 的主键为 `(node_id, slot)`，`node_disk_ring` 的主键为 `(node_id, mountpoint, slot)`。槽位固定为 60 个：

```text
epoch_5s = floor(unix_seconds(effective_at) / 5)
slot     = epoch_5s mod 60       # 取值 0..59
```

PostgreSQL 计算表达式等价于：

```sql
mod(floor(extract(epoch FROM effective_at) / 5)::bigint, 60)
```

所有计算按 UTC Unix epoch 对齐，不受数据库 Session 时区或夏令时影响。数据库须有 `CHECK (slot >= 0 AND slot < 60)`。

### 2.2 写入冲突规则

同一节点同一槽位采用 UPSERT，但延迟到达的旧样本不能覆盖较新的样本：

```sql
INSERT INTO node_metric_ring (...)
VALUES (...)
ON CONFLICT (node_id, slot) DO UPDATE
SET effective_at = EXCLUDED.effective_at,
    sampled_at   = EXCLUDED.sampled_at,
    received_at  = EXCLUDED.received_at,
    ...
WHERE EXCLUDED.effective_at > node_metric_ring.effective_at
   OR (
        EXCLUDED.effective_at = node_metric_ring.effective_at
        AND EXCLUDED.received_at > node_metric_ring.received_at
      );
```

磁盘表使用同样规则，并把 `mountpoint` 纳入冲突键。一个 5 秒窗内出现多个样本时只保留 `effective_at` 最新者；完全相同时以 `received_at` 最新者为准。批次幂等仍由 `processed_batches` 保证，槽位 UPSERT 不是批次幂等的替代品。

`node_metric_current` 和 `node_disk_current` 只在新样本的 `(effective_at, received_at)` 更新时覆盖，用于当前状态展示；它们不是历史表，可以保留最后状态，但 UI 必须同时展示 `received_at`/离线状态，不能把旧 current 行画入历史曲线。

节点在线状态的 V1 默认阈值为服务端 `received_at` 距查询事务固定 `as_of` 45 秒，可在 10 秒至 24 小时范围内通过服务端部署配置调整。状态优先级固定为：节点被禁用时 `disabled`；尚未注册时 `unregistered`；超过离线阈值时 `offline`；仍在线但时钟被标记异常时 `skewed`；其余为 `online`。旧 current 行可随离线节点返回用于展示“最后状态”，但不能因此把节点判为在线或进入历史点数组。

### 2.3 查询边界

一次基础指标查询固定 `as_of`，实际可见窗口为：

```text
(as_of - 300 seconds, as_of]
```

SQL 必须同时按节点和时间过滤：

```sql
WHERE node_id = $1
  AND effective_at >  $as_of - interval '5 minutes'
  AND effective_at <= $as_of
ORDER BY effective_at ASC
```

严格大于左边界可避免在整 5 秒边界产生第 61 个逻辑点。即使节点离线且槽位没有被覆盖，超过 5 分钟的旧行也不会返回。客户端传入更早的 `from` 不会扩大该窗口。

### 2.4 物理清理与容量上限

- 每分钟删除 `effective_at <= as_of - interval '5 minutes'` 的 ring 行。
- 环形主键使指标表理论上最多 60 行/节点；磁盘表最多 60 行/节点/挂载点，物理清理用于移除离线节点遗留槽位。
- 节点删除时相关 ring/current 行按外键策略清理；这不向 Agent 下发任何操作。
- 基础指标不进入 5m/1h 长期聚合，不备份为长期历史。

## 3. 探测结果事实与精确统计

`probe_result_raw`、`probe_result_5m`、`probe_result_1h` 至少保存：

```text
target_id
effective_at 或 bucket_start
sent_count
received_count
latency_sum_us
latency_min_us
latency_max_us
```

聚合表另存 `result_count`（被聚合的原始/下级结果行数）。约束：

- `sent_count >= 1`，`0 <= received_count <= sent_count`。
- `received_count = 0` 时，`latency_sum_us = 0`，`latency_min_us` 和 `latency_max_us` 为 NULL。
- `received_count > 0` 时，三项延迟均非负，且 `latency_min_us <= latency_sum_us / received_count <= latency_max_us`。

任意层级的展示值都从可加事实计算，不平均“平均值”，也不平均“丢包率”：

```text
loss_rate       = (sent_count - received_count) / sent_count
http_error_count = count(HTTP status < 200 or >= 400)
failure_rate    = (sent_count - received_count + http_error_count) / sent_count
average_latency = received_count == 0 ? null
                   : latency_sum_us / received_count
```

`received_count` 只表示传输层收到响应，因此 HTTP 4xx/5xx 仍保留响应延迟，同时计入 `http_error_count`。合法成功状态固定为 200..399；100..199 和 400..599 计为 HTTP 应用层失败，范围外状态按 `invalid_response` 探测失败处理，不得进入上报队列的状态字段。API 使用浮点/decimal 完成除法，禁止整数截断。延迟单位统一为微秒，响应层可转换成毫秒。

## 4. 多级聚合

### 4.1 桶边界

- 5 分钟桶：`[bucket_start, bucket_start + 5m)`，按 UTC Unix epoch 的 5 分钟整数倍对齐。
- 1 小时桶：`[bucket_start, bucket_start + 1h)`，按 UTC 整点对齐。
- 主键分别为 `(target_id, bucket_start)`。

推荐使用 PostgreSQL `date_bin` 且 origin 固定为 `1970-01-01 00:00:00+00`，不得依赖本地时区的 `date_trunc` 行为。

### 4.2 聚合公式

同一目标、同一桶内：

```text
result_count   = sum(下一级 result_count；raw 每行按 1 计)
sent_count     = sum(sent_count)
received_count = sum(received_count)
http_error_count = sum(http_error_count；raw 按 HTTP status < 200 或 >= 400 计 1)
latency_sum_us = sum(latency_sum_us)
latency_min_us = min(仅 received_count > 0 的 latency_min_us)
latency_max_us = max(仅 received_count > 0 的 latency_max_us)
```

1 小时表从 5 分钟事实聚合，所得 sent/received/http_error_count/sum/min/max 与直接从 raw 聚合等价。每次使用确定性 `INSERT ... ON CONFLICT DO UPDATE` 覆盖整个桶，不能在旧聚合值上重复累加。

### 4.3 调度、迟到数据与水位

- 5 分钟任务每 5 分钟运行，处理所有已闭合桶，并重算最近 30 分钟的闭合桶，以吸收 Agent 最多 5 分钟内存队列和网络重试造成的迟到数据。
- 1 小时任务每小时运行，处理已闭合整点桶，并重算最近 3 个闭合小时。
- 物理清理不得侵入对应的重算窗口：raw 至少保留最近 30 分钟，5m 至少保留最近 3 小时；API 查询仍立即执行目标自己的 `R` cutoff，不会因该内部安全窗口返回过期数据。
- `job_watermarks` 记录每类任务已完整处理到的排他上界。任务恢复时从持久化水位继续，不以进程启动时间猜测缺口。
- 单实例也使用事务；多 API 实例必须先取得对应 PostgreSQL advisory lock，同一任务同一时刻只能有一个执行者。
- 聚合 UPSERT 与水位推进处于同一事务。失败时事务回滚，不得推进水位。

清理源数据之前必须满足：覆盖该时间段的下一级聚合已经成功落库且水位越过其桶结束时间。没有数据的桶可由水位证明已扫描；不能要求虚构一行零值聚合。

## 5. 各层保留范围

对每个目标使用自己的 `R` 和同一事务固定的 `as_of`：

| 表 | 用途 | API 可查询时间 | 聚合与清理成功后的物理目标范围 |
|---|---|---|---|
| `probe_result_raw` | 最近趋势原始精度 | `effective_at >= max(as_of-R, as_of-24h)` | `max(min(R, 24h), 30m)`，且清理前 5m 聚合必须完成 |
| `probe_result_5m` | 中期 5 分钟精度 | `bucket_start >= max(as_of-R, as_of-7d)` | `max(min(R, 7d), 3h)`，且清理前 1h 聚合必须完成 |
| `probe_result_1h` | 长期 1 小时精度 | `bucket_start >= as_of-R` | `R`，最大 90 天 |

API 的时间过滤是硬边界；物理清理因调度或聚合保护而稍晚执行时，不得借此返回边界外的数据。聚合桶以 `bucket_start` 判断可见性，因此跨越保留 cutoff 的边界桶不返回，宁可少显示一个部分桶，也不泄露保留期之外的趋势。

建议删除谓词（实际 SQL 还必须加入聚合完成保护）：

```text
raw：effective_at < max(as_of - R, as_of - 24h)
5m ：bucket_start < max(as_of - R, as_of - 7d)
1h ：bucket_start < as_of - R
```

后台清理频率：

- 每分钟：基础指标 ring 过期行。
- 每 5 分钟：5m 聚合。
- 每小时：1h 聚合，然后按目标 R 清理三层探测数据。
- 每天：过期 Session、Token 和 `processed_batches`；精确幂等台账至少保留 24 小时，且只有对应 raw 已按聚合水位保护规则清理、已无外键引用后才能删除。台账允许因未完成聚合而保留更久，不能通过级联删除绕过源数据保护。台账清理不重置 `nodes.last_accepted_sequence`；该节点级高水位长期保留并永久拒绝旧 sequence，只有吊销全部旧 Agent Token 的管理员重新注册事务可以开始新 epoch 并重置它。

每日清理在同一个事务中处理认证、令牌与幂等台账，冻结删除边界为：Session 在 `expires_at <= as_of` 时删除，或在已吊销且 `revoked_at <= as_of - session_retention` 时删除；登录限流记录在 `updated_at <= as_of - 2 * max(login_ip_window, login_username_window)` 时删除；一次性注册令牌只在 `expires_at <= as_of` 时删除；Agent Token 只在显式 `expires_at` 已到期时删除，无到期时间的吊销记录继续保留；幂等台账只删除 `received_at <= as_of - 24h` 且已经不存在关联 `probe_result_raw` 的行。raw 外键继续作为并发写入时的最终保护，不使用级联删除规避聚合水位。

该事务先取得独立 PostgreSQL transaction-scoped advisory lock，再在锁内读取 `job_watermarks['daily-cleanup']` 的上次成功时间。取得锁的实例如果距离上次成功不足 24 小时必须跳过；只有全部删除成功时才在同一事务推进成功水位，删除与水位必须原子提交，数据库明确返回的事务失败不得推进水位。客户端收到提交错误时不能自行假定成功，后续运行仍以数据库中的持久水位为准。这样既阻止并发实例同时执行，也阻止错峰启动或进程重启在 24 小时窗口内重复执行。

## 6. 查询分辨率

请求先把 `[from,to)` 裁剪到 `[as_of-R, as_of)`；裁剪后为空则返回空数据。`resolution=auto` 选择一个覆盖完整请求区间的统一层级：

1. 区间全部位于最近 24 小时内、跨度不超过 24 小时且预计 raw 点数不超过响应上限时，选 `raw`。
2. 否则，区间全部位于最近 7 天内、跨度不超过 7 天且 5m 点数不超过响应上限时，选 `5m`。
3. 其他合法区间选 `1h`。

默认探测周期为 30 秒，最小允许周期为 10 秒。响应点数硬上限冻结为 2,200；这是为了让完整 90 天的小时桶（最多 2,160 点）无需丢弃。若 raw/5m 预计超限，`auto` 自动提升到更粗一级；若配置切换产生的额外即时探测使实际点数超过估算，`auto` 必须在同一查询快照内继续降级，不能偶发返回 422。显式请求某分辨率但其数据年龄不可用或超过 2,200 点时返回 `422 resolution_unavailable`，不能静默伪装成所请求精度。

5m/1h 候选还必须在同一只读事务中读取对应 `job_watermarks`：水位非空、按桶宽对齐、不晚于 `floor(as_of, bucket_width)`，并且至少覆盖 `floor(to, bucket_width)`；否则 `auto` 跳过该候选，显式分辨率返回 `422 resolution_unavailable`。水位证明的是最近一次已提交聚合的闭合桶覆盖；水位更新后才提交的迟到源数据由 30 分钟/3 小时重算窗口在后续维护轮吸收，因此最新闭合桶是有界最终一致，而不是跨事务即时强一致。

响应必须包含实际 `resolution`、`from`、`to`、`as_of` 和点数组，使前端明确当前粒度。不得把 raw、5m、1h 混在一条未标注的序列中。

## 7. 保留期限的创建与修改

三层都必须拒绝超过 90 天或非正数：

- 前端控件限制 `1..7,776,000` 秒，只改善体验。
- API 在业务写入前验证。
- PostgreSQL `probe_targets.retention_seconds` 使用：

```sql
CHECK (retention_seconds > 0 AND retention_seconds <= 7776000)
```

超过上限返回：

```json
{
  "error": "retention_exceeds_limit",
  "message": "retention_seconds must not exceed 7776000",
  "request_id": "...",
  "details": {
    "max_retention_seconds": 7776000
  }
}
```

缩短 R：API 写入新值后，查询立即使用新 cutoff；物理行由下一次小时清理删除。延长 R：从修改时刻继续积累，已经聚合或删除的数据不恢复，也不得用空值伪造历史。每次修改都记录审计日志中的前后值。

删除或禁用目标：禁用只停止新探测，已有结果继续按 R 查询和清理。V1 的 `DELETE` 明确采用硬删除：API 在同一事务中先写入包含目标摘要和操作者的审计记录，再删除目标；数据库通过外键级联删除该目标的 raw、5m 和 1h 历史。该操作不可恢复，前端必须二次确认。需要保留历史时只能禁用目标，不能删除。无论禁用还是删除，都绝不向 Agent 下发命令。

## 8. 时间异常与保留的关系

当 Agent 时钟偏差超过协议阈值时，服务端保留原始 `sampled_at`，但使用归一化 `effective_at` 入 ring、raw 和聚合桶。这样错误的未来或远古 Agent 时间不能延长保留期、污染长期曲线或改变在线状态。具体算法见 `agent-protocol.md`。

所有清理 cutoff 使用数据库服务端时间和 `effective_at`，禁止使用 Agent 声明的当前时间。

## 9. 验收不变量

- 任意节点的 `node_metric_ring` 不超过 60 行；任意节点/挂载点的 `node_disk_ring` 不超过 60 行。
- 所有基础指标查询均不返回 `effective_at <= as_of-5m` 的点，即使清理任务停止。
- 聚合前不删除源数据；任务重复运行不会重复累计。
- 由聚合事实重新计算的总发送数、总接收数、平均延迟和丢包率与原始事实一致。
- 不同目标的 R 独立生效，任何 API 查询都不能越过该目标 cutoff。
- `R > 7,776,000` 在 API 和数据库均失败；完整 90 天查询最多返回小时精度趋势。
- 缩短 R 后旧数据立即不可查询，延长 R 不会恢复已经删除的数据。
- 时钟严重偏差的 Agent 不会创建未来桶或突破保留边界；在线状态只依赖 `received_at`。
