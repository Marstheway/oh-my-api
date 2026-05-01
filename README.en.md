# oh-my-api

A personal AI API gateway built with Go + Gin, focused on developer experience.

It supports three inbound protocols and routes requests to different providers based on config, with concurrent race, load balance, and failover scheduling modes.

## Features

- OpenAI Chat: `/v1/chat/completions`
- Anthropic Messages: `/v1/messages`
- OpenAI Responses: `/v1/responses`
- Provider scheduling modes: `concurrent` / `load-balance` / `failover`
- Model alias redirection (`redirect`)
- API key authentication (Bearer / `x-api-key` / query param)
- Prometheus metrics export
- SQLite usage statistics

## Core Value

- Decouples inbound protocol from provider protocol, with automatic conversion between openai.chat/openai.responses/anthropic.
- Scheduling modes for real-world scenarios:
  - concurrent: race multiple free models to reduce latency
  - load-balance: aggregate quota across multiple providers
  - failover: handle unstable vendors automatically

## Quick Start

### 1. Prepare config

Copy the example config and replace keys:

```bash
cp config.example.yaml config.yaml
```

At minimum, configure:

- `inbound.auth.keys`
- `providers.<name>.api_key`
- `model_groups`

### 2. Run locally

```bash
go run ./cmd/oh-my-api -config config.yaml serve
```

Default listen address: `0.0.0.0:18000`

### 3. Common commands

```bash
# Start server
oh-my-api --config config.yaml serve

# Test provider/model connectivity
oh-my-api --config config.yaml test openai/gpt-4o

# View stats
oh-my-api --config config.yaml stats
oh-my-api --config config.yaml stats --today
oh-my-api --config config.yaml stats --since "2026-04-01" --until "2026-04-25"
```

## Build

```bash
# Default: linux amd64
./build.sh

# Specify platform
./build.sh darwin arm64
```

Artifacts are output to `bin/`.

## Release

- GitHub repository: `https://github.com/Marstheway/oh-my-api`
- CI: push and PR run `go test ./...`
- Release: pushing a tag (for example `v0.1.0`) triggers GitHub Actions to build and upload multi-platform binaries
- Container: pushing a tag also publishes an image to GHCR: `ghcr.io/marstheway/oh-my-api`

### Container Usage

Package URL: `https://github.com/Marstheway/oh-my-api/pkgs/container/oh-my-api`

```bash
# Pull latest
docker pull ghcr.io/marstheway/oh-my-api:latest

# Run container (mount config file)
docker run --rm -p 18000:18000 -p 9090:9090 \
  -v $(pwd)/config.yaml:/app/config.docker.yaml:ro \
  ghcr.io/marstheway/oh-my-api:latest
```

## API Compatibility

- OpenAI Chat API: `POST /v1/chat/completions`
- Anthropic Messages API: `POST /v1/messages`
- OpenAI Responses API: `POST /v1/responses`
- Model list: `GET /v1/models`

## Development and Testing

```bash
# Run all tests
go test ./...

# Run a single package
go test -v ./internal/codec/
```

## Monitoring

After setting `server.metrics_listen`, Prometheus metrics are exposed, including:

- `request_total`
- `request_duration_seconds`
- `token_input_total`
- `token_output_total`
- `provider_health_status`
- `concurrent_requests`

## Deployment

- You can build an image directly with the root [Dockerfile](./Dockerfile)
- You can also use the GHCR image directly: `ghcr.io/marstheway/oh-my-api:latest`

## Configuration

See full example: [config.example.yaml](./config.example.yaml)

## License

[MIT](./LICENSE)
