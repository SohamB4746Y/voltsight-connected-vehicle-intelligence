#!/usr/bin/env bash
# Starts the whole pipeline (gateway, worker with range-risk, sink, alert service, API + web console) and a
# simulated fleet in real time. Requires: make up (stack), make seed, make build, and `make web` for the console.
# usage: tools/live/demo.sh [vehicles=100000] [sim_seconds=1800]
set -euo pipefail
cd "$(dirname "$0")/../.."
V=${1:-100000}; D=${2:-1800}
mkdir -p tmp/demo
set -a; source .env; set +a
for p in vsgateway vsworker vssink vsalerts vsapi vssim; do pkill -x "$p" 2>/dev/null || true; done
sleep 1
# clean slate: topics, consumer groups, live state, history, alerts
KT="docker exec voltsight-kafka-1 /opt/kafka/bin"
$KT/kafka-topics.sh --bootstrap-server localhost:9092 --delete --topic 'telemetry.v1,telemetry.dlq.v1,alerts.v1,charger.status.v1' >/dev/null 2>&1 || true
for g in rt-processor sink alert-svc; do $KT/kafka-consumer-groups.sh --bootstrap-server localhost:9092 --delete --group $g >/dev/null 2>&1 || true; done
docker exec voltsight-clickhouse-1 clickhouse-client --user voltsight --password "$CLICKHOUSE_PASSWORD" -q "drop table if exists telemetry_raw sync"
docker exec voltsight-redis-1 redis-cli -a "$REDIS_PASSWORD" flushall >/dev/null 2>&1
docker exec voltsight-postgres-1 psql -q -U voltsight -d voltsight -c "delete from alert_event; delete from alert" >/dev/null 2>&1 || true
sleep 2
./bin/vstopics >/dev/null
nohup ./bin/vsgateway > tmp/demo/gateway.log 2>&1 &
nohup ./bin/vsworker -vehicles 100000 > tmp/demo/worker.log 2>&1 &
nohup ./bin/vssink > tmp/demo/sink.log 2>&1 &
nohup ./bin/vsalerts > tmp/demo/alerts.log 2>&1 &
nohup ./bin/vsapi > tmp/demo/api.log 2>&1 &
sleep 8
echo "console: http://localhost:8081   (sign in as dispatcher@meridian.example / \$DEMO_USER_PASSWORD from .env)"
# a stressed fleet so alerts appear quickly: low batteries, drivers ignoring warnings, 90% of chargers failing at +120 s
nohup ./bin/vssim -vehicles "$V" -duration "$D" -start-tod 27000 -realtime -low-soc-fraction 0.3 -low-soc-min 0.02 -low-soc-max 0.05 \
  -charger-outage-at 90 -charger-outage-duration 1200 -charger-outage-fraction 0.99 \
  -gateway https://127.0.0.1:8443 > tmp/demo/sim.log 2>&1 &
echo "simulator running for $D s; logs in tmp/demo/"
