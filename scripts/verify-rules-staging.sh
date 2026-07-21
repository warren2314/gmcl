#!/usr/bin/env bash
set -euo pipefail

cd /opt/gmcl
docker compose ps
docker compose logs --tail=80 app

if ! docker compose exec -T app sh -lc '[ "$RULES_ASSISTANT_ENABLED" = "true" ]'; then
  echo "ERROR: RULES_ASSISTANT_ENABLED is not true in the app container" >&2
  exit 1
fi

if ! docker compose exec -T app sh -lc '[ -n "$OPENAI_API_KEY" ]'; then
  echo "ERROR: OPENAI_API_KEY is not configured in the app container" >&2
  exit 1
fi

docker compose exec -T app wget -q -O /dev/null http://127.0.0.1:8080/rules-assistant

test "$(docker compose exec -T db psql -U gmcl -d gmcl -Atc "SELECT extname FROM pg_extension WHERE extname='vector'")" = "vector"
test "$(docker compose exec -T db psql -U gmcl -d gmcl -Atc "SELECT to_regclass('public.rule_releases')")" = "rule_releases"

active_release_count="$(docker compose exec -T db psql -U gmcl -d gmcl -Atc "SELECT COUNT(*) FROM rule_releases WHERE status='active'")"
if [[ "${active_release_count}" -lt 1 ]]; then
  echo "ERROR: no active rules release is available; run the initial A1 sync" >&2
  exit 1
fi

status="$(curl -sS -o /dev/null -w '%{http_code}' -H 'Host: rules-staging.gmcl.co.uk' http://127.0.0.1/health)"
test "${status}" = "308" || test "${status}" = "200"

echo "pgvector=ready"
echo "rules_schema=ready"
echo "rules_assistant=enabled"
echo "active_rules_releases=${active_release_count}"
echo "local_http_status=${status}"
