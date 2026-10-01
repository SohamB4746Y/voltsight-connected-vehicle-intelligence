# Gate G13 status (chaos / resilience) — PARTIAL
Method (`chaos/run.sh`): 100,000 vehicles in real time for 75 s on the 4-vCPU sandbox (everything on one host, single broker/ClickHouse/Redis); one component is killed or restarted at +30 s; after recovery, the table written by the *faulted live path* must contain exactly the distinct events of a **clean replay of the whole Kafka log** into a second table (ClickHouse FINAL counts). "Recovery" = both consumer groups back under 20,000 records of lag.

| Scenario | Result | Live distinct rows = clean replay | Events the sender gave up on | Recovery to lag < 20K |
|---|---|---|---|---|
| worker-kill | **PASS** | 7,500,000 = 7,500,000 (lost 0) | 0 | 84 s |
| sink-kill | **PASS** | 7,500,000 = 7,500,000 (lost 0) | 0 | 86 s |
| redis-restart | **PASS** | 7,500,000 = 7,500,000 (lost 0) | 0 | 72 s |
| clickhouse-restart | **PASS** | 7,500,000 = 7,500,000 (lost 0) | 0 | 68 s |
| kafka-restart | **PASS** | 7,500,000 = 7,500,000 (lost 0) | 0 | 97 s |
| gateway-kill | **NOT RUN** (script supports it; skipped for time) | | | |

## Findings
- kafka-restart: 469 duplicate records appeared in Kafka (sender retries after 5xx while the broker was down; 48 retried requests, 0 given up) and were collapsed by the idempotent sink path — live distinct rows still equal the clean replay.
- **Defects found and fixed by this method (2):** (1) in the first `kafka-restart` run `vsworker` exited on a failed offset commit (`kafka-restart-BEFORE-FIX-worker-died/`) — commits are now retried; (2) in the first `redis-restart` run the stream worker exited on `LOADING Redis is loading the dataset in memory` and the consumer group stalled (evidence kept in `redis-restart-BEFORE-FIX-worker-died/`). Workers and the alert service now retry transient dependency failures with bounded back-off and commit offsets only after the state/rows are saved. The scenario was re-run after the fix.
- Earlier, separately: the ClickHouse sink survives a ClickHouse restart (`TestSinkSurvivesClickHouseRestartWithoutLoss`), and a worker killed mid-stream converges to the uninterrupted state (G4.6).

## Limits (honest)
- Single-node stores locally: this proves *no loss and recovery* of the pipeline, not high availability — there is no replicated Kafka/Redis/PostgreSQL/ClickHouse here (LIMITATION; replication exists only in the Terraform/Helm design).
- Not run: gateway-kill, PostgreSQL primary failover and restore drill, Keycloak/ML/LLM outages, network partitions (toxiproxy), pod kills on Kubernetes, availability computed from probes (N9), the 99.9% target.
- Recovery times are from an oversubscribed 4-vCPU host (the catch-up competes with the live 100K ev/s producer) and are not representative of production hardware.
