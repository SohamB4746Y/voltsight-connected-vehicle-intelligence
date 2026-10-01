#!/usr/bin/env bash
# G14 measurements on a quiet host, in sequence: (1) 3x burst, (2) alert latency under a stress scenario,
# (3) PostgreSQL-vs-ClickHouse insert comparison, (4) ClickHouse history benchmark on synthetic rows.
cd "$(dirname "$0")/../.."
mkdir -p evidence/G14 tmp
# (1) 3x telemetry burst for 40 s in the middle of a 100 s run
tools/live/e2e.sh evidence/G14/burst_3x 100000 100 -burst-at 20 -burst-duration 40 -burst-mult 3 > tmp/g14-burst.log 2>&1
# (2) alert latency: low batteries + 99% charger outage, 5 minutes
tools/live/e2e.sh evidence/G6/live_alert_latency 100000 300 -low-soc-fraction 0.3 -low-soc-min 0.101 -low-soc-max 0.13 \
  -charger-outage-at 60 -charger-outage-duration 1200 -charger-outage-fraction 0.99 > tmp/g14-latency.log 2>&1
# (3) store comparison
go test -tags stack -count=1 -v -run PostgresVsClickHouse ./internal/sink 2>&1 | grep -E "MEASURED|FAIL|ok" > evidence/G14/pg_vs_clickhouse.txt
# (4) history benchmark: 50M synthetic rows
tools/sqlopt/ch_bench.sh 500 evidence/G14/ch_bench.json > tmp/g14-chbench.log 2>&1
echo done > tmp/g14.done
