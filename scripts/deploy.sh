#!/usr/bin/env bash
# deploy.sh — Pull latest code and roll out a new build with zero-downtime restart.
#
# Run on the droplet as the deploy user (or root):
#   cd /opt/gmcl && bash scripts/deploy.sh
#
# Or trigger remotely from CI / your local machine:
#   ssh deploy@YOUR_DROPLET_IP "cd /opt/gmcl && bash scripts/deploy.sh"
#
# What it does:
#   1. Validates that .env exists and has no un-filled CHANGE_ME values
#   2. Pulls latest code from the current git branch
#   3. Builds new Docker images and restarts containers (rolling, DB kept up)
#   4. Waits for the health endpoint to respond
#   5. Cleans up old dangling images

set -euo pipefail

APP_DIR="${APP_DIR:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
HEALTH_URL="${HEALTH_URL:-http://localhost/health}"
HEALTH_TIMEOUT=120   # seconds to wait for /health to respond after restart

RED='\033[0;31m'; YELLOW='\033[1;33m'; GREEN='\033[0;32m'; BLUE='\033[0;34m'; NC='\033[0m'
info()    { echo -e "${GREEN}[deploy]${NC} $*"; }
section() { echo -e "\n${BLUE}▶ $*${NC}"; }
warn()    { echo -e "${YELLOW}[warn]${NC}  $*"; }
error()   { echo -e "${RED}[error]${NC} $*"; exit 1; }

cd "${APP_DIR}"

# ── Pre-flight checks ────────────────────────────────────────────────────────
section "Pre-flight checks"

[[ -f .env ]] || error ".env not found at ${APP_DIR}/.env — run setup-server.sh first."

# Warn if any CHANGE_ME placeholders remain
if grep -q "CHANGE_ME" .env; then
    error ".env still contains CHANGE_ME placeholders. Fill them in before deploying."
fi

command -v docker &>/dev/null || error "Docker not installed."
docker compose version &>/dev/null || error "Docker Compose plugin not installed."

info "Checks passed."

# ── Git pull ─────────────────────────────────────────────────────────────────
section "Pulling latest code"

BRANCH=$(git rev-parse --abbrev-ref HEAD)
info "Branch: ${BRANCH}"

# Stash any local changes (shouldn't be any in prod, but guard against accidental edits)
if ! git diff --quiet; then
    warn "Uncommitted local changes detected — stashing before pull."
    git stash push -m "deploy-stash-$(date +%s)"
fi

git fetch origin
git reset --hard "origin/${BRANCH}"

COMMIT=$(git log -1 --format="%h  %s  (%cr)" HEAD)
info "Deploying commit: ${COMMIT}"

# ── Build & restart ──────────────────────────────────────────────────────────
section "Building images and restarting containers"

# Keep the DB running — only rebuild app + caddy
docker compose up -d --build --no-deps --remove-orphans app caddy

info "Containers restarted."

# ── Health check ─────────────────────────────────────────────────────────────
section "Waiting for health check at ${HEALTH_URL}"

ELAPSED=0
until curl -sf "${HEALTH_URL}" &>/dev/null; do
    if [[ ${ELAPSED} -ge ${HEALTH_TIMEOUT} ]]; then
        error "Health check timed out after ${HEALTH_TIMEOUT}s. Check: docker compose logs app"
    fi
    echo -n "."
    sleep 3
    ELAPSED=$((ELAPSED + 3))
done
echo ""
info "Health check passed (${ELAPSED}s)."

# ── Cleanup ──────────────────────────────────────────────────────────────────
section "Cleaning up dangling images"
docker image prune -f --filter "dangling=true" || true

# ── Done ─────────────────────────────────────────────────────────────────────
echo ""
echo -e "${GREEN}════════════════════════════════════════════════════════${NC}"
echo -e "${GREEN}  Deployment complete!  ${COMMIT}${NC}"
echo -e "${GREEN}════════════════════════════════════════════════════════${NC}"
echo ""
echo "  Useful commands:"
echo "  docker compose logs -f app     — live app logs"
echo "  docker compose logs -f caddy   — live Caddy logs"
echo "  docker compose ps              — container status"
echo ""
