FROM golang:1.25.7-bookworm AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
  -trimpath \
  -ldflags="-s -w" \
  -o /out/oh-my-api \
  ./cmd/oh-my-api \
  && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -trimpath \
    -ldflags="-s -w" \
    -o /out/oh-my-api-bridge \
    ./cmd/oh-my-api-bridge

FROM alpine:3.22 AS runtime

# tzdata 提供 zoneinfo，配合 TZ 让 time.Now().Local() 按目标时区计算
# （rules 的 enable_time_range / disable_time_range 等按本地墙钟生效）
RUN apk add --no-cache ca-certificates tzdata \
  && mkdir -p /tmp

ENV TZ=Asia/Shanghai

WORKDIR /app

COPY --from=builder /out/oh-my-api /app/oh-my-api
COPY --from=builder /out/oh-my-api-bridge /app/oh-my-api-bridge
COPY scripts/docker-entrypoint.sh /app/docker-entrypoint.sh

RUN chmod 755 /app/docker-entrypoint.sh \
  && mkdir -p /data/bridge

EXPOSE 18000 9090

ENTRYPOINT ["/app/docker-entrypoint.sh"]
CMD ["--config", "/app/config.yaml", "serve"]
