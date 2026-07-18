#!/usr/bin/env bash
set -euo pipefail

cd /opt/gmcl
docker compose ps
docker compose logs --tail=80 app

test "$(docker compose exec -T db psql -U gmcl -d gmcl -Atc "SELECT extname FROM pg_extension WHERE extname='vector'")" = "vector"
test "$(docker compose exec -T db psql -U gmcl -d gmcl -Atc "SELECT to_regclass('public.rule_releases')")" = "rule_releases"

status="$(curl -sS -o /dev/null -w '%{http_code}' -H 'Host: rules-staging.gmcl.co.uk' http://127.0.0.1/health)"
test "${status}" = "308" || test "${status}" = "200"

echo "pgvector=ready"
echo "rules_schema=ready"
echo "local_http_status=${status}"
