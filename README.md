# oh-my-api

English: [README.en.md](./README.en.md)

oh-my-api 是一个基于 Go + Gin 的**个人/小团队自托管 AI API 网关**。它为 OpenAI、Anthropic、OpenAI Responses 和兼容服务提供统一入口，并按配置完成模型映射、协议转换、调度、回退、统计与监控。

项目面向受控环境中的个人开发者、小团队和 Agent 工具使用者：在保留常见客户端协议的前提下，组合多个云端或本地 Provider，获得更稳定、灵活的模型访问方式。

> 本项目不是多租户企业 AI 平台：当前不提供 RBAC、SSO、审计、计费、分布式限流或跨实例高可用控制面。

## 功能概览

### API 与协议

| 能力 | 状态 |
|------|------|
| OpenAI Chat Completions | `POST /v1/chat/completions` |
| Anthropic Messages | `POST /v1/messages` |
| OpenAI Responses | `POST /v1/responses` |
| OpenAI Embeddings | `POST /v1/embeddings` |
| 模型列表 | `GET /v1/models` |
| 聊天上游协议 | OpenAI Chat、Anthropic Messages、OpenAI Responses、Ollama Chat |
| Embedding 上游协议 | OpenAI Embeddings、Ollama Embeddings |

- 支持 OpenAI Chat、Anthropic Messages、OpenAI Responses 三种入站协议之间的请求、响应与流式转换。
- 支持系统提示、工具调用、主流多模态输入和 reasoning/thinking 等常见能力。
- 优先选择与入站协议一致的上游协议，减少不必要的转换；Provider 也可为不同协议配置独立 endpoint。
- Ollama 当前仅作为上游协议，不提供 Ollama 原生入站 API。

### 模型与调度

- 模型别名重定向和三层模型命名：用户模型名 → 模型组/别名 → `provider/upstream_model`。
- 模型组支持嵌套引用，以及 `public`、`hidden`、`internal` 三种暴露规则。
- 四种调度模式：
  - `concurrent`：并发竞速，首个成功结果胜出。
  - `load-balance`：按权重或轮询分流。
  - `failover`：按顺序失败回退。
  - `adaptive`：基于历史首 token 延迟选择候选。
- 两层 QPM 限流：provider 账号级（`providers.*.rate_limit.qpm`，全家共用一桶）叠加 rules 的 per-model 限流（`qpm` action，按 `provider/upstream_model` 建桶），以及请求失败驱动的健康状态、冷却恢复。
- rules 规则十类 action：protocol / effort / effort_mode / temperature_mode / thinking / max_tokens 改写请求，qpm / enable_time_range / disable_time_range / retries 控制调度（per-model QPM、每日可用时段与叶子瞬时失败重试都通过 rules 配置，旧 `providers.*.upstream_model` 与 `disabled_time_ranges` 已废弃）。
- 支持本地 Ollama、OpenAI 兼容服务，以及 OpenAI 兼容 Embeddings。

### 运维与管理

- 全局请求、连接、首 token、流式空闲超时。
- Prometheus 指标：请求数、延迟、首 token 延迟、Token、Provider 尝试结果、限流与健康状态等。
- SQLite 调用统计，可按 Key、Provider、模型和日期查询。
- 可选 Admin Web UI：维护 Provider、模型组、别名、入口 Key；通过草稿校验后应用运行时配置。
- 可选上游模型目录探测与 Admin UI 自动补全。
- 可选 Smart Route：为显式启用的模型别名选择 scout、judge 或 reasoning 路径。

## 快速开始

### 1. 准备配置

复制示例配置并替换占位密钥：

```bash
cp config.example.yaml config.yaml
```

至少需要配置：

- `inbound.auth.keys`
- `providers.<name>.api_key`（纯 Ollama、部分本地代理和 OAuth Bridge 场景可为空）
- `model_groups`

完整字段、协议和模型组示例见 [config.example.yaml](./config.example.yaml)。不要提交包含真实密钥的 `config.yaml`。

### 2. 本地运行

```bash
go run ./cmd/oh-my-api -config config.yaml serve
```

默认监听地址为 `:18000`。

### 3. 调用接口

```bash
curl http://127.0.0.1:18000/v1/chat/completions \
  -H 'Authorization: Bearer <gateway-api-key>' \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "gpt-4o-balanced",
    "messages": [{"role": "user", "content": "你好"}]
  }'
```

客户端请求的 `model` 是模型组名或公开别名，而不是必须直接暴露上游的真实模型名。

### 4. 常用命令

```bash
# 启动服务
oh-my-api --config config.yaml serve

# 测试 provider/model 连通性
oh-my-api --config config.yaml test openai/gpt-4o

# 查看本地统计
oh-my-api --config config.yaml stats
oh-my-api --config config.yaml stats --today
oh-my-api --config config.yaml stats --since "2026-04-01" --until "2026-04-25"
```

## 自托管建议

建议将网关部署在受控网络中，或放在支持 TLS 和访问控制的反向代理之后。

- 入口 API Key 仅支持 `Authorization: Bearer` 或 `x-api-key`；不要把密钥放入 URL。
- `config.yaml`、SQLite 数据库和 Bridge OAuth 状态均包含敏感数据，应限制文件权限、定期备份且不提交到版本控制。
- 生产环境 `server.metrics_listen` 需对 Docker 容器网络可达（例如 `:9090`），由共享 Prometheus 在内网抓取；不要把 metrics 端口映射到公网，也不要占用宿主常见冲突口（如 `9090` 常被其他组件占用）。
- 启用 Admin UI 时应设置强密码，并只向受信任网络暴露 `/admin`。
- 当前主网关尚未提供 `/livez` 或 `/readyz`；部署自动化应在升级后执行鉴权和模型请求冒烟测试。

