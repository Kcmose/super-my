# Agent 部署与升级

Agent 是独立的 Go 工程和 systemd 服务。它只主动访问 `https://<api-host>/api/v1/agent`，不监听端口，不接受服务端反连，也不包含命令、Shell、文件管理、隧道或远程自动升级能力。

## 1. 支持范围

V1 支持 Linux `amd64` 和 `arm64`。生产构建必须在 Debian 13 构建环境中从同步的 `probe-agent/` 源码完成；不要在 Windows 本地构建。

服务使用：

```text
二进制：/usr/local/bin/probe-agent
环境文件：/etc/probe-agent/probe-agent.env
可选私有 CA：/etc/probe-agent/private-ca.pem
状态：/var/lib/probe-agent/state.json
systemd：probe-agent.service
用户/组：probe-agent
```

`state.json` 保存节点 ID、Agent Token 和下一个批次序号，是敏感且不可随意重建的持久状态。

## 2. 在 Debian 13 构建机验证和构建

从新的同步源码目录执行：

```bash
cd /var/tmp/probe-source-20260823/probe-agent
go test ./...
go vet ./...
CGO_ENABLED=0 go build -trimpath \
  -ldflags '-s -w -X main.version=1.0.0' \
  -o /var/tmp/probe-agent ./cmd/probe-agent
sha256sum /var/tmp/probe-agent
```

为另一架构构建时显式设置 `GOOS=linux` 和 `GOARCH=amd64` 或 `GOARCH=arm64`。构建产物应通过受控的软件包、配置管理或人工文件分发传到目标机；项目不提供远程自动升级。

## 3. 从管理面板一键首次安装

管理面板的“节点与凭证”页面提供推荐流程：

1. 点击“新建节点”，保持节点为已启用并保存元数据和采集设置；禁用节点不会签发安装命令；
2. 保存成功后，管理端自动请求一枚默认 900 秒有效、只能消费一次的注册令牌；同节点此前未使用的安装命令会立即失效；
3. API 只在本次禁止缓存的响应中返回安装命令，前端不写入 `localStorage`、`sessionStorage` 或日志；
4. 先登录 root 或执行 `sudo -i` 进入 root Shell，再直接粘贴完整单行命令；命令通过 `bash` 启动安装器，不依赖目标机预装 `sudo`；
5. 关闭弹窗会移除当前页面对命令的引用，但不会替你清除 Shell 历史、浏览器开发工具或系统剪贴板；使用后应主动覆盖。若命令过期或丢失，在未注册节点上重新签发即可；
6. 已注册节点会显示“重新安装命令”并二次确认。新命令本身不影响当前 Agent，但它一旦在任意主机注册成功，原 Agent Token 会立即失效。

目标机至少需要 systemd、`bash`、`curl`、`sha256sum`、`awk`、`install`、`getent`、`useradd/groupadd` 及常用 coreutils；安装命令必须在 root Shell 中执行，但不要求系统安装 `sudo`。命令以严格 HTTPS 从 `https://raw.githubusercontent.com/Kcmose/my-agent/refs/tags/v1.0.2/deploy/install.sh` 读取安装器；该 GitHub Release 已启用不可变保护，标签锁定后不能移动或复用，执行内容不会随 `main` 漂移。安装器把全部副作用放在最终 `main()` 中，只有 Shell 完整解析下载内容及文件末尾入口后才开始安装；中途截断不会留下半安装或落盘令牌。随后它由 `-e` 自动推导 Agent API 与下载根，自动识别 `amd64`/`arm64`，再下载 `SHA256SUMS`、对应二进制和 systemd 单元，严格校验清单中的唯一 SHA256 项后才落盘。各下载有明确连接与总时限，最坏预算明显短于 15 分钟令牌期限。

安装器会创建或严格验证 `probe-agent` 低权限用户；既有账户必须是非 root、主组唯一、无附加组并使用非登录 Shell。注册成功后，它原子删除环境文件中的 `PROBE_AGENT_ENROLLMENT_TOKEN` 并重启服务，确认没有一次性令牌也能从 `state.json` 恢复。失败时同样删除落盘令牌、停止并禁用服务；若尚未产生 Agent 身份，还会删除本次创建的固定托管文件以允许干净重试。任一固定二进制、配置、CA、systemd 单元、`state.json` 已存在或服务已运行都会拒绝，绝不把首次安装器当升级器。下载目录只包含公开资产，不包含节点令牌。

安装命令本身包含一次性令牌，可能被终端写入 Shell 历史、浏览器开发工具和系统剪贴板。Komari 式短命令通过 `-t` 传入令牌，因此令牌也会短暂出现在 `bash` 的进程参数中；它仍不会进入下载 URL，安装器不会记录其值，注册完成或失败后也会从磁盘环境文件清除。只在可信管理员终端操作，不要发到聊天、工单或共享脚本。15 分钟有效期、一次消费和注册后清理只缩短风险窗口，不能把命令当成非敏感文本。需要避免命令行参数时，可继续手动下载已校验安装器并使用兼容的 `--enrollment-token-stdin`。

