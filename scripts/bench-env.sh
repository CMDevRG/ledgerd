#!/usr/bin/env bash
# Prints the environment a benchmark ran in, so results can be interpreted (and
# reproduced). Output is plain text, meant to be saved next to the results.
set -euo pipefail
cd "$(dirname "$0")/.."

echo "date:        $(date -u +%Y-%m-%dT%H:%M:%SZ)"
echo "commit:      $(git rev-parse --short HEAD 2>/dev/null || echo unknown)$(git diff --quiet 2>/dev/null || echo ' (uncommitted changes)')"
echo "host OS:     $(uname -sr)"
if [ -r /proc/cpuinfo ]; then
  echo "CPU:         $(grep -m1 'model name' /proc/cpuinfo | cut -d: -f2- | sed 's/^ //') ($(nproc) logical cores visible)"
fi
if [ -r /proc/meminfo ]; then
  echo "memory:      $(awk '/MemTotal/ {printf "%.1f GiB", $2/1024/1024}' /proc/meminfo) visible to this environment"
fi
echo "docker:      $(docker version --format '{{.Server.Version}} ({{.Server.Os}}/{{.Server.Arch}})' 2>/dev/null || echo unavailable)"
echo "docker VM:   $(docker info --format '{{.NCPU}} CPUs, {{.OperatingSystem}}' 2>/dev/null || echo unavailable)"
echo "postgres:    $(docker compose exec -T postgres postgres --version 2>/dev/null | tr -d '\r' || echo unavailable)"
echo "ledgerd:     $(docker compose run --rm --no-deps -T api version 2>/dev/null | tr -d '\r' || echo unavailable)"
echo "db pool:     LEDGERD_DB_MAX_CONNS=${LEDGERD_DB_MAX_CONNS:-20 (default)}"
echo "storage:     PostgreSQL data on a Docker named volume (default fsync/synchronous_commit settings)"
