#!/usr/bin/env bash
set -euo pipefail

cd /opt/gmcl

domain="rules-staging.gmcl.co.uk"

if [[ ! -f .env ]]; then
  postgres_password="$(openssl rand -hex 24)"
  session_secret="$(openssl rand -base64 48 | tr -d '\n')"
  admin_secret="$(openssl rand -base64 48 | tr -d '\n')"
  csrf_secret="$(openssl rand -base64 48 | tr -d '\n')"
  rules_secret="$(openssl rand -base64 48 | tr -d '\n')"
  n8n_secret="$(openssl rand -base64 48 | tr -d '\n')"
  admin_password="$(openssl rand -base64 18 | tr -d '/+=\n')"

  cat > .env <<ENVEOF
POSTGRES_USER=gmcl
POSTGRES_PASSWORD=${postgres_password}
POSTGRES_DB=gmcl
DB_DSN=postgres://gmcl:${postgres_password}@db:5432/gmcl?sslmode=disable

APP_HTTP_ADDR=:8080
APP_ENV=staging
APP_BASE_URL=https://${domain}
PUBLIC_BASE_URL=https://${domain}
PUBLIC_ALT_BASE_URL=https://${domain}
ENABLE_HSTS=1

MIGRATE=1
MIGRATE_DIR=/migrations
SEED=1
SEED_ADMIN_EMAIL=webmaster@gmcl.co.uk
SEED_ADMIN_PASSWORD=${admin_password}
DISABLE_2FA=1

SESSION_SECRET=${session_secret}
ADMIN_SESSION_SECRET=${admin_secret}
CSRF_SECRET=${csrf_secret}
RULES_ASSISTANT_SECRET=${rules_secret}
N8N_HMAC_SECRET=${n8n_secret}
INTERNAL_HMAC_SECRET=${n8n_secret}

OPENAI_API_KEY=
OPENAI_CHAT_MODEL=gpt-5.6-terra
OPENAI_EMBEDDING_MODEL=text-embedding-3-small
RULES_SOURCE_URL=https://www.gtrmcrcricket.co.uk/pages/rules-main-menu
ENVEOF

  printf '%s\n' "${admin_password}" > /root/gmcl-staging-admin-password
  chmod 600 .env /root/gmcl-staging-admin-password
fi

cp Caddyfile.production Caddyfile
sed -i "s/YOUR_DOMAIN/${domain}/g" Caddyfile
sed -i 's#https://www\.gmcl\.co\.uk#https://rules-staging.gmcl.co.uk#g' docker-compose.yml
sed -i 's#https://gmcl\.co\.uk#https://rules-staging.gmcl.co.uk#g' docker-compose.yml

docker compose pull db
docker compose up -d --build db app caddy
