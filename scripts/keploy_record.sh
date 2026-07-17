#!/usr/bin/env bash
# Re-record Keploy test cases + mocks locally, then commit the keploy/ directory.
#
# Keploy records with eBPF (Linux only), so on macOS/Windows this drives the
# recording inside a privileged Linux container via Docker. It:
#   1. builds a Linux binary of the service,
#   2. starts a throwaway PostgreSQL,
#   3. runs `keploy record` (open-source, no API key) while firing sample
#      traffic, capturing HTTP test cases and the Postgres/DNS mocks,
#   4. writes them to ./keploy/ (bind-mounted) for you to commit.
#
# Requirements: Docker + Go. Usage: ./scripts/keploy_record.sh
set -euo pipefail

cd "$(dirname "$0")/.."
REPO="$PWD"
NET="keploy_rec_net"
PG="keploy_rec_pg"
DSN="postgres://postgres@${PG}:5432/postgres?sslmode=disable&pool_max_conns=1&default_query_exec_mode=simple_protocol"

# Match the container architecture (Apple Silicon -> arm64).
case "$(uname -m)" in
  arm64|aarch64) ARCH=arm64 ;;
  *)             ARCH=amd64 ;;
esac

echo ">> building linux/${ARCH} binary"
GOOS=linux GOARCH="$ARCH" CGO_ENABLED=0 go build -o urlshortener.linux ./cmd/urlshortener

cleanup() { docker rm -f "$PG" >/dev/null 2>&1 || true; }
trap cleanup EXIT

docker network create "$NET" >/dev/null 2>&1 || true
docker rm -f "$PG" >/dev/null 2>&1 || true
echo ">> starting postgres"
docker run -d --name "$PG" --network "$NET" -e POSTGRES_HOST_AUTH_METHOD=trust postgres:16 >/dev/null
for _ in $(seq 1 30); do docker exec "$PG" pg_isready -q 2>/dev/null && break; sleep 1; done

echo ">> recording (privileged linux container)"
docker run --rm --network "$NET" --privileged \
  -v "$REPO":/app -w /app \
  -e DATABASE_URL="$DSN" -e PUBLIC_BASE_URL="http://localhost:8080" -e HTTP_ADDR=":8080" \
  ubuntu:22.04 bash -c '
    set -e
    printf "#!/bin/sh\nexec \"\$@\"\n" > /usr/local/bin/sudo && chmod +x /usr/local/bin/sudo
    export DEBIAN_FRONTEND=noninteractive
    apt-get update -qq >/dev/null 2>&1 && apt-get install -y -qq curl ca-certificates >/dev/null 2>&1
    curl --silent -L https://keploy.io/install.sh | bash -s -- --oss >/tmp/i.log 2>&1
    command -v keploy >/dev/null || { echo "keploy install failed"; tail -5 /tmp/i.log; exit 1; }
    rm -rf keploy/
    (
      for _ in $(seq 1 40); do (echo > /dev/tcp/localhost/8080) 2>/dev/null && break; sleep 1; done
      sleep 2
      curl -s -X POST -H "Content-Type: application/json" -d "{\"url\":\"https://example.com/one\"}" http://localhost:8080/shorten >/dev/null
      curl -s -X POST -H "Content-Type: application/json" -d "{\"url\":\"https://example.com/one\"}" http://localhost:8080/shorten >/dev/null
      curl -s -X POST -H "Content-Type: application/json" -d "{\"url\":\"https://example.com/two\",\"custom_alias\":\"docs\"}" http://localhost:8080/shorten >/dev/null
      curl -s -o /dev/null http://localhost:8080/docs
      curl -s -o /dev/null http://localhost:8080/missing
      curl -s -o /dev/null -X POST -H "Content-Type: application/json" -d "{\"url\":\"ftp://nope\"}" http://localhost:8080/shorten
      sleep 6
      kill -INT "$(pgrep -n -f "keploy record")" 2>/dev/null || true
    ) &
    keploy record -c "./urlshortener.linux"
  '

rm -f urlshortener.linux
echo ">> done. Recorded:"
find keploy -type f | sort
echo ">> review and commit the keploy/ directory."
