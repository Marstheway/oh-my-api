# oh-my-api

中文说明: [README.md](./README.md)

oh-my-api is a **self-hosted AI API gateway for individuals and small teams**, built with Go + Gin. It provides a unified entry point for OpenAI, Anthropic, OpenAI Responses and compatible services, and performs model mapping, protocol conversion, scheduling, failover, statistics and monitoring according to configuration.

The project targets individual developers, small teams and agent tooling in controlled environments: while keeping the common client protocols, you can combine multiple cloud or local providers for more stable and flexible model access.

> This project is not a multi-tenant enterprise AI platform: it currently provides no RBAC, SSO, auditing, billing, distributed rate limiting, or cross-instance high-availability control plane.

## Feature Overview

### API and protocols

| Capability | Status |
|------|------|
| OpenAI Chat Completions | `POST /v1/chat/completions` |
| Anthropic Messages | `POST /v1/messages` |
| OpenAI Responses | `POST /v1/responses` |
| OpenAI Embeddings | `POST /v1/embeddings` |
| Model list | `GET /v1/models` |
| Chat upstream protocols | OpenAI Chat, Anthropic Messages, OpenAI Responses, Ollama Chat |
| Embedding upstream protocols | OpenAI Embeddings, Ollama Embeddings |

- Converts requests, responses and streams between three inbound protocols: OpenAI Chat, Anthropic Messages and OpenAI Responses.
- Supports system prompts, tool calls, common multimodal input and reasoning/thinking.
- Prefers the upstream protocol that matches the inbound protocol to avoid unnecessary conversion; a provider can also configure a dedicated endpoint per protocol.
- Ollama is available as an upstream protocol only; there is no native Ollama inbound API.

### Models and scheduling

- Model alias redirection and three-layer model naming: user model name → model group/alias → `provider/upstream_model`.
- Model groups support nested references and three exposure rules: `public`, `hidden` and `internal`.
- Four scheduling modes:
  - `concurrent`: race candidates in parallel, the first success wins.
  - `load-balance`: distribute by weight or round robin.
  - `failover`: fall back in order.
  - `adaptive`: pick candidates based on historical first-token latency.
- Two layers of QPM limiting: the provider account level (`providers.*.rate_limit.qpm`, one shared bucket per provider) plus per-model limiting from rules (the `qpm` action, bucketed by `provider/upstream_model`), together with failure-driven health state and cooldown recovery.
- Ten kinds of rules actions: protocol / effort / effort_mode / temperature_mode / thinking / max_tokens rewrite the request, while qpm / enable_time_range / disable_time_range / retries control scheduling (per-model QPM, daily availability windows and per-leaf transient retries are all configured through rules; the legacy `providers.*.upstream_model` and `disabled_time_ranges` are deprecated).
- Supports local Ollama, OpenAI-compatible services and OpenAI-compatible Embeddings.

### Operations and management

- Global request, connection, first-token and streaming idle timeouts.
- Prometheus metrics: request counts, latency, first-token latency, tokens, provider attempt results, rate limiting and health state.
- SQLite usage statistics, queryable by key, provider, model and date.
- Optional Admin Web UI: manage providers, model groups, aliases and inbound keys; apply runtime config after draft validation.
- Optional upstream model catalog probing and Admin UI autocomplete.
- Optional Smart Route: choose the scout, judge or reasoning path for explicitly enabled model aliases.

## Quick Start

### 1. Prepare config

Copy the example config and replace the placeholder keys:

```bash
cp config.example.yaml config.yaml
```

At a minimum, configure:

- `inbound.auth.keys`
- `providers.<name>.api_key` (may be empty for pure Ollama, some local proxies and the OAuth Bridge)
- `model_groups`

See [config.example.yaml](./config.example.yaml) for the full field list, protocol and model group examples. Do not commit a `config.yaml` containing real keys.

### 2. Run locally

```bash
go run ./cmd/oh-my-api -config config.yaml serve
```

The default listen address is `:18000`.

### 3. Call the API

```bash
curl http://127.0.0.1:18000/v1/chat/completions \
  -H 'Authorization: Bearer <gateway-api-key>' \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "gpt-4o-balanced",
    "messages": [{"role": "user", "content": "hello"}]
  }'
```

The `model` a client sends is a model group name or a public alias; upstream model names do not have to be exposed directly.

### 4. Common commands

```bash
# Start the server
oh-my-api --config config.yaml serve

# Test provider/model connectivity
oh-my-api --config config.yaml test openai/gpt-4o

# View local statistics
oh-my-api --config config.yaml stats
oh-my-api --config config.yaml stats --today
oh-my-api --config config.yaml stats --since "2026-04-01" --until "2026-04-25"
```

## Self-hosting notes

Deploy the gateway in a controlled network, or behind a reverse proxy that provides TLS and access control.

- The inbound API key only accepts `Authorization: Bearer` or `x-api-key`; never put keys in the URL.
- `config.yaml`, the SQLite database and the Bridge OAuth state all contain sensitive data: restrict file permissions, back them up regularly and keep them out of version control.
- In production, `server.metrics_listen` must be reachable from the Docker container network (for example `:9090`) so that Prometheus can scrape it; do not expose the metrics port to the public internet, and avoid ports that commonly conflict on the host (`9090` is often taken by other components).
- When the Admin UI is enabled, set a strong password and expose `/admin` only to trusted networks.
- The main gateway does not provide `/livez` or `/readyz` yet; deployment automation should run authentication and model request smoke tests after an upgrade.

