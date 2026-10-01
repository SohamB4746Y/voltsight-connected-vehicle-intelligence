#!/usr/bin/env bash
# Runs every chaos scenario in sequence and collects the results (about 5 minutes each on a 4-vCPU box).
cd "$(dirname "$0")/.."
for s in ${SCENARIOS:-worker-kill sink-kill redis-restart clickhouse-restart kafka-restart gateway-kill}; do
  chaos/run.sh $s evidence/G13/$s 75 > tmp/chaos-$s.log 2>&1
done
python3 - <<'PY'
import json,glob
rows=[json.load(open(f)) for f in sorted(glob.glob('evidence/G13/*/result.json'))]
json.dump(rows,open('evidence/G13/summary.json','w'),indent=2)
for r in rows: print(r['scenario'], 'no_loss=',r['no_loss'], 'live=',r['live_distinct_rows'], 'clean=',r['clean_replay_distinct_rows'], [l for l in r['events_log'] if 'recovered' in l])
PY
