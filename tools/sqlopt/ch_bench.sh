#!/usr/bin/env bash
# ClickHouse history benchmark on SYNTHETIC rows generated inside ClickHouse (numbers()): 100,000 vehicles x S samples
# (one per minute), same schema, ordering and codecs as telemetry_raw. Measures bytes per row on disk and the latency of
# representative history queries. usage: tools/sqlopt/ch_bench.sh <samples_per_vehicle> <out.json>
set -euo pipefail
cd "$(dirname "$0")/../.."
set -a; source .env; set +a
S=${1:-300}; OUT=${2:-evidence/G14/ch_bench.json}
CH="docker exec -i voltsight-clickhouse-1 clickhouse-client --user voltsight --password $CLICKHOUSE_PASSWORD"
$CH -q "drop table if exists telemetry_big sync"
# same DDL as the sink (printed by the sink package through a tiny helper below)
DDL=$(cat <<'SQL'
CREATE TABLE telemetry_big (
  tenant_id UUID, vin FixedString(17),
  ts DateTime64(3, 'UTC') CODEC(DoubleDelta, ZSTD(1)), recv_ts DateTime64(3, 'UTC') CODEC(DoubleDelta, ZSTD(1)),
  seq UInt64 CODEC(Delta, ZSTD(1)), lat Float64 CODEC(Gorilla, ZSTD(1)), lon Float64 CODEC(Gorilla, ZSTD(1)),
  speed_kmh Float32 CODEC(Gorilla, ZSTD(1)), soc_pct Float32 CODEC(Gorilla, ZSTD(1)), odo_km Float64 CODEC(Gorilla, ZSTD(1)),
  heading_deg Float32 CODEC(ZSTD(1)), pack_temp_c Float32 CODEC(Gorilla, ZSTD(1)), ambient_temp_c Float32 CODEC(Gorilla, ZSTD(1)),
  pack_voltage_v Float32 CODEC(Gorilla, ZSTD(1)), pack_current_a Float32 CODEC(Gorilla, ZSTD(1)),
  charge_state Enum8('UNSPECIFIED' = 0, 'NONE' = 1, 'PLUGGED' = 2, 'CHARGING' = 3), charger_id LowCardinality(String),
  evt Enum8('UNSPECIFIED' = 0, 'HARSH_BRAKE' = 1), oem LowCardinality(String), schema_ver UInt8, dtc Array(LowCardinality(String))
) ENGINE = ReplacingMergeTree PARTITION BY toYYYYMMDD(ts) ORDER BY (tenant_id, vin, ts, seq)
SQL
)
$CH -q "$DDL"
START=$(date +%s)
CHUNK=20   # sample rows per vehicle per INSERT (2M rows per statement keeps memory low)
for ((a=0; a<S; a+=CHUNK)); do
  b=$((a+CHUNK)); [ $b -gt $S ] && b=$S
  $CH -q "INSERT INTO telemetry_big
    SELECT toUUID(concat('00000000-0000-0000-0000-00000000000', toString(1 + v % 6))),
           toFixedString(concat('ZAR', leftPad(toString(v), 14, '0')), 17),
           toDateTime64('2026-09-01 00:00:00', 3, 'UTC') + toIntervalSecond(s * 60), toDateTime64('2026-09-01 00:00:00', 3, 'UTC') + toIntervalSecond(s * 60 + 1),
           s, 13 + (v % 1000) / 1000.0 + sin(s / 50.0) / 100, 80 + (v % 777) / 1000.0 + cos(s / 50.0) / 100,
           toFloat32(30 + 20 * sin(s / 9.0 + v)), toFloat32(20 + 80 * (1 - ((s * 7 + v) % 600) / 600.0)), 5000 + s * 0.4 + (v % 100),
           toFloat32(s % 360), toFloat32(28 + 6 * sin(s / 30.0)), toFloat32(30), toFloat32(350 + (s % 50)), toFloat32(-20 + (s % 40)),
           if(s % 9 = 0, 'CHARGING', 'NONE'), '', 'UNSPECIFIED', if(v % 2 = 0, 'AURORA', 'BOREAS'), 1, []
    FROM (SELECT number % 100000 AS v, intDiv(number, 100000) + $a AS s FROM numbers(100000 * ($b - $a)))" > /dev/null
done
$CH -q "optimize table telemetry_big final" > /dev/null
WALL=$(( $(date +%s) - START ))
ROWS=$($CH -q "select count() from telemetry_big")
read BYTES COMP <<< $($CH -q "select sum(bytes_on_disk), sum(data_uncompressed_bytes) from system.parts where table='telemetry_big' and active format TSV")
q() { # run a query, print wall ms and rows read (from the query log)
  local t0=$(date +%s%N); $CH -q "$1" > /dev/null; local ms=$(( ($(date +%s%N) - t0) / 1000000 ))
  $CH -q "system flush logs" > /dev/null
  local rr=$($CH -q "select read_rows from system.query_log where type='QueryFinish' and query like '%$2%' and query not like '%system.query_log%' order by event_time desc limit 1")
  echo "{\"name\":\"$2\",\"wall_ms\":$ms,\"rows_read\":${rr:-0}}"
}
T=00000000-0000-0000-0000-000000000001
Q=$(cat <<JSON
[
$(q "SELECT ts, soc_pct FROM telemetry_big WHERE tenant_id = '$T' AND vin = 'ZAR00000000000007' AND ts >= '2026-09-02 00:00:00' AND ts < '2026-09-03 00:00:00' ORDER BY ts /*q_vehicle_day*/" q_vehicle_day),
$(q "SELECT toStartOfHour(ts) h, avg(soc_pct), min(soc_pct) FROM telemetry_big WHERE tenant_id = '$T' AND ts >= '2026-09-02 00:00:00' AND ts < '2026-09-05 00:00:00' GROUP BY h ORDER BY h /*q_tenant_hourly_3d*/" q_tenant_hourly_3d),
$(q "SELECT uniqExact(vin) FROM telemetry_big WHERE tenant_id = '$T' AND soc_pct < 8 AND ts >= '2026-09-03 00:00:00' AND ts < '2026-09-03 01:00:00' /*q_low_soc_hour*/" q_low_soc_hour),
$(q "SELECT vin, max(odo_km) - min(odo_km) km FROM telemetry_big WHERE tenant_id = '$T' AND ts >= '2026-09-02 00:00:00' AND ts < '2026-09-03 00:00:00' GROUP BY vin ORDER BY km DESC LIMIT 10 /*q_km_per_vehicle_day*/" q_km_per_vehicle_day),
$(q "SELECT count(), avg(soc_pct), uniqExact(vin) FROM telemetry_big /*q_full_scan*/" q_full_scan)
]
JSON
)
python3 - "$ROWS" "$BYTES" "$COMP" "$WALL" "$S" "$OUT" <<PY
import json,sys
rows,b,c,wall,s,out=sys.argv[1:7]; rows=int(rows); b=int(b); c=int(c)
q=json.loads('''$Q''')
res={"synthetic_rows":rows,"vehicles":100000,"samples_per_vehicle":int(s),"generation_and_merge_seconds":int(wall),"bytes_on_disk":b,
 "bytes_per_row_on_disk":round(b/rows,2),"uncompressed_bytes_per_row":round(c/rows,2),"compression_ratio":round(c/b,2),"queries":q,
 "note":"synthetic data generated inside ClickHouse with the production schema/ordering/codecs; smooth synthetic signals compress better than real telemetry"}
json.dump(res,open(out,'w'),indent=2); print(json.dumps(res,indent=2))
PY