Deployment templates live in [`deploy/`](./deploy/). Prometheus + Grafana are meant to be deployed as separate infrastructure rather than started by the application compose file, and are joined over a Docker network.

## Protocol compatibility boundaries

Mainstream text chat, streaming responses, tool calls and common multimodal requests are covered, but protocol conversion does not guarantee lossless preservation of every vendor extension. In particular:

- Some OpenAI Responses specific fields, and reasoning/thinking information across repeated protocol conversions, may be degraded or unsupported.
- Final usage and a few tool call details on cross-protocol streaming paths are still being refined.
- Even when the inbound and upstream protocols match, requests are parsed structurally to rewrite the model name; future or vendor-private fields may not be preserved completely.

## Monitoring and statistics

With `server.metrics_listen` configured, the service exposes Prometheus metrics on a separate port, for example:

- `request_total`
- `request_duration_seconds`
- `request_first_token_seconds`
- `token_input_total` / `token_output_total`
- `provider_health_status`
- `provider_attempt_total`
- `concurrent_requests`

Usage statistics are written to SQLite by default and can be queried with `oh-my-api stats`. A sample Grafana dashboard is available in [`deploy/grafana/`](./deploy/grafana/).

## OAuth Bridge (`oh-my-api-bridge`)

`oh-my-api-bridge` is a standalone local bridge service that lets a cloud-hosted oh-my-api use local OAuth credentials through a private machine. It currently supports xAI OAuth: device-code login, access token refresh, OpenAI Responses/Chat request forwarding and model list proxying.

The Bridge does not depend on a Hermes installation, auth file or credential pool; it maintains OAuth state itself, by default at `~/.oh-my-api/bridge/auth.json`, with directory permissions `0700` and file permissions `0600`.

### Login

```bash
# Start the bridge service first, then complete device-code authorization
oh-my-api-bridge auth login
```

### Run

```bash
oh-my-api-bridge -bridge-token <bridge-token> [-listen :8081]
```

| Flag | Description |
|------|------|
| `-bridge-token` | Fixed bearer token used to authenticate the Bridge, required |
| `-listen` | Listen address, defaults to `:8081` |

| Method | Path | Description |
|------|------|------|
| POST | `/v1/responses` | Forward OpenAI Responses requests to xAI |
| POST | `/v1/chat/completions` | Forward OpenAI Chat Completions requests to xAI |
| GET | `/v1/models` | Proxy the xAI model catalog for the main gateway to process |
| GET | `/healthz` | Report Bridge liveness and OAuth state readability |

Bridge requests require:

- `Authorization: Bearer <bridge-token>`
- `X-Oh-My-API-Bridge-Provider: xai-oauth`
- `Content-Type: application/json`

The main gateway uses the Bridge through a `remote_bridge` provider; see [config.example.yaml](./config.example.yaml).

## Cascade Hub-Spoke (optional)

Cascade lets an internal-network dev machine (spoke) connect outbound over **WSS** to a cloud gateway (hub) and share its locally reachable models with the hub's scheduler. It is an **application-level job session**, not Tailscale, frp or a generic TCP tunnel; the spoke only needs outbound access to the hub's **HTTPS/WSS (usually 443)**.

- Hub: configure a `cascade.enabled` provider in `providers` (empty `endpoint`, all three protocols plus a shared `token`); the `GET /cascade` route is always registered (it returns 503 when unconfigured).
- Spoke: fill in the top-level `cascade` block with the `hub` origin (`https://host[:port]`, without `/cascade` or `ws(s)://`), `token` and `peer` (an existing provider name, skipped during jobs for loop protection).
- **Model authorization follows exposure; there is no offer allowlist**: once registered, a spoke syncs every `public` and `hidden` model group / redirect (including the optional `context_length` computed by the same rules as `/v1/models`) to the hub over the session; `internal` entries can never be called by the hub. A legacy `cascade.offer` in YAML is hard-rejected at load time and on Admin Apply.
- **The hub API surface is still determined by hub config**: synced metadata only feeds `context_length` derivation for leaves of cascade providers that the hub has already configured explicitly (`/v1/models`); it never creates model groups, redirects or `/v1/models` entries, and an explicit `model_metadata.context_length` on a group always wins.
- After Admin Apply the old hub session is closed and the spoke reconnects automatically, then resends the full snapshot.

If Caddy/nginx reverse-proxies `/cascade`, do not set the WebSocket **idle timeout** too low (a few minutes at least), otherwise the long-lived connection will be cut by the intermediary.

See the comment block at the end of [config.example.yaml](./config.example.yaml) for an example.

## Build, test and release

```bash
# Default: build oh-my-api and oh-my-api-bridge for linux/amd64
./build.sh

# Specify a platform
./build.sh darwin arm64

# Run all tests
go test ./...

# Run a single package
go test -v ./internal/codec/
```

Artifacts are written to `bin/`. GitHub Actions runs `go test ./...` on every push and pull request; pushing a `v*` tag builds and publishes multi-platform `oh-my-api` and `oh-my-api-bridge` binaries. The root [Dockerfile](./Dockerfile) can be used to build a container image.

### Container image

Pushing a tag also publishes an image to GHCR: `ghcr.io/marstheway/oh-my-api`.

```bash
# Pull the latest image
docker pull ghcr.io/marstheway/oh-my-api:latest

# Run the container (mount a config file)
docker run --rm -p 18000:18000 -p 9090:9090 \
  -v "$(pwd)/config.yaml:/app/config.yaml:ro" \
  ghcr.io/marstheway/oh-my-api:latest
```

## License

[MIT](./LICENSE)
