#!/usr/bin/env bash
# One-command live bring-up of the WHOLE platform (Kafka, PostgreSQL+pgvector, Redis, ClickHouse, Keycloak, Vault,
# observability, gateway, stream worker + range-risk engine, sink, alert service, API + web console, simulator)
# behind a single HTTPS origin. Works unchanged on any Docker host: GitHub Codespaces, a cloud VM, a laptop.
#
#   deploy/live/up.sh                 # start (or update) the stack; prints the public URL and the demo logins
#   deploy/live/up.sh --reset-demo    # additionally clear topics, live state, history and alerts (clean demo start)
#
# Environment (all optional):
#   PUBLIC_URL      the https origin users will open (auto-detected in Codespaces; default http://localhost:8080)
#   PUBLIC_HOST     DNS name of this host: with it the proxy obtains a Let's Encrypt certificate (ports 80/443)
#   DEMO_VEHICLES   simulated vehicles streaming in real time (default 2000, i.e. ~2,000 events/s)
#   VOLTSIGHT_IMAGE application image (default: the GHCR image; it is built locally when it cannot be pulled)
#   COMPOSE_EXTRA   an additional compose file layered on top (e.g. deploy/compose/docker-compose.lowulimit.yml for
#                   hosts whose hard RLIMIT_NOFILE is below 262144, such as restricted sandboxes)
#   ANTHROPIC_API_KEY  enables the Anthropic Copilot provider (otherwise the deterministic stub provider is used)
set -euo pipefail
cd "$(dirname "$0")/../.."

RESET=0; [ "${1:-}" = "--reset-demo" ] && RESET=1
say() { printf '\033[1m==> %s\033[0m\n' "$*"; }
need() { command -v "$1" >/dev/null 2>&1 || { echo "missing prerequisite: $1" >&2; exit 1; }; }
need docker; need python3; need curl
docker compose version >/dev/null 2>&1 || { echo "missing prerequisite: docker compose v2" >&2; exit 1; }

# ---- configuration -----------------------------------------------------------------------------------------
python3 tools/dev/gen_env.py >/dev/null
set_env() { # set_env KEY VALUE  (replace or append in .env; the file is git-ignored)
  if grep -q "^$1=" .env; then sed -i "s|^$1=.*|$1=$2|" .env; else printf '%s=%s\n' "$1" "$2" >> .env; fi
}
get_env() { grep "^$1=" .env | head -1 | cut -d= -f2-; }

if [ -z "${PUBLIC_URL:-}" ]; then
  if [ -n "${CODESPACE_NAME:-}" ]; then
    PUBLIC_URL="https://${CODESPACE_NAME}-8080.${GITHUB_CODESPACES_PORT_FORWARDING_DOMAIN:-app.github.dev}"
  elif [ -n "${PUBLIC_HOST:-}" ]; then
    PUBLIC_URL="https://${PUBLIC_HOST}"
  else
    PUBLIC_URL="http://localhost:8080"
  fi
fi
PUBLIC_URL="${PUBLIC_URL%/}"
PREV_URL="$(get_env PUBLIC_URL || true)"
set_env PUBLIC_URL "$PUBLIC_URL"
[ -n "${PUBLIC_HOST:-}" ] && set_env PUBLIC_HOST "$PUBLIC_HOST"
[ -n "${DEMO_VEHICLES:-}" ] && set_env DEMO_VEHICLES "$DEMO_VEHICLES"
[ -n "${VOLTSIGHT_IMAGE:-}" ] && set_env VOLTSIGHT_IMAGE "$VOLTSIGHT_IMAGE"
[ -n "${ANTHROPIC_API_KEY:-}" ] && set_env ANTHROPIC_API_KEY "$ANTHROPIC_API_KEY"

PROFILES=""; [ -n "${PUBLIC_HOST:-}" ] && PROFILES="--profile tls"
EXTRA=""; [ -n "${COMPOSE_EXTRA:-}" ] && EXTRA="-f $COMPOSE_EXTRA"
DC="docker compose $PROFILES -f deploy/compose/docker-compose.yml -f deploy/compose/docker-compose.live.yml $EXTRA --env-file .env"
IMAGE="$(get_env VOLTSIGHT_IMAGE || true)"; IMAGE="${IMAGE:-ghcr.io/sohamb4746y/voltsight-connected-vehicle-intelligence:main}"
PROXY=caddy; [ -n "${PUBLIC_HOST:-}" ] && PROXY=caddy-tls

# ---- application image ------------------------------------------------------------------------------------
if ! docker image inspect "$IMAGE" >/dev/null 2>&1; then
  say "pulling $IMAGE"
  $DC pull -q api 2>/dev/null || { say "image not published; building it from this checkout (a few minutes)"; $DC build api; }
fi

# ---- infrastructure ----------------------------------------------------------------------------------------
say "starting infrastructure (Kafka, PostgreSQL, ClickHouse, Redis, Vault, Keycloak, observability)"
up_infra() { $DC up -d --wait --wait-timeout 600 kafka postgres clickhouse redis vault keycloak otel-collector prometheus grafana; }
# a cold start on a small shared host can report one service unhealthy before it settles: retry, bounded
for attempt in 1 2 3; do up_infra && break || { [ "$attempt" = 3 ] && exit 1; say "infrastructure not healthy yet (attempt $attempt of 3); waiting"; sleep 15; }; done

