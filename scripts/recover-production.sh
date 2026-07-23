#!/usr/bin/env bash
# Conservative recovery used by the scheduled production health workflow.
# It restarts the currently deployed containers; it never changes code or data.

set -Eeuo pipefail

APP_DIR="${APP_DIR:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
HEALTH_URL="${HEALTH_URL:-http://localhost/health}"

cd "${APP_DIR}"

wait_for_health() {
    local service="$1"
    local timeout="$2"
    local elapsed=0
    local container_id
    local status

    while (( elapsed < timeout )); do
        container_id="$(docker compose ps -q "${service}")"
        if [[ -n "${container_id}" ]]; then
            status="$(docker inspect --format='{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' "${container_id}" 2>/dev/null || true)"
            if [[ "${status}" == "healthy" || "${status}" == "running" ]]; then
                return 0
            fi
        fi
        sleep 3
        elapsed=$((elapsed + 3))
    done
    return 1
}

docker compose up -d --no-deps db
wait_for_health db 60

docker compose up -d --no-deps app caddy
docker compose restart app caddy
wait_for_health app 120

curl --fail --silent --show-error --retry 5 --retry-delay 3 --max-time 15 "${HEALTH_URL}" >/dev/null
printf '[recover] Production containers restarted and health checks passed\n'
