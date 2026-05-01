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
  ./cmd/oh-my-api

FROM alpine:3.22 AS runtime

RUN apk add --no-cache ca-certificates \
  && mkdir -p /tmp

WORKDIR /app

COPY --from=builder /out/oh-my-api /app/oh-my-api

EXPOSE 18000 9090

ENTRYPOINT ["/app/oh-my-api"]
CMD ["--config", "/app/config.docker.yaml", "serve"]
