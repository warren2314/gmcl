# GMCL Deployment Guide

This guide covers deploying the GMCL Cricket Ground Feedback app to a DigitalOcean droplet using Docker Compose and Caddy for automatic TLS.

---

## Architecture

```
Internet → Caddy (80/443, TLS termination) → App (:8080) → Postgres (internal only)
```

All three services run as Docker containers on the same host, on an isolated internal network (`appnet`). Only ports 80 and 443 are exposed to the internet.

---

## Prerequisites

| Item | Details |
|------|---------|
| DigitalOcean droplet | Ubuntu 22.04 or 24.04, 1 GB RAM minimum (2 GB recommended) |
| Domain name | A DNS A record pointing at the droplet's IP (e.g. `admin.gmcl.co.uk → YOUR_IP`) |
| SSH access | Root or sudo access to the droplet |
| Git repo access | The server needs to be able to `git pull` from your repository |

---

## Step 1 — Create the Droplet

1. Log into DigitalOcean and create a new droplet:
   - **Image**: Ubuntu 22.04 LTS
   - **Size**: Basic, 2 GB / 1 vCPU (can scale up later)
   - **Region**: London or nearest to your users
   - **Authentication**: SSH key (add your public key)
2. Note the droplet's public IP address.

---

## Step 2 — Point DNS

In your DNS provider, add an **A record**:

```
admin.gmcl.co.uk  →  YOUR_DROPLET_IP
```

TTL: 300 (5 minutes) to propagate quickly. Caddy will fail to get a certificate if DNS hasn't propagated.

---

## Step 3 — Provision the Server

SSH into the droplet as root and run the setup script. It installs Docker, configures the firewall, creates a non-root deploy user, and writes a `.env` template.

```bash
ssh root@YOUR_DROPLET_IP

# Option A: clone the repo first, then run the script
git clone git@github.com:YOUR_ORG/gmcl.git /opt/gmcl
cd /opt/gmcl
bash scripts/setup-server.sh

# Option B: let the script clone for you
REPO_URL=git@github.com:YOUR_ORG/gmcl.git bash scripts/setup-server.sh
```

The script will:
- Install Docker Engine and Docker Compose plugin
- Configure UFW (SSH, HTTP, HTTPS only)
- Create a `deploy` user in the `docker` group
- Create `/opt/gmcl/.env` with placeholder values

---

## Step 4 — Configure Environment Variables

Edit the `.env` file:

```bash
nano /opt/gmcl/.env
```

Replace **every** `CHANGE_ME_*` value:

| Variable | Description |
|----------|-------------|
| `POSTGRES_PASSWORD` | Strong random password for the Postgres container |
| `DB_DSN` | Must use the same password as `POSTGRES_PASSWORD` |
| `ADMIN_SESSION_SECRET` | 32-byte random secret — run `openssl rand -base64 32` |
| `CSRF_SECRET` | 32-byte random secret — run `openssl rand -base64 32` |
| `SEED_ADMIN_PASSWORD` | Temporary password for `webmaster@gmcl.co.uk` (forced change on first login) |
| `SMTP_*` | Your SMTP details for sending admin invite emails |
| `PLAY_CRICKET_API_KEY` | Your Play-Cricket API key |
| `APP_BASE_URL` | Your full domain, e.g. `https://admin.gmcl.co.uk` |

> **Security**: `.env` is created with `chmod 600` by the setup script. Never commit it to git.

Generate random secrets:

```bash
openssl rand -base64 32   # run twice — once for ADMIN_SESSION_SECRET, once for CSRF_SECRET
```

---

## Step 5 — Configure the Production Caddyfile

The development `Caddyfile` uses `http://localhost` (no TLS). Before deploying, switch to the production version:

```bash
cd /opt/gmcl
cp Caddyfile.production Caddyfile
```

Then update the domain in the Caddyfile:

```bash
# Replace YOUR_DOMAIN with your actual domain
sed -i 's/YOUR_DOMAIN/admin.gmcl.co.uk/g' Caddyfile
```

---

## Step 6 — Deploy

Run the deploy script as the `deploy` user (or root):

```bash
cd /opt/gmcl
bash scripts/deploy.sh
```

This will:
1. Validate `.env` has no `CHANGE_ME` placeholders
2. Pull the latest code from the current branch
3. Build Docker images and restart containers
4. Wait for the `/health` endpoint to respond
5. Clean up old dangling images

First deploy takes 3–5 minutes while Docker builds the Go binary.

---

## Step 7 — First Login

1. Visit `https://admin.gmcl.co.uk/admin/login`
2. Log in with:
   - **Username**: `webmaster@gmcl.co.uk`
   - **Password**: the value you set for `SEED_ADMIN_PASSWORD`
3. You will be immediately redirected to the **Change Password** page — set a strong password.
4. After changing the password, you have full admin access.

> Once logged in, go to **Admin Users** to invite other administrators. Each new admin receives an email with a temporary password and must change it on first login.

---

## Subsequent Deployments

Every time you push new code, deploy it from the server:

```bash
ssh deploy@YOUR_DROPLET_IP "cd /opt/gmcl && bash scripts/deploy.sh"
```

Or from CI/CD (GitHub Actions example):

```yaml
- name: Deploy to production
  run: |
    ssh -o StrictHostKeyChecking=no deploy@${{ secrets.DROPLET_IP }} \
      "cd /opt/gmcl && bash scripts/deploy.sh"
```

---

## Useful Commands

```bash
# View live app logs
docker compose logs -f app

# View Caddy / TLS logs
docker compose logs -f caddy

# Container status
docker compose ps

# Restart just the app (no rebuild)
docker compose restart app

# Open a Postgres shell
docker compose exec db psql -U gmcl -d gmcl

# Run a database migration manually
docker compose exec app /bin/app --migrate-only

# Full restart (all containers)
docker compose down && docker compose up -d
```

---

## Backup

The only stateful volume is `db_data` (Postgres). Back it up regularly:

```bash
# Dump the database to a compressed file
docker compose exec db pg_dump -U gmcl gmcl | gzip > /opt/backups/gmcl-$(date +%Y%m%d-%H%M%S).sql.gz

# Automate with cron (daily at 2am)
echo "0 2 * * * deploy cd /opt/gmcl && docker compose exec -T db pg_dump -U gmcl gmcl | gzip > /opt/backups/gmcl-\$(date +\%Y\%m\%d).sql.gz" \
    | crontab -u deploy -
```

---

## Troubleshooting

| Symptom | Likely cause | Fix |
|---------|-------------|-----|
| Caddy shows "no certificate" | DNS not propagated yet | Wait 5–10 min, check `dig admin.gmcl.co.uk` |
| App container restarts in loop | Bad `.env` values | `docker compose logs app` — look for startup errors |
| `/health` returns 502 | App not started | `docker compose ps` — check app container is `Up` |
| Can't log in | DB not seeded | Check `SEED=1` in `.env`, `docker compose logs app` |
| Email not sending | SMTP config wrong | Check `SMTP_*` values, ensure app password used (not account password) for Gmail |
| `CHANGE_ME` error at deploy | Forgot to fill `.env` | Edit `/opt/gmcl/.env` and fill all values |

---

## Security Notes

- The `db` container is **not** exposed on any host port — only accessible from within `appnet`.
- Caddy enforces HTTPS and sets security headers (HSTS, X-Frame-Options, etc.).
- Admin sessions use HMAC-signed cookies; the signing key is `ADMIN_SESSION_SECRET`.
- CSRF tokens are required on all admin POST routes.
- All admin actions are written to the `audit_log` table.
- Passwords are stored as bcrypt hashes (cost 12).
