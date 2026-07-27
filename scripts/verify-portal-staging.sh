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

headers_dir="$(mktemp -d)"
portal_headers="${headers_dir}/portal.headers"
admin_headers="${headers_dir}/admin.headers"
trap 'rm -rf "${headers_dir}"' EXIT
portal_status="$(
    curl --silent --show-error --output /dev/null \
        --dump-header "${portal_headers}" \
        --max-time "${REQUEST_TIMEOUT}" \
        --write-out '%{http_code}' \
        "${base_url}/portal"
)"
[[ "${portal_status}" == "303" ]] || fail "unauthenticated portal returned HTTP ${portal_status}"
grep -qi '^Location: */portal/login?return_to=%2Fportal' "${portal_headers}" ||
    fail "unauthenticated portal did not redirect to the internal portal login"
grep -qi '^Cache-Control:.*no-store' "${portal_headers}" ||
    fail "portal response omitted Cache-Control: no-store"
grep -qi '^Pragma:.*no-cache' "${portal_headers}" ||
    fail "portal response omitted Pragma: no-cache"
grep -qi '^Content-Security-Policy:.*default-src.*self' "${portal_headers}" ||
    fail "portal response omitted the baseline Content-Security-Policy"
grep -qi '^Content-Security-Policy:.*object-src.*none' "${portal_headers}" ||
    fail "portal Content-Security-Policy did not deny object sources"
grep -qi '^Content-Security-Policy:.*frame-ancestors.*none' "${portal_headers}" ||
    fail "portal Content-Security-Policy did not deny framing"
grep -qi '^Strict-Transport-Security:.*max-age=' "${portal_headers}" ||
    fail "portal response omitted Strict-Transport-Security"
grep -qi '^X-Content-Type-Options: *nosniff' "${portal_headers}" ||
    fail "portal response omitted X-Content-Type-Options: nosniff"
grep -qi '^X-Frame-Options: *DENY' "${portal_headers}" ||
    fail "portal response omitted X-Frame-Options: DENY"

admin_status="$(
    curl --silent --show-error --output /dev/null \
        --dump-header "${admin_headers}" \
        --max-time "${REQUEST_TIMEOUT}" \
        --write-out '%{http_code}' \
        "${base_url}/admin/login"
)"
[[ "${admin_status}" == "200" ]] ||
    fail "legacy administrator login returned HTTP ${admin_status}"
csrf_cookie="$(grep -i '^Set-Cookie: *csrf_token=' "${admin_headers}" | head -n 1 || true)"
[[ -n "${csrf_cookie}" ]] || fail "administrator login did not issue a CSRF cookie"
[[ "${csrf_cookie}" == *"Path=/"* ]] || fail "CSRF cookie did not use Path=/"
[[ "${csrf_cookie}" == *"HttpOnly"* ]] || fail "CSRF cookie omitted HttpOnly"
[[ "${csrf_cookie}" == *"Secure"* ]] || fail "CSRF cookie omitted Secure"
[[ "${csrf_cookie}" == *"SameSite=Lax"* ]] || fail "CSRF cookie omitted SameSite=Lax"
grep -qi '^Cache-Control:.*no-store' "${admin_headers}" ||
    fail "administrator login omitted Cache-Control: no-store"

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
printf 'admin_status=%s\n' "${admin_status}"
printf 'worker_unauthenticated_status=%s\n' "${worker_status}"
