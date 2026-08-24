# 第 6 阶段负载测试报告

> 状态：内网持久化预览的只读负载冒烟通过；生产性能与容量尚未验收。

## 1. 目的和范围

本报告验证白名单内匿名游客读取接口在两档小规模并发下的错误率和 P95。请求只访问 `/api/v1/panel/` GET 接口，不携带 Cookie、Authorization 或 CSRF 头。它是部署回归冒烟，不代表容量上限、长稳、峰值流量或多地域结果。

## 2. 环境元数据

| 字段 | 实际值 |
|---|---|
| 执行时间（UTC） | 基线 2026-08-23 09:57；扩展 2026-08-23 10:12 |
| 源码版本/提交标识 | Stage 6 同步源码快照；当前目录不是 Git 仓库 |
| Debian / 内核 | Debian 13.5 / `6.12.90+deb13.1-amd64` |
| CPU / 内存 | 6 vCPU / 8,294,096,896 bytes |
| Nginx | 1.26.3 |
| probe-api | `dev` |
| PostgreSQL | 17.11；未另设资源上限 |
| 数据规模 | 1 个节点、1 个探测目标；数据库 12,990,131 bytes |
| 测试机与网络 | 负载生成器和服务同一 VM，通过 VM 自身 `192.168.33.253` 地址访问；未经过代理 |
| 目标 URL | `https://192.168.33.253:18453/api/v1/panel/nodes?limit=50` |
| TLS | 预览自签名证书；使用 `--cacert /srv/probe-stage5-preview/tls/server.crt` |

## 3. 参数和命令

基线冻结阈值：100 请求、并发 10、错误率不高于 1%、成功请求 P95 不高于 1000 ms。脚本单请求连接超时 5 秒、总超时 15 秒，预期状态为 200。

```bash
bash probe-api/deploy/scripts/load-smoke.sh \
  --url 'https://192.168.33.253:18453/api/v1/panel/nodes?limit=50' \
  --requests 100 \
  --concurrency 10 \
  --max-error-rate 1 \
  --max-p95-ms 1000 \
  --cacert /srv/probe-stage5-preview/tls/server.crt
```

扩展轮次仅把请求数和并发改为 1000/25，阈值不放宽。P95 使用成功请求排序后的最近秩；错误数由 curl 失败、非预期状态和无效测量组成。

## 4. 实际结果

### 4.1 基线：100 请求 / 并发 10

| 指标 | 实际值 |
|---|---:|
| 脚本退出码 | `0` |
| 脚本报告持续时间 | `0s`（不足 1 秒） |
| 测量数 / 成功数 | `100 / 100` |
| curl 失败 / 非预期状态 / 无效测量 | `0 / 0 / 0` |
| 错误率 | `0.000%` |
| min / avg / P95 / max | `5.754 / 12.613 / 19.534 / 22.272 ms` |
| HTTP 状态分布 | `200=100` |

### 4.2 扩展：1000 请求 / 并发 25

| 指标 | 实际值 |
|---|---:|
| 脚本退出码 | `0` |
| 持续时间 | `4s` |
| 测量数 / 成功数 | `1000 / 1000` |
| curl 失败 / 非预期状态 / 无效测量 | `0 / 0 / 0` |
| 错误率 | `0.000%` |
| min / avg / P95 / max | `6.982 / 31.939 / 64.721 / 107.767 ms` |
| HTTP 状态分布 | `200=1000` |

两轮均明显低于冻结的 1% 错误率和 1000 ms P95 阈值。

## 5. 服务端观测

| 指标/证据 | 测试前 | 测试中 | 测试后 |
|---|---:|---:|---:|
| API readiness | `200` | 未连续采样；负载请求全部 `200` | `200` |
| API systemd 重启计数 | `0` | `0` | `0` |
| VM CPU 忙碌率 | 未单独取点 | `vmstat` 1 秒样本最高 `99%` | 最后样本 `22%` |
| 可用内存 | 6,505,861,120 bytes | 最低 6,464,028,672 bytes | 6,485,585,920 bytes |
| API RSS | 20,548 KiB | 最高 20,548 KiB | 20,356 KiB |
| PostgreSQL 活跃连接（含采样连接） | 8 | 最高 8 | 8 |
| 负载请求中的 `4xx/5xx` | `0/0` | `0/0` | `0/0` |

负载生成器和服务共享 6 vCPU，扩展轮次的 CPU 峰值包含 curl 与采样进程，不能解释为 API 单独消耗，也不能据此推导生产容量。测试后 PostgreSQL、API、Nginx、Agent 和备份 timer 五个持久化单元均为 `active`。

## 6. 证据

| 文件 | SHA-256 |
|---|---|
| `/srv/probe-stage5-preview/artifacts/stage6/load-smoke-20260823T095711Z.log` | `4bb8330375c0728b7d2e65a25fd48a81f3ad9411b365c162b57943885d1a09f0` |
| `/srv/probe-stage5-preview/artifacts/stage6/load-smoke-extended-20260823T101201Z.log` | `dfe6fffed421bf4e926a59bd5e38d12d5160d932a8e923dafb64f9ab9afa092b` |
| `/srv/probe-stage5-preview/artifacts/stage6/load-vmstat-20260823T101201Z.log` | `26c9f7b6d23af1117d276c9238a5627c6c93dcca96e2b9552b6381d5b5f4fa4d` |
| `/srv/probe-stage5-preview/artifacts/stage6/load-resources-20260823T101201Z.log` | `498b1da9e6957f189cf87d7bd4239cc321c245a29d2353346c7dca4fab1c36eb` |

## 7. 结论与后续

内网预览的只读负载冒烟通过：测量完整、零错误、P95 达标，测试前后 readiness 正常且无服务重启。生产性能与容量仍未验证；正式上线前应使用生产域名和资源配置补做多档并发、30 分钟以上长稳、大数据集最坏查询，以及备份/聚合/清理任务与读取并行测试。
