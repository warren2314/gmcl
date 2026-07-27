#!/usr/bin/env bash
set -euo pipefail

APP_DIR="${APP_DIR:-/opt/gmcl}"
REQUEST_TIMEOUT="${REQUEST_TIMEOUT:-20}"

fail() {
    printf '[portal-preflight] ERROR: %s\n' "$*" >&2
    exit 1
}

command -v docker >/dev/null || fail "docker is unavailable"
command -v curl >/dev/null || fail "curl is unavailable"

cd "${APP_DIR}"
[[ -f .env ]] || fail ".env is missing from ${APP_DIR}"
docker compose version >/dev/null || fail "Docker Compose is unavailable"

docker compose ps
docker compose exec -T app /bin/portal-preflight -mode pilot

base_url="$(
    docker compose exec -T app sh -lc 'printf %s "$PUBLIC_BASE_URL"'
)"
[[ "${base_url}" == https://* ]] || fail "PUBLIC_BASE_URL is not HTTPS"
base_url="${base_url%/}"

health_status="$(
    curl --silent --show-error --output /dev/null \
        --max-time "${REQUEST_TIMEOUT}" \
        --write-out '%{http_code}' \
        "${base_url}/health"
)"
[[ "${health_status}" == "200" ]] || fail "health returned HTTP ${health_status}"

legacy_status="$(
    curl --silent --show-error --output /dev/null \
        --max-time "${REQUEST_TIMEOUT}" \
        --write-out '%{http_code}' \
        "${base_url}/"
)"
[[ "${legacy_status}" == "200" ]] || fail "legacy entry page returned HTTP ${legacy_status}"

headers_file="$(mktemp)"
trap 'rm -f "${headers_file}"' EXIT
portal_status="$(
    curl --silent --show-error --output /dev/null \
        --dump-header "${headers_file}" \
        --max-time "${REQUEST_TIMEOUT}" \
        --write-out '%{http_code}' \
        "${base_url}/portal"
)"
[[ "${portal_status}" == "303" ]] || fail "unauthenticated portal returned HTTP ${portal_status}"
grep -qi '^Cache-Control:.*no-store' "${headers_file}" ||
    fail "portal response omitted Cache-Control: no-store"
grep -qi '^Pragma:.*no-cache' "${headers_file}" ||
    fail "portal response omitted Pragma: no-cache"

worker_status="$(
    curl --silent --show-error --output /dev/null \
        --request POST \
        --header 'Content-Type: application/json' \
        --data '{}' \
        --max-time "${REQUEST_TIMEOUT}" \
        --write-out '%{http_code}' \
        "${base_url}/internal/process-portal-notifications"
)"
[[ "${worker_status}" == "401" ]] ||
    fail "unauthenticated notification worker returned HTTP ${worker_status}"

printf 'portal_preflight=ready\n'
printf 'health_status=%s\n' "${health_status}"
printf 'legacy_status=%s\n' "${legacy_status}"
printf 'portal_status=%s\n' "${portal_status}"
printf 'worker_unauthenticated_status=%s\n' "${worker_status}"
