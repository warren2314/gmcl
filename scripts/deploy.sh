#!/usr/bin/env bash
# Deploy an exact tested commit and automatically restore the previous app
# version if build, container health, or end-to-end health checks fail.

set -Eeuo pipefail

APP_DIR="${APP_DIR:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
HEALTH_URL="${HEALTH_URL:-http://localhost/health}"
HEALTH_TIMEOUT="${HEALTH_TIMEOUT:-120}"
DEPLOY_COMMIT="${DEPLOY_COMMIT:-origin/master}"

PREVIOUS_COMMIT=""
TARGET_COMMIT=""
DEPLOY_STARTED=0
CADDY_CHANGED=0
ROLLBACK_ACTIVE=0

log() {
    printf '[deploy] %s\n' "$*"
}

fail() {
    printf '[deploy] ERROR: %s\n' "$*" >&2
    return 1
}

wait_for_service() {
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
                log "${service} is ${status}"
                return 0
            fi
        fi
        sleep 3
        elapsed=$((elapsed + 3))
    done

    docker compose ps "${service}" || true
    docker compose logs --tail=100 "${service}" || true
    fail "${service} did not become healthy within ${timeout}s"
}

restore_previous_release() {
    local failed_status=$?
    trap - ERR

    if [[ "${ROLLBACK_ACTIVE}" == "1" || "${DEPLOY_STARTED}" != "1" || -z "${PREVIOUS_COMMIT}" ]]; then
        exit "${failed_status}"
    fi

    ROLLBACK_ACTIVE=1
    printf '[deploy] Deployment failed; restoring %s\n' "${PREVIOUS_COMMIT}" >&2

    if git switch --detach "${PREVIOUS_COMMIT}" \
        && docker compose build app \
        && docker compose up -d --no-deps app \
        && wait_for_service app "${HEALTH_TIMEOUT}"; then
        if [[ "${CADDY_CHANGED}" == "1" ]]; then
            docker compose up -d --build --no-deps caddy || true
        fi
        if curl --fail --silent --show-error --max-time 15 "${HEALTH_URL}" >/dev/null; then
            printf '[deploy] Rollback succeeded; production is serving %s\n' "${PREVIOUS_COMMIT}" >&2
        else
            printf '[deploy] Rollback container is healthy, but end-to-end health still fails\n' >&2
        fi
    else
        printf '[deploy] Automatic rollback failed; manual intervention is required\n' >&2
    fi

    exit "${failed_status}"
}

trap restore_previous_release ERR

cd "${APP_DIR}"
APP_DIR="$(pwd -P)"
BACKUP_DIR="${APP_DIR}/backups"

[[ -f .env ]] || fail ".env is missing from ${APP_DIR}"
command -v git >/dev/null || fail "git is not installed"
command -v docker >/dev/null || fail "docker is not installed"
command -v curl >/dev/null || fail "curl is not installed"
docker compose version >/dev/null || fail "Docker Compose is unavailable"

if grep -q 'CHANGE_ME' .env; then
    fail ".env still contains CHANGE_ME placeholders"
fi
if ! git diff --quiet || ! git diff --cached --quiet; then
    fail "production checkout contains uncommitted tracked changes"
fi

log "Fetching deployment target"
git fetch --prune origin master
git cat-file -e "${DEPLOY_COMMIT}^{commit}" || fail "${DEPLOY_COMMIT} is not a fetched commit"

PREVIOUS_COMMIT="$(git rev-parse HEAD)"
TARGET_COMMIT="$(git rev-parse "${DEPLOY_COMMIT}^{commit}")"

if ! git merge-base --is-ancestor "${TARGET_COMMIT}" origin/master; then
    fail "${TARGET_COMMIT} is not contained in origin/master"
fi

if ! git diff --quiet "${PREVIOUS_COMMIT}" "${TARGET_COMMIT}" -- Caddyfile docker/Dockerfile.caddy; then
    CADDY_CHANGED=1
fi

log "Creating pre-deploy database backup"
install -d -m 750 "${BACKUP_DIR}"
backup_file="${BACKUP_DIR}/gmcl-$(date -u +%Y%m%dT%H%M%SZ)-${TARGET_COMMIT:0:12}.dump"
docker compose up -d --no-deps db
wait_for_service db 60
docker compose exec -T db sh -lc 'pg_dump -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Fc' > "${backup_file}"
test -s "${backup_file}" || fail "database backup is empty"
sha256sum "${backup_file}" > "${backup_file}.sha256"
find "${BACKUP_DIR}" -maxdepth 1 -type f -name 'gmcl-*.dump*' -mtime +14 -delete
log "Backup written to ${backup_file}"

log "Switching checkout to tested commit ${TARGET_COMMIT}"
git switch --detach "${TARGET_COMMIT}"
DEPLOY_STARTED=1

log "Building and starting application"
docker compose build app
docker compose up -d --no-deps app
wait_for_service app "${HEALTH_TIMEOUT}"

if [[ "${CADDY_CHANGED}" == "1" ]]; then
    log "Caddy configuration changed; rebuilding proxy"
    docker compose up -d --build --no-deps caddy
fi

log "Checking end-to-end health"
curl --fail --silent --show-error --retry 5 --retry-delay 3 --max-time 15 "${HEALTH_URL}" >/dev/null

printf '%s\n' "${TARGET_COMMIT}" > "${APP_DIR}/.last-successful-deploy"
docker image prune -f --filter 'dangling=true' --filter 'until=168h' >/dev/null || true
log "Deployment completed successfully at ${TARGET_COMMIT}"
