#!/usr/bin/env bash
# Live end-to-end run: simulator -> mTLS gateway -> Kafka -> {stream worker, ClickHouse sink}, with a
# per-second resource sampler, then reconciliation. Requires `make up bootstrap seed` and built binaries in ./bin.
# usage: tools/live/e2e.sh <out-dir> [vehicles=100000] [duration_s=60] [extra vssim flags...]
set -euo pipefail
OUT=${1:?out dir}; V=${2:-100000}; D=${3:-60}; shift 3 || shift $#
cd "$(dirname "$0")/../.."
mkdir -p "$OUT"
set -a; source .env; set +a
CH="docker exec voltsight-clickhouse-1 clickhouse-client --user voltsight --password $CLICKHOUSE_PASSWORD -q"
KT="docker exec voltsight-kafka-1 /opt/kafka/bin"

# clean slate
pkill -f bin/vsgateway || true; pkill -f bin/vsworker || true; pkill -f bin/vssink || true; pkill -f bin/vsalerts || true; sleep 1
$KT/kafka-topics.sh --bootstrap-server localhost:9092 --delete --topic 'telemetry.v1,telemetry.dlq.v1,alerts.v1,charger.status.v1' >/dev/null 2>&1 || true
for g in rt-processor sink alert-svc; do $KT/kafka-consumer-groups.sh --bootstrap-server localhost:9092 --delete --group $g >/dev/null 2>&1 || true; done
$CH "drop table if exists telemetry_raw sync"
docker exec voltsight-postgres-1 psql -q -U voltsight -d voltsight -c "delete from alert" >/dev/null 2>&1 || true
docker exec voltsight-redis-1 redis-cli -a "$REDIS_PASSWORD" flushall >/dev/null 2>&1
sleep 2; ./bin/vstopics >/dev/null

nohup ./bin/vsgateway > "$OUT/gateway.log" 2>&1 &
nohup ./bin/vsworker > "$OUT/worker.log" 2>&1 &
nohup ./bin/vssink > "$OUT/sink.log" 2>&1 &
nohup ./bin/vsalerts -latency-log "$OUT/alert_latency.ndjson" > "$OUT/alerts.log" 2>&1 &
sleep 6

# sampler: 1 Hz, ClickHouse tracked memory + container cgroup usage + host load + consumer lag
( echo "ts,ch_tracked_mb,ch_cgroup_mb,load1"
  CID=$(docker inspect -f '{{.Id}}' voltsight-clickhouse-1)
  while true; do
    m=$($CH "select round(value/1048576) from system.metrics where metric='MemoryTracking'" 2>/dev/null || echo NA)
    c=$(( $(cat /sys/fs/cgroup/memory/docker/$CID/memory.usage_in_bytes 2>/dev/null || cat /sys/fs/cgroup/system.slice/docker-$CID.scope/memory.current 2>/dev/null || echo 0) / 1048576 ))
    echo "$(date +%T),$m,$c,$(cut -d' ' -f1 /proc/loadavg)"; sleep 1
  done ) > "$OUT/samples.csv" 2>/dev/null &
SAMP=$!

# optional adversarial analytics: heavy aggregations against the table being written (ADVERSARIAL=1)
if [ "${ADVERSARIAL:-0}" = "1" ]; then
  ( while true; do
      r=$($CH "select uniqExact(tenant_id,vin,ts,seq) from telemetry_raw" 2>&1 | head -c 300 | tr '\n' ' ')
      echo "$(date +%T) $r"; sleep 3
    done ) > "$OUT/adversarial.log" 2>&1 &
  ADV=$!
fi

./bin/vssim -vehicles "$V" -duration "$D" -start-tod 27000 -realtime -gateway https://127.0.0.1:8443 -report "$OUT/sim.json" "$@" > "$OUT/sim.log" 2>&1 || true

# let the consumers drain (bounded), then reconcile
group_lag(){ $KT/kafka-consumer-groups.sh --bootstrap-server localhost:9092 --describe --group "$1" 2>/dev/null | awk 'NR>2 && $6 ~ /^[0-9]+$/ {l+=$6; n++} END{print (n?l:-1)}'; }
for i in $(seq 1 180); do
  [ "$(group_lag sink)" = "0" ] && [ "$(group_lag rt-processor)" = "0" ] && break; sleep 2
done
echo "drain loop iterations: $i" > "$OUT/drain.txt"
sleep 5
kill $SAMP ${ADV:-} 2>/dev/null || true
./bin/vsrecon -report "$OUT/recon.json" > "$OUT/recon.out" 2> "$OUT/recon.err" || echo "recon: books do not balance (see $OUT/recon.err)"
for i in $(seq 1 30); do [ "$(group_lag alert-svc)" = "0" ] && break; sleep 1; done
docker exec voltsight-postgres-1 psql -At -U voltsight -d voltsight -c "select rule, severity, count(*) from alert group by 1,2 order by 1,2" > "$OUT/alerts_pg.txt" 2>&1 || true
$KT/kafka-get-offsets.sh --bootstrap-server localhost:9092 --topic alerts.v1 > "$OUT/alerts_topic_offsets.txt" 2>&1 || true
$CH "system flush logs" >/dev/null
$CH "select event_type, count(), formatReadableSize(max(peak_memory_usage)), max(rows) from system.part_log group by event_type format TSV" > "$OUT/ch_part_log.tsv"
$CH "select count(), uniqExact(partition_id), sum(rows) from system.parts where table='telemetry_raw' and active format TSV" > "$OUT/ch_parts.tsv"
pkill -f bin/vsgateway || true; pkill -f bin/vsworker || true; pkill -f bin/vssink || true; pkill -f bin/vsalerts || true
echo "done: $OUT"
