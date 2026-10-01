#!/usr/bin/env bash
# Chaos scenario: run the live pipeline (100K vehicles, real time, no injected data faults), break one component
# mid-run, let the system recover, then prove nothing was lost: the table written by the faulted live path must equal
# a clean replay of the whole Kafka log (distinct events, ClickHouse FINAL).
# usage: chaos/run.sh <worker-kill|sink-kill|alerts-kill|redis-restart|clickhouse-restart|kafka-restart|gateway-kill> <outdir> [seconds=75]
set -uo pipefail
S=${1:?scenario}; OUT=${2:?out dir}; D=${3:-75}
cd "$(dirname "$0")/.."
mkdir -p "$OUT"; set -a; source .env; set +a
CH="docker exec voltsight-clickhouse-1 clickhouse-client --user voltsight --password $CLICKHOUSE_PASSWORD -q"
KT="docker exec voltsight-kafka-1 /opt/kafka/bin"
LOW="-f deploy/compose/docker-compose.lowulimit.yml"
now() { date +%s; }
lag() { $KT/kafka-consumer-groups.sh --bootstrap-server localhost:9092 --describe --group "$1" 2>/dev/null | awk 'NR>2 && $6 ~ /^[0-9]+$/ {l+=$6; n++} END{print (n?l:-1)}'; }
start_gateway() { nohup ./bin/vsgateway >> "$OUT/gateway.log" 2>&1 & }
start_worker()  { nohup ./bin/vsworker -risk=false >> "$OUT/worker.log" 2>&1 & }
start_sink()    { nohup ./bin/vssink >> "$OUT/sink.log" 2>&1 & }

for p in vsgateway vsworker vssink vsalerts vssim; do pkill -x $p 2>/dev/null; done; sleep 1
$KT/kafka-topics.sh --bootstrap-server localhost:9092 --delete --topic 'telemetry.v1,telemetry.dlq.v1,alerts.v1,charger.status.v1' >/dev/null 2>&1
for g in rt-processor sink chk alert-svc; do $KT/kafka-consumer-groups.sh --bootstrap-server localhost:9092 --delete --group $g >/dev/null 2>&1; done
$CH "drop table if exists telemetry_raw sync"; $CH "drop table if exists telemetry_chk sync"
docker exec voltsight-redis-1 redis-cli -a "$REDIS_PASSWORD" flushall >/dev/null 2>&1
sleep 2; ./bin/vstopics >/dev/null
start_gateway; start_worker; start_sink; sleep 8
./bin/vssim -vehicles 100000 -duration "$D" -start-tod 27000 -realtime -gateway https://127.0.0.1:8443 -report "$OUT/sim.json" > "$OUT/sim.log" 2>&1 &
SIM=$!
sleep 30
T_FAULT=$(now); echo "fault at $(date +%T): $S" | tee "$OUT/events.log"
case $S in
  worker-kill)        pkill -9 -x vsworker; sleep 5; start_worker ;;
  sink-kill)          pkill -9 -x vssink; sleep 5; start_sink ;;
  redis-restart)      docker restart voltsight-redis-1 >/dev/null ;;
  clickhouse-restart) docker restart voltsight-clickhouse-1 >/dev/null ;;
  kafka-restart)      docker restart voltsight-kafka-1 >/dev/null ;;
  gateway-kill)       pkill -9 -x vsgateway; sleep 4; start_gateway ;;
  *) echo "unknown scenario"; exit 2 ;;
esac
echo "fault injected, recovery started $(date +%T)" >> "$OUT/events.log"
# recovery time: until both consumer groups are back under 20K records of lag
T_REC=""
for i in $(seq 1 200); do
  a=$(lag rt-processor); b=$(lag sink)
  echo "$(date +%T) rt=$a sink=$b" >> "$OUT/lag.log"
  if [ -z "$T_REC" ] && [ "$a" -ge 0 ] && [ "$b" -ge 0 ] && [ "$a" -lt 20000 ] && [ "$b" -lt 20000 ] && [ $(( $(now) - T_FAULT )) -gt 8 ]; then T_REC=$(now); fi
  if [ -n "$T_REC" ] && ! kill -0 $SIM 2>/dev/null; then break; fi
  sleep 2
done
wait $SIM 2>/dev/null
for i in $(seq 1 90); do [ "$(lag sink)" = "0" ] && [ "$(lag rt-processor)" = "0" ] && break; sleep 2; done
echo "recovered_to_lag_lt_20k_seconds_after_fault: $([ -n "$T_REC" ] && echo $(( T_REC - T_FAULT )) || echo not-observed)" >> "$OUT/events.log"
# clean replay of the whole log into a second table
pkill -x vssink; sleep 1
timeout 240 ./bin/vssink -group chk -table telemetry_chk > "$OUT/replay.log" 2>&1 &
for i in $(seq 1 100); do [ "$(lag chk)" = "0" ] && break; sleep 3; done
sleep 3; pkill -x vssink
$CH "optimize table telemetry_raw final" >/dev/null 2>&1; $CH "optimize table telemetry_chk final" >/dev/null 2>&1
LIVE=$($CH "select count() from telemetry_raw final"); CHK=$($CH "select count() from telemetry_chk final")
KAFKA=$($KT/kafka-get-offsets.sh --bootstrap-server localhost:9092 --topic telemetry.v1 | awk -F: '{s+=$3} END{print s}')
python3 - "$OUT" "$S" "$LIVE" "$CHK" "$KAFKA" <<'PY'
import json,sys,os
out,s,live,chk,kafka=sys.argv[1:6]
sim=json.load(open(os.path.join(out,'sim.json')))
ev=open(os.path.join(out,'events.log')).read()
res={"scenario":s,"live_distinct_rows":int(live),"clean_replay_distinct_rows":int(chk),"kafka_records":int(kafka),
 "lost_vs_clean_replay":int(chk)-int(live),"sim_events_emitted":sim["events_emitted"],"gateway_accepted":sim["gateway"]["accepted"],
 "sim_failed":sim["gateway"]["failed"],"sim_retried_429":sim["gateway"]["retried_429"],"sim_retried_503":sim["gateway"]["retried_503"],
 "events_log":ev.strip().split("\n"),"no_loss":int(live)==int(chk)}
json.dump(res,open(os.path.join(out,'result.json'),'w'),indent=2); print(json.dumps(res,indent=2))
PY
for p in vsgateway vsworker vssink; do pkill -x $p 2>/dev/null; done