# Keycloak imports the realm only when it does not exist yet; a changed public URL needs a fresh import.
if [ -n "$PREV_URL" ] && [ "$PREV_URL" != "$PUBLIC_URL" ]; then
  say "public URL changed ($PREV_URL -> $PUBLIC_URL): re-importing the Keycloak realm"
  $DC stop keycloak
  $DC exec -T postgres psql -q -U voltsight -d postgres -c "DROP DATABASE IF EXISTS keycloak WITH (FORCE)" -c "CREATE DATABASE keycloak"
  $DC up -d --wait --wait-timeout 300 keycloak
fi

# ---- one-time / idempotent initialisation (run from the same image the services use) -----------------------
run_init() { $DC run --rm -T --no-deps "$@"; }
say "database schema, service roles, device CA (Vault PKI), Kafka topics"
run_init init-migrate
run_init init-roles
run_init init-pki      # dev-mode Vault is in-memory: the CA is re-created whenever Vault restarted
run_init init-topics
VEH="$($DC exec -T postgres psql -At -U voltsight -d voltsight -c 'SELECT count(*) FROM vehicle' 2>/dev/null || echo 0)"
if [ "${VEH:-0}" -lt 1 ]; then
  say "seeding the deterministic 100,000-vehicle fleet (about a minute)"
  run_init init-seed
  run_init init-incidents
fi

if [ "$RESET" = 1 ]; then
  say "clean demo start: clearing topics, consumer groups, live state, history and alerts"
  $DC stop simulator worker sink alerts gateway api 2>/dev/null || true
  KT="$DC exec -T kafka /opt/kafka/bin"
  $KT/kafka-topics.sh --bootstrap-server localhost:9092 --delete --topic 'telemetry.v1,telemetry.dlq.v1,alerts.v1,charger.status.v1' >/dev/null 2>&1 || true
  for g in rt-processor sink alert-svc; do $KT/kafka-consumer-groups.sh --bootstrap-server localhost:9092 --delete --group $g >/dev/null 2>&1 || true; done
  $DC exec -T clickhouse clickhouse-client --user voltsight --password "$(get_env CLICKHOUSE_PASSWORD)" -q "drop table if exists telemetry_raw sync"
  $DC exec -T redis redis-cli -a "$(get_env REDIS_PASSWORD)" --no-auth-warning flushall >/dev/null
  $DC exec -T postgres psql -q -U voltsight -d voltsight -c "delete from alert_event; delete from alert" >/dev/null 2>&1 || true
  sleep 2
  run_init init-topics
fi

# ---- the platform ----------------------------------------------------------------------------------------
say "starting gateway, stream worker (range-risk), sink, alert service, API + console, proxy"
$DC up -d --force-recreate gateway worker sink alerts api "$PROXY"
sleep 6
say "starting the simulator: ${DEMO_VEHICLES:-$(get_env DEMO_VEHICLES || echo 2000)} vehicles streaming through the mTLS gateway"
$DC up -d --force-recreate simulator

# ---- readiness (through the public proxy, i.e. the same path a browser takes) -------------------------------
LOCAL=http://localhost:8080; [ -n "${PUBLIC_HOST:-}" ] && LOCAL=https://localhost
CURL="curl -fsS -k -m 5 -H Host:$(echo "$PUBLIC_URL" | sed 's|^[a-z]*://||')"
say "waiting for the API and the OIDC provider"
ok=0
for _ in $(seq 1 90); do
  if $CURL "$LOCAL/readyz" >/dev/null 2>&1 && $CURL "$LOCAL/realms/voltsight/.well-known/openid-configuration" 2>/dev/null | grep -q "\"issuer\":\"$PUBLIC_URL/realms/voltsight\""; then ok=1; break; fi
  sleep 3
done
[ "$ok" = 1 ] || { echo "stack did not become ready; see: $DC logs --tail=50 api keycloak caddy" >&2; exit 1; }

# GitHub Codespaces: forwarded ports are private by default; make the console reachable by people without a GitHub login
if [ -n "${CODESPACE_NAME:-}" ] && command -v gh >/dev/null 2>&1; then
  gh codespace ports visibility 8080:public -c "$CODESPACE_NAME" >/dev/null 2>&1 \
    || echo "NOTE: could not make port 8080 public automatically: PORTS tab -> 8080 -> right-click -> Port Visibility -> Public"
fi

PW="$(get_env DEMO_USER_PASSWORD)"
cat <<EOF

VoltSight is live:   $PUBLIC_URL
  sign in as         dispatcher@meridian.example   (also viewer@ / energy_manager@ / tenant_admin@meridian.example,
                     and the same four roles @coastal.example for a second tenant)
  password           $PW        (generated for this deployment; stored only in the git-ignored .env)
  simulator          $(get_env DEMO_VEHICLES || echo 2000) vehicles, real time, through the mTLS gateway
  logs               $DC logs -f --tail=50 worker api
  stop               docker compose -f deploy/compose/docker-compose.yml -f deploy/compose/docker-compose.live.yml --env-file .env down
EOF
