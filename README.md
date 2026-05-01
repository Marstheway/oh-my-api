# oh-my-api

一个用 Go + Gin 实现的个人 AI API 网关，目标是体验优先。

它支持三种入方向协议，并按配置将请求路由到不同 Provider，支持并发竞速、负载均衡、故障转移三种调度模式。

## 功能特性

- 支持 OpenAI Chat：`/v1/chat/completions`
- 支持 Anthropic Messages：`/v1/messages`
- 支持 OpenAI Responses：`/v1/responses`
- Provider 调度模式：`concurrent` / `load-balance` / `failover`
- 模型别名重定向（`redirect`）
- API Key 鉴权（Bearer / `x-api-key` / query 参数）
- Prometheus 指标导出
- SQLite 调用统计

## 快速开始

### 1. 准备配置

复制示例配置并替换密钥：

```bash
cp config.example.yaml config.yaml
```

需要至少配置：

- `inbound.auth.keys`
- `providers.<name>.api_key`
- `model_groups`

### 2. 本地运行

```bash
go run ./cmd/oh-my-api -config config.yaml serve
```

默认监听：`0.0.0.0:18000`

### 3. 常用命令

```bash
# 启动服务
oh-my-api --config config.yaml serve

# 测试 provider/model 连通性
oh-my-api --config config.yaml test openai/gpt-4o

# 查看统计
oh-my-api --config config.yaml stats
oh-my-api --config config.yaml stats --today
oh-my-api --config config.yaml stats --since "2026-04-01" --until "2026-04-25"
```

## 构建

```bash
# 默认 linux amd64
./build.sh

# 指定平台
./build.sh darwin arm64
```

产物输出到 `bin/`。

## 发布

- GitHub 仓库：`https://github.com/Marstheway/oh-my-api`
- CI：推送和 PR 会自动执行 `go test ./...`
- Release：推送 tag（例如 `v0.1.0`）后，GitHub Actions 会自动构建并上传多平台二进制
- 容器：根目录提供 [Dockerfile](./Dockerfile)，可直接用于镜像构建

## 接口兼容说明

- OpenAI Chat 接口：`POST /v1/chat/completions`
- Anthropic Messages 接口：`POST /v1/messages`
- OpenAI Responses 接口：`POST /v1/responses`
- 模型列表：`GET /v1/models`

## 开发与测试

```bash
# 全量测试
go test ./...

# 单包测试
go test -v ./internal/codec/
```

## 监控

配置 `server.metrics_listen` 后，会暴露 Prometheus 指标，例如：

- `request_total`
- `request_duration_seconds`
- `token_input_total`
- `token_output_total`
- `provider_health_status`
- `concurrent_requests`

## 部署

- 可直接使用根目录 [Dockerfile](./Dockerfile) 构建镜像
- 发布到 GitHub 时，建议通过 Release 附带各平台二进制

## 配置参考

完整示例请看：[config.example.yaml](./config.example.yaml)

## License

[MIT](./LICENSE)
