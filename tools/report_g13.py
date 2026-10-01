"""Renders evidence/G13/status.md from the scenario result files (missing scenarios are listed as NOT RUN)."""
import json, os
SC = ["worker-kill", "sink-kill", "redis-restart", "clickhouse-restart", "kafka-restart", "gateway-kill"]
rows, lines = [], []
for s in SC:
    p = f"evidence/G13/{s}/result.json"
    if not os.path.exists(p):
        rows.append(f"| {s} | **NOT RUN / not completed** | | | |")
        continue
    r = json.load(open(p))
    rec = next((l.split(": ")[1] for l in r["events_log"] if "recovered" in l), "n/a")
    rows.append(f"| {s} | {'**PASS**' if r['no_loss'] else '**FAIL**'} | {r['live_distinct_rows']:,} = {r['clean_replay_distinct_rows']:,} (lost {r['lost_vs_clean_replay']}) | {r['sim_failed']} | {rec} s |")
out = """# Gate G13 status (chaos / resilience) — PARTIAL
Method (`chaos/run.sh`): 100,000 vehicles in real time for 75 s on the 4-vCPU sandbox (everything on one host, single broker/ClickHouse/Redis); one component is killed or restarted at +30 s; after recovery, the table written by the *faulted live path* must contain exactly the distinct events of a **clean replay of the whole Kafka log** into a second table (ClickHouse FINAL counts). "Recovery" = both consumer groups back under 20,000 records of lag.

| Scenario | Result | Live distinct rows = clean replay | Events the sender gave up on | Recovery to lag < 20K |
|---|---|---|---|---|
""" + "\n".join(rows) + """

## Findings
- **Defect found and fixed by this method:** in the first `redis-restart` run the stream worker exited on `LOADING Redis is loading the dataset in memory` and the consumer group stalled (evidence kept in `redis-restart-BEFORE-FIX-worker-died/`). Workers and the alert service now retry transient dependency failures with bounded back-off and commit offsets only after the state/rows are saved. The scenario was re-run after the fix.
- Earlier, separately: the ClickHouse sink survives a ClickHouse restart (`TestSinkSurvivesClickHouseRestartWithoutLoss`), and a worker killed mid-stream converges to the uninterrupted state (G4.6).

## Limits (honest)
- Single-node stores locally: this proves *no loss and recovery* of the pipeline, not high availability — there is no replicated Kafka/Redis/PostgreSQL/ClickHouse here (LIMITATION; replication exists only in the Terraform/Helm design).
- Not run: PostgreSQL primary failover and restore drill, Keycloak/ML/LLM outages, network partitions (toxiproxy), pod kills on Kubernetes, availability computed from probes (N9), the 99.9% target.
- Recovery times are from an oversubscribed 4-vCPU host (the catch-up competes with the live 100K ev/s producer) and are not representative of production hardware.
"""
open("evidence/G13/status.md", "w").write(out)
print(out)