私有 CA 的 IP+端口模式下，API 通过 `PROBE_AGENT_INSTALL_CA_FILE` 在启动时读取并验证公开 PEM 证书，只把该文件的 64 位 SHA-256 写入命令；同一证书必须作为固定 `/downloads/probe-agent/ca.pem` 发布。安装器第一次获取 `ca.pem` 时不发送令牌，只在精确哈希匹配后才把它设为后续 curl 与 Agent 的 CA；不匹配立即失败，不能继续下载清单或注册。该文件不得包含私钥。三域名模式必须使用系统公信证书且不配置此项。

此安装器只负责第一次接入。它不被 Agent 或服务端自动调用，不提供远程升级、回连、命令、脚本或文件管理能力。

## 4. 手动首次安装

在 Agent 目标机创建低权限账户：

```bash
addgroup --system probe-agent
adduser --system --ingroup probe-agent --no-create-home \
  --home /nonexistent --shell /usr/sbin/nologin probe-agent

install -o root -g root -m 0755 /path/to/probe-agent /usr/local/bin/probe-agent
install -d -o root -g probe-agent -m 0750 /etc/probe-agent
install -o root -g root -m 0600 \
  /path/to/probe-agent.env.example \
  /etc/probe-agent/probe-agent.env
install -o root -g root -m 0644 \
  /path/to/probe-agent.service \
  /etc/systemd/system/probe-agent.service
```

编辑环境文件：

```text
PROBE_AGENT_API_URL=https://真实-api-域名/api/v1/agent
PROBE_AGENT_STATE_FILE=/var/lib/probe-agent/state.json
PROBE_AGENT_ENROLLMENT_TOKEN=一次性注册令牌
```

API URL 必须是 HTTPS，路径只能是 `/api/v1/agent`。环境文件由 systemd 在降权前读取，应保持 `root:root 0600`。如果使用私有 CA，把公开 PEM CA bundle 设置为 `root:root 0644`，并设置 `PROBE_AGENT_CA_FILE=/etc/probe-agent/private-ca.pem`；不要关闭 TLS 验证。

一次性注册令牌只能从独立管理端点生成；该 `no-store` 响应保留明文字段用于手动部署兼容。手动模式不要把它放进镜像、源码、命令行参数或日志，而应只临时写入 `root:root 0600` 的环境文件。管理面板自动生成的一键命令是推荐路径，其风险与清理语义见上一节。

## 5. 注册和移除一次性令牌

```bash
systemctl daemon-reload
systemd-analyze verify /etc/systemd/system/probe-agent.service
systemctl enable --now probe-agent.service
journalctl -u probe-agent -n 100 --no-pager
```

首次注册成功后确认状态文件存在且权限安全：

```bash
stat -c '%U %G %a %n' /var/lib/probe-agent /var/lib/probe-agent/state.json
```

目录应是 `probe-agent:probe-agent 0700`，状态文件应是 `0600`。随后立即从环境文件删除 `PROBE_AGENT_ENROLLMENT_TOKEN` 行并重启：

```bash
editor /etc/probe-agent/probe-agent.env
systemctl restart probe-agent.service
```

Agent 会从原子状态文件恢复身份，不会再次注册。若注册请求结果不确定、状态文件写入失败或状态损坏，Agent 会停止并要求管理员处理；不要反复使用同一个一次性令牌。先在管理面板吊销未确认凭据，再签发新令牌。

## 6. 运行验收

```bash
systemctl is-active probe-agent.service
journalctl -u probe-agent --since '-10 minutes' --no-pager
ss -lntp
```

验收要求：

- Agent 没有任何监听 socket；
- 只主动连接配置的 HTTPS API；
- systemd 使用 `probe-agent` 低权限账户、`NoNewPrivileges`、只读系统保护和 `SocketBindDeny=any`；
- 管理面板能看到节点持续上报；
- 日志不包含一次性令牌、Agent Token 或状态文件内容。

## 7. 手动升级

Agent 不接受远程升级指令。使用人工或配置管理流程：

1. 在 Debian 13 构建机上测试并构建新二进制，记录 SHA-256；
2. 把新二进制传到目标机的临时路径；
3. 校验架构、所有者、权限和 SHA-256；
4. 把当前二进制复制到 root-only 回退路径；
5. 用同文件系统临时文件加 `mv -T` 原子替换 `/usr/local/bin/probe-agent`；
6. 重启并检查日志和上报；
7. 保留 `/etc/probe-agent/probe-agent.env` 和 `/var/lib/probe-agent/state.json`，绝不能用新包覆盖或删除。

示例：

```bash
install -o root -g root -m 0755 /path/to/new-probe-agent \
  /usr/local/bin/.probe-agent.new
cp -a /usr/local/bin/probe-agent /usr/local/bin/probe-agent.previous
mv -T /usr/local/bin/.probe-agent.new /usr/local/bin/probe-agent
systemctl restart probe-agent.service
systemctl is-active probe-agent.service
```

若新版本失败，恢复旧二进制并重启。不要回退或删除 Agent 状态文件；批次序号回退会被服务端拒绝，Token 状态也不能从日志或旧配置重建。

## 8. 卸载注意事项

停止和禁用服务不等于从服务端删除节点。服务端删除节点只会吊销认证，不会向 Agent 下发命令。卸载 Agent 时，先在管理面板吊销 Token，再在目标机人工停止服务；只有在明确不再恢复该 Agent 身份时，才可单独确认删除 `/var/lib/probe-agent/state.json`。