生产部署模板见 [`deploy/`](./deploy/)。Prometheus + Grafana 建议作为独立的基础设施部署，不随应用 compose 启动，通过 Docker 网络接入。

## 协议兼容边界

主流文本对话、流式响应、工具调用和常见多模态请求已覆盖，但协议转换不承诺对所有厂商扩展字段无损保留。尤其应注意：

- 部分 OpenAI Responses 特有字段，以及多次协议转换中的 reasoning/thinking 信息，可能降级或不被支持。
- 跨协议流式路径的最终 usage 和少数工具调用细节仍在完善中。
- 即使入站与上游协议相同，请求也会经过结构化解析以重写模型名；未来或厂商私有字段可能无法完整保留。

## 监控与统计

配置 `server.metrics_listen` 后，服务会在独立端口暴露 Prometheus 指标，例如：

- `request_total`
- `request_duration_seconds`
- `request_first_token_seconds`
- `token_input_total` / `token_output_total`
- `provider_health_status`
- `provider_attempt_total`
- `concurrent_requests`

调用统计默认写入 SQLite，可通过 `oh-my-api stats` 查询。Grafana dashboard 样例位于 [`deploy/grafana/`](./deploy/grafana/)。

## OAuth Bridge (`oh-my-api-bridge`)

`oh-my-api-bridge` 是独立运行的本地桥接服务，用于让云端 oh-my-api 经由内网机器使用本地 OAuth 凭证。当前支持 xAI OAuth：设备码登录、access token 刷新、OpenAI Responses/Chat 请求转发和模型列表代理。

Bridge 不依赖 Hermes 的安装、认证文件或 credential pool；OAuth 状态由 Bridge 自行维护，默认存放于 `~/.oh-my-api/bridge/auth.json`，目录权限为 `0700`、文件权限为 `0600`。

### 登录

```bash
# 先启动 bridge 服务，再完成设备码授权
oh-my-api-bridge auth login
```

### 启动

```bash
oh-my-api-bridge -bridge-token <bridge-token> [-listen :8081]
```

| 参数 | 说明 |
|------|------|
| `-bridge-token` | Bridge 鉴权使用的固定 Bearer Token，必填 |
| `-listen` | 监听地址，默认 `:8081` |

| 方法 | 路径 | 说明 |
|------|------|------|
| POST | `/v1/responses` | 转发 OpenAI Responses 请求到 xAI |
| POST | `/v1/chat/completions` | 转发 OpenAI Chat Completions 请求到 xAI |
| GET | `/v1/models` | 透传 xAI 模型目录，供主网关进一步处理 |
| GET | `/healthz` | 返回 Bridge 进程存活和 OAuth 状态可读性 |

Bridge 请求需要：

- `Authorization: Bearer <bridge-token>`
- `X-Oh-My-API-Bridge-Provider: xai-oauth`
- `Content-Type: application/json`

主网关通过 `remote_bridge` Provider 配置使用 Bridge，示例见 [config.example.yaml](./config.example.yaml)。

## Cascade Hub-Spoke（可选）

Cascade 让内网开发机（spoke）通过**出站 WSS** 连上云上网关（hub），把内网可调用模型同步给 hub 调度。这是**应用层 job 会话**，不是 Tailscale、frp 或通用 TCP 隧道；spoke 只需能访问 hub 的 **HTTPS/WSS（通常 443）**。

- Hub：在 `providers` 里配置 `cascade.enabled` provider（空 `endpoint`，三种协议 + 共享 `token`），路由 `GET /cascade` 始终注册（未配置时返回 503）。
- Spoke：顶层 `cascade` 块填写 `hub` origin（`https://host[:port]`，不要写 `/cascade` 或 `ws(s)://`）、`token`、`peer`（已有 provider 名，job 时跳过防环）。
- **模型授权由 exposure 决定，无 offer 白名单**：spoke 注册后会把全部 `public` 与 `hidden` 的 model_group / redirect（含按 `/v1/models` 同一规则计算的可选 `context_length`）通过会话自动同步给 hub；`internal` 一律不可被 hub 调用。历史 YAML 中的 `cascade.offer` 会在加载与 Admin Apply 时被硬拒绝。
- **Hub API 面仍由 Hub 配置决定**：同步元数据只用于 Hub 已显式配置的 `cascade-provider/model` 叶子上下文推导（`/v1/models` 的 `context_length`），不会自动创建 model group、redirect 或 `/v1/models` 条目；group 显式 `model_metadata.context_length` 始终优先。
- Admin Apply 后会关闭旧 hub 会话并让 spoke 自动重连，重连后重新发送完整快照。

若前面有 Caddy/nginx 反代 `/cascade`，请勿把 WebSocket **空闲超时**设得过短（建议 ≥ 几分钟），否则长连接会被中间层提前断开。

示例见 [config.example.yaml](./config.example.yaml) 末尾注释块。

## 构建、测试与发布

```bash
# 默认构建 linux/amd64 的 oh-my-api 与 oh-my-api-bridge
./build.sh

# 指定平台
./build.sh darwin arm64

# 全量测试
go test ./...

# 单包测试
go test -v ./internal/codec/
```

构建产物输出到 `bin/`。GitHub Actions 会在 push/PR 时运行 `go test ./...`；推送 `v*` tag 时构建并发布多平台 `oh-my-api` 二进制。根目录 [Dockerfile](./Dockerfile) 可用于构建容器镜像。

## License

[MIT](./LICENSE)
