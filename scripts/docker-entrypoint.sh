#!/bin/sh
set -eu

/app/oh-my-api-bridge \
  -listen=127.0.0.1:8081 \
  -allow-unauthenticated-loopback &

exec /app/oh-my-api "$@"
