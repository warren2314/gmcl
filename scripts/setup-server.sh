#!/usr/bin/env bash
# setup-server.sh — Provision a fresh Ubuntu 22.04 / 24.04 DigitalOcean droplet for GMCL.
#
# Run once as root (or with sudo) on the droplet:
#   curl -fsSL https://raw.githubusercontent.com/YOUR_ORG/GMCL/main/scripts/setup-server.sh | bash
#   — or — scp scripts/setup-server.sh root@YOUR_DROPLET_IP: && ssh root@YOUR_DROPLET_IP bash setup-server.sh
#
# What it does:
#   1. Updates system packages
#   2. Installs Docker Engine + Docker Compose plugin
#   3. Configures UFW firewall (SSH, HTTP, HTTPS only)
#   4. Creates a non-root deploy user and installs your SSH key
#   5. Clones the repository into /opt/gmcl
#   6. Creates a .env template with all required variables
#   7. Creates a systemd service so the app starts on boot

set -euo pipefail

# ── Config ──────────────────────────────────────────────────────────────────
APP_DIR="/opt/gmcl"
DEPLOY_USER="deploy"
REPO_URL="${REPO_URL:-}"          # Set via env or edit below, e.g. git@github.com:org/gmcl.git
SSH_PUBKEY="${SSH_PUBKEY:-}"      # Optional: paste your public key here to authorise it for deploy user

RED='\033[0;31m'; YELLOW='\033[1;33m'; GREEN='\033[0;32m'; NC='\033[0m'
info()  { echo -e "${GREEN}[setup]${NC} $*"; }
warn()  { echo -e "${YELLOW}[warn]${NC}  $*"; }
error() { echo -e "${RED}[error]${NC} $*"; exit 1; }

# ── Root check ───────────────────────────────────────────────────────────────
[[ $EUID -ne 0 ]] && error "Run this script as root (or with sudo)."

# ── 1. System update ─────────────────────────────────────────────────────────
info "Updating system packages..."
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get upgrade -y -qq
apt-get install -y -qq \
    curl wget git unzip jq \
    ca-certificates gnupg lsb-release \
    ufw fail2ban

# ── 2. Docker Engine ─────────────────────────────────────────────────────────
info "Installing Docker Engine..."
if ! command -v docker &>/dev/null; then
    install -m 0755 -d /etc/apt/keyrings
    curl -fsSL https://download.docker.com/linux/ubuntu/gpg \
        | gpg --dearmor -o /etc/apt/keyrings/docker.gpg
    chmod a+r /etc/apt/keyrings/docker.gpg

    echo \
        "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.gpg] \
https://download.docker.com/linux/ubuntu $(lsb_release -cs) stable" \
        > /etc/apt/sources.list.d/docker.list

    apt-get update -qq
    apt-get install -y -qq docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
else
    info "Docker already installed, skipping."
fi

systemctl enable --now docker
info "Docker $(docker --version) installed."

# ── 3. Firewall ───────────────────────────────────────────────────────────────
info "Configuring UFW firewall..."
ufw --force reset
ufw default deny incoming
ufw default allow outgoing
ufw allow ssh        comment "SSH"
ufw allow 80/tcp     comment "HTTP"
ufw allow 443/tcp    comment "HTTPS"
ufw --force enable
ufw status verbose

# ── 4. Deploy user ───────────────────────────────────────────────────────────
info "Creating deploy user '${DEPLOY_USER}'..."
if ! id "${DEPLOY_USER}" &>/dev/null; then
    adduser --disabled-password --gecos "" "${DEPLOY_USER}"
fi
usermod -aG docker "${DEPLOY_USER}"

# Authorise SSH key if provided
if [[ -n "${SSH_PUBKEY}" ]]; then
    SSH_DIR="/home/${DEPLOY_USER}/.ssh"
    mkdir -p "${SSH_DIR}"
    echo "${SSH_PUBKEY}" >> "${SSH_DIR}/authorized_keys"
    chmod 700 "${SSH_DIR}"
    chmod 600 "${SSH_DIR}/authorized_keys"
    chown -R "${DEPLOY_USER}:${DEPLOY_USER}" "${SSH_DIR}"
    info "SSH public key added for ${DEPLOY_USER}."
fi

# ── 5. Clone / init app directory ────────────────────────────────────────────
info "Setting up application directory at ${APP_DIR}..."
mkdir -p "${APP_DIR}"
chown "${DEPLOY_USER}:${DEPLOY_USER}" "${APP_DIR}"

if [[ -n "${REPO_URL}" ]]; then
    if [[ ! -d "${APP_DIR}/.git" ]]; then
        info "Cloning ${REPO_URL}..."
        sudo -u "${DEPLOY_USER}" git clone "${REPO_URL}" "${APP_DIR}"
    else
        info "Repo already cloned."
    fi
