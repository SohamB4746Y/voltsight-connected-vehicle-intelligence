# Gate G4/G5 status (M4): PASS for G4.1–G4.9 (live run on the 4-core sandbox, see limitations); Parquet archive deferred to M7

Criteria: [`docs/gates/G4.md`](../../docs/gates/G4.md). Commands to reproduce: `make up bootstrap seed` then `tools/live/e2e.sh <dir> 100000 60 -dup-rate 0.02 -ooo-rate 0.03 -fault-rate 0.002` (`ADVERSARIAL=1` adds a heavy ClickHouse aggregation loop).

| Criterion | Status | Evidence |
|---|---|---|
| G4.1 exact dedup window | PASS | property test vs reference set (`internal/dedup`), 4.3 ns/event (measured on the original Windows dev machine) |
| G4.2 Bloom filter | PASS | measured false-positive rate 0.0096 vs 0.010 design; no false negatives |
| G4.3 Count-Min / top-K | PASS | never under-counts; 0 keys over eps*N; top-10 recall >= 9/10 |
| G4.4 worker correctness | PASS | real Kafka + Redis: verdict totals equal the reference model |
| G4.5 replay identical state | PASS | wipe Redis, new group from earliest offset, identical state hash (re-run in this session after the offset-default change) |
| G4.6 crash safety | PASS | killed mid-stream, restart converges to the clean-run state (re-run) |
| G4.7 sink | PASS | rows == records, FINAL collapses duplicates, enum/array mapping; 453K rows/s insert-time throughput (this sandbox); **new:** `TestSinkSurvivesClickHouseRestartWithoutLoss` (ClickHouse restarted mid-run, 5 retried attempts, 200,000/200,000 distinct events present) |
| G4.8 reconciliation | PASS | `vsrecon` now checks worker processed == Kafka records, ClickHouse FINAL rows == worker new+late+stale, both lags 0: `live_fixed_faults_adversarial/recon.out` |
| G4.9 lag bounded at ~100K events/s | PASS (MEASURED, 60 s, 4-core sandbox) | below |

## G4.9 measurement (MEASURED, not extrapolated)
Environment: 4 vCPU / 15.7 GB Linux sandbox, Docker 29.6, **everything on one host** (simulator, gateway, worker, sink, Kafka, ClickHouse 3 GB, Redis, PG, Vault); host load average peaked at 14 on 4 cores, so the CPU was oversubscribed. 100,000 vehicles, real-time pacing, 60 s, fault injection (2% duplicates, 3% out-of-order, 0.2% malformed).
- Simulator->mTLS gateway->Kafka: 6,012,412 events at **98.3K events/s** (61.2 s wall), 6,000,557 accepted, 11,855 to the DLQ, 0 failed, 0 throttled.
- Stream worker: processed all 6,000,557; max consumer lag 5,332 records (~0.05 s of traffic).
- Sink: drained to 0 lag (its in-process lag gauge peaked at 766 records); ClickHouse FINAL = 5,897,756 distinct events = worker new (5,810,387) + late (87,369); 102,801 duplicates identified by the worker = 6,000,557 − 5,897,756.
- ClickHouse (3 GB container): max tracked memory 706 MB during the run, container cgroup max 1,747 MB (includes page cache), with a concurrent `uniqExact(tenant_id,vin,ts,seq)` over the growing table every 3 s: 0 failures (`adversarial.log`).
- This is a 60-second run. It is not the 1x sustained / 3x burst / soak evidence required by G14, which is still to be produced. The earlier 98.7K/s run of this gate was on a 14-CPU Windows machine; the two are not comparable.

## The defect, honestly (diagnosis and what was and was not confirmed)
1. **Original symptom** (previous session, Windows/WSL2): the sink died at ~3.0M rows with `memory limit exceeded: would use 2.70 GiB` against a 3 GB container. **I could not reproduce that exact failure** with the original configuration on this machine, even with the container squeezed to 1 GB (`live_repro_1g_unfixed`, `live_repro_1g_faults`: peak insert 34 MiB, peak merge 80 MiB, tracked memory <= 411 MB). The exact trigger on the Windows host is therefore NOT confirmed.
2. **What is confirmed:** (a) ClickHouse enforces a *total* server memory limit on RSS, and nothing capped an individual query: a plain `SELECT uniqExact(tenant_id, vin, ts, seq)` over 5.9M rows on a 1 GB container fails with `(total) memory limit exceeded ... current RSS 924 MiB, maximum 921 MiB` — any analytic query running next to the sink can push the server over its limit and make the sink's inserts fail. (b) The shipped defaults (mark cache 5 GB, uncompressed cache 8 GB, background pool 16) assume a large host. (c) The sink exited on the first insert error instead of retrying.
3. **A second, worse defect found while reproducing — silent data loss:** a consumer group with no committed offset used `AtEnd`, which is resolved when partitions are assigned, i.e. after the simulator had already started producing. In `live_repro_1g_faults` the sink skipped the first ~7 s of the stream (ClickHouse held 5,155,742 rows; a replay of the same topic yields 5,897,756 distinct events, so ~742K events were never stored) while the group reported **lag 0**. The old reconciliation (`rows == records`) was also wrong whenever duplicates exist. Both the worker and the sink had the same default.
4. **Fixes:** new groups start at the earliest retained offset (`-from-end` is an explicit opt-in); ClickHouse mounted config `deploy/compose/config/clickhouse/` (server ceiling 80% of the container, mark cache 256 MB, no uncompressed cache, background pool 8, per-query `max_memory_usage` 1 GiB with spill-to-disk for GROUP BY/ORDER BY); the sink retries any failed insert/commit with capped exponential back-off and jitter for up to 10 minutes and commits offsets only after an acknowledged insert (data errors stay fatal); `vsrecon` reconciles distinct events instead of raw rows.
5. **Not verified:** that the per-query cap alone would have prevented the original Windows crash.

## Environment notes
- Hard `RLIMIT_NOFILE` is 20,000 in this sandbox: `deploy/compose/docker-compose.lowulimit.yml` lowers ClickHouse's ulimit; Docker Hub rate-limits the sandbox proxy, so images were pulled from `mirror.gcr.io` and retagged.
- CI for the M2–M4 commits: runs 4 (`dd56cea`, G1 gate incl. Linux Testcontainers/Vault/Keycloak), 5 (M3) and 6 (M4, `ea0e5c9`) all concluded **success** on GitHub Actions. Run 3 (`a95f618`) had failed (G0 regression step on a CI runner) and was fixed by `dd56cea`. So G1.12b is PASS.

## Not done in this milestone
Parquet cold archive (written with M7); wiring `vsworker`/`vssink`/`vsrecon` into `make`.
