# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## 项目概述

oh-my-api 是一个用 Go + Gin 实现的个人 AI API 网关，核心目标是体验优先。它支持三种入方向协议：OpenAI Chat (`/v1/chat/completions`)、Anthropic Messages (`/v1/messages`)、OpenAI Response (`/v1/responses`)，出方向按配置路由到不同 Provider，支持并发竞速、负载均衡、故障转移三种调度模式。

## 构建与开发命令

```bash
# 构建（默认 linux amd64），产物输出到 bin/
./build.sh
# 指定平台
./build.sh darwin arm64

# 部署到远程服务器（基于 Docker 镜像）
./deploy.sh [OS] [ARCH]

# 本地 Docker 开发
cd docker-dev && docker-compose up -d

# 运行
go run ./cmd/oh-my-api -config config.yaml

# 测试
go test ./...
# 运行单个包的测试
go test -v ./internal/codec/
```

## 架构

### 请求处理流程

```
客户端请求 → middleware(auth/log/recovery) → router → handler → model.Resolver → scheduler → provider.Client → 上游 API
```

### 核心模块

| ���块 | 职责 |
|------|------|
| `cmd/oh-my-api` | 入口，加载配置、初始化 resolver/client/handler、启动 server |
| `internal/config` | YAML 配置加载与解析，支持单 model 配置 |
| `internal/model` | `Resolver` — 模型命名三层体系：user_model → internal_name → upstream_model；模型别名重定向 |
| `internal/router` | Gin 路由注册，映射路径到 handler |
| `internal/handler` | HTTP 处理器：chat（OpenAI Chat 格式）、messages（Anthropic 格式）、responses（OpenAI Response 格式）、models（模型列表） |
| `internal/codec` | 协议编解码器：OpenAI Chat ↔ Anthropic Messages ↔ OpenAI Response 三种格式互转，支持请求/响应/流式转换 |
| `internal/adaptor` | 协议适配器入口，基于 codec 实现格式转换逻辑 |
| `internal/middleware` | 认证（API Key，兼容 Bearer/x-api-key/query param）、日志、panic 恢复 |
| `internal/ratelimit` | 限流器，基于令牌桶的请求限流 |
| `internal/scheduler` | 调度器，支持 concurrent、loadbalance、failover 三种模式 |
| `internal/provider` | `Client` — 上游 Provider HTTP 客户端封装，支持多协议直通 |
| `internal/dto` | 数据传输对象（OpenAI Chat/Response 和 Anthropic 的请求/响应结构体） |
| `internal/errors` | 统一错误处理，按入方向协议返回对应格式的错误响应 |
| `internal/server` | HTTP 服务器启动与生命周期管理，支持独立的 metrics 端口 |
| `internal/health` | `Checker` — Provider 健康状态管理，连续失败阈值+冷却恢复机制 |
| `internal/metrics` | Prometheus 指标集成：请求计数、延迟直方图、token 统计、provider 健康状态 |
| `internal/stats` | SQLite 统计存储，Recorder/Querier 接口，按 key/provider/model 维度聚合 |
| `internal/token` | 基于 tiktoken 的本地 token 统计，Estimator + StreamCounter |
| `internal/migrate` | 数据库迁移管理 |

### 模型命名三层体系

```
user_model         → internal_name              → upstream_model
(客户端请求填的)     (provider/upstream_model)    (发给 provider 的真实名)

"gpt-4o"      →   "openai/gpt-4o"          →  "gpt-4o"
                   "openrouter/openai/gpt-4o" →  "openai/gpt-4o"
```

- `internal_name` 取第一个 `/` 前为 provider 名，剩余为 upstream_model
- 同一 user_model 在不同 provider 的 upstream_model 可能不同，所以 upstream_model 跟 provider 绑定

### 模型别名重定向

支持模型别名映射到内部 model_group，用于隐藏内部实现细节：

```yaml
redirect:
  claude-4-6-20261201: claude-fast  # 用户请求别名，实际路由到 claude-fast
```

配合 `visible: false` 可隐藏内部模型名，只通过别名访问。

### 三种调度模式

1. **concurrent** — 并发竞速，首 token 胜出，其余取消
2. **loadbalance** — 按权重/轮询分配流量，配合 health.Checker 自动摘除不健康 provider
3. **failover** — 按顺序尝试，失败自动切下一个

### Provider 健康检查

`health.Checker` 用于 loadbalance 模式：
- 连续失败达到阈值（默认 3 次）后摘除 provider
- 冷却时间（默认 30s）后自动恢复
- Scheduler 在每次请求后上报成功/失败

### Token 统计

- 输入 token：请求时计算
- 输出 token：流式响应累加文本后计算（基于 tiktoken cl100k_base 编码）
- 统计数据存入 SQLite，按日期/key/provider/model 维度聚合

### 多协议直通

Provider 可配置 `protocol: "openai/anthropic"` 表示兼容两种协议，优先使用与 inbound 相同的协议以避免转换开销。

### 协议编解码器 (Codec)

`internal/codec` 模块实现三种协议之间的双向转换：
- **OpenAI Chat** (`openai_chat`) — `/v1/chat/completions` 格式
- **Anthropic Messages** (`anthropic_messages`) — `/v1/messages` 格式
- **OpenAI Response** (`openai_response`) — `/v1/responses` 格式

转换类型：
- 请求转换：`request_x_to_y.go`
- 响应转换：`response_x_to_y.go`
- 流式转换：`stream_map_x_to_y.go`、`stream_write_x.go`

### Prometheus 指标

独立的 metrics 端口（配置 `server.metrics_address`）暴露 Prometheus 指标：
- `request_total` — 按协议/provider/model/key/status 分类的请求计数
- `request_duration_seconds` — 请求延迟直方图
- `token_input_total` / `token_output_total` — Token 消耗追踪
- `provider_health_status` — Provider 健康状态
- `concurrent_requests` — 当前并发请求数

### 部署架构

- `deploy/` — 生产部署配置（Docker Compose + Grafana + Prometheus）
- `docker-dev/` — 本地开发环境配置
- `Dockerfile` — 容器化构建支持

### 配置

配置文件 `config.yaml`（含敏感信息，不要读取），如需了解配置文件，参考 `config.example.yaml`。包含：server 全局设置（含 health_check、metrics_address）、database 配置、inbound 认证与路由、providers 定义、redirect 别名映射、model_groups 调度配置。

## GIT

除非用户明确要求 git add/commit/push，否则不要自动对 git 操作。