else
    warn "REPO_URL not set — skipping git clone. You can clone manually later:"
    warn "  git clone YOUR_REPO_URL ${APP_DIR}"
fi

# ── 6. .env template ─────────────────────────────────────────────────────────
ENV_FILE="${APP_DIR}/.env"
if [[ ! -f "${ENV_FILE}" ]]; then
    info "Creating .env template at ${ENV_FILE} ..."
    cat > "${ENV_FILE}" <<'ENVEOF'
# =============================================================================
#  GMCL Production Environment Variables
#  Copy this file to .env and fill in ALL values before running deploy.sh
# =============================================================================

# ── Database ─────────────────────────────────────────────────────────────────
# Credentials for the Postgres container. Use strong random values.
POSTGRES_USER=gmcl
POSTGRES_PASSWORD=CHANGE_ME_DB_PASSWORD
POSTGRES_DB=gmcl

# Connection string used by the app (must match POSTGRES_* above).
DB_DSN=postgres://gmcl:CHANGE_ME_DB_PASSWORD@db:5432/gmcl?sslmode=disable

# ── App ───────────────────────────────────────────────────────────────────────
APP_HTTP_ADDR=:8080
APP_ENV=production
APP_BASE_URL=https://admin.gmcl.co.uk    # Update to your real domain

# ── Migrations ───────────────────────────────────────────────────────────────
MIGRATE=1
MIGRATE_DIR=/migrations

# ── Session security ──────────────────────────────────────────────────────────
# Generate with: openssl rand -base64 32
ADMIN_SESSION_SECRET=CHANGE_ME_SESSION_SECRET

# ── CSRF ─────────────────────────────────────────────────────────────────────
CSRF_SECRET=CHANGE_ME_CSRF_SECRET

# ── Bootstrap admin (only used when SEED=1 and no admin users exist) ─────────
# After first login the admin is forced to change this password.
SEED=1
SEED_ADMIN_EMAIL=webmaster@gmcl.co.uk
SEED_ADMIN_PASSWORD=CHANGE_ME_BOOTSTRAP_PASSWORD

# ── Email / SMTP (for admin invite emails) ────────────────────────────────────
SMTP_HOST=email-smtp.eu-west-2.amazonaws.com
SMTP_PORT=587
SMTP_USERNAME=CHANGE_ME_SMTP_USERNAME
SMTP_PASSWORD=CHANGE_ME_SMTP_PASSWORD
SMTP_FROM=GMCL Admin <webmaster@gmcl.co.uk>

# ── Play-Cricket API ──────────────────────────────────────────────────────────
PLAY_CRICKET_API_KEY=CHANGE_ME_PC_API_KEY
PLAY_CRICKET_SITE_ID=CHANGE_ME_PC_SITE_ID
ENVEOF
    chown "${DEPLOY_USER}:${DEPLOY_USER}" "${ENV_FILE}"
    chmod 600 "${ENV_FILE}"
    warn ".env created — EDIT IT BEFORE DEPLOYING: ${ENV_FILE}"
else
    info ".env already exists, skipping."
fi

# ── 7. Systemd service ────────────────────────────────────────────────────────
info "Installing systemd service..."
cat > /etc/systemd/system/gmcl.service <<SVCEOF
[Unit]
Description=GMCL Cricket Ground Feedback App
Requires=docker.service
After=docker.service network-online.target

[Service]
Type=oneshot
RemainAfterExit=yes
WorkingDirectory=${APP_DIR}
ExecStart=/usr/bin/docker compose up -d --build --remove-orphans
ExecStop=/usr/bin/docker compose down
TimeoutStartSec=300
User=${DEPLOY_USER}
Group=${DEPLOY_USER}
Restart=no

[Install]
WantedBy=multi-user.target
SVCEOF

systemctl daemon-reload
systemctl enable gmcl.service
info "Systemd service 'gmcl' enabled (starts on boot)."

# ── Done ─────────────────────────────────────────────────────────────────────
echo ""
echo -e "${GREEN}════════════════════════════════════════════════════════${NC}"
echo -e "${GREEN}  Server provisioning complete!                         ${NC}"
echo -e "${GREEN}════════════════════════════════════════════════════════${NC}"
echo ""
echo "  Next steps:"
echo "  1. Edit ${ENV_FILE} — fill in ALL CHANGE_ME values"
echo "  2. Update the Caddyfile with your real domain"
[[ -z "${REPO_URL}" ]] && echo "  3. Clone your repo: git clone YOUR_REPO ${APP_DIR}"
echo "  4. Run: cd ${APP_DIR} && bash scripts/deploy.sh"
echo ""
