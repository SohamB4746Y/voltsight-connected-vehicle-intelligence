# Chaos and failure results

Source of truth: `evidence/G13/status.md` (method `chaos/run.sh`, summary `evidence/G13/summary.json`) and `evidence/live-deployment/restart-recovery.txt`, `full-restart.txt`. Host: 4 vCPU / 15 GiB Linux sandbox with every component on one host (single broker, single ClickHouse/Redis/PostgreSQL); load: 100,000 simulated vehicles in real time for 75 s, fault injected at +30 s. "Lost" is measured as live distinct rows in ClickHouse versus a clean replay of the entire Kafka log.

| Failure injected | Expected | Actual | Data loss | Recovery to lag < 20K | Issue found → fix → rerun |
|---|---|---|---|---|---|
| Stream worker killed | consumer group resumes, no loss | as expected | 0 of 7,500,000 | 84 s | none |
| ClickHouse sink killed | offsets uncommitted, replay on restart | as expected | 0 of 7,500,000 | 86 s | none |
| Redis restarted | worker rides over the `LOADING` window | **first run: worker exited** on `LOADING` → bounded retry added → rerun | 0 of 7,500,000 | 72 s | yes, fixed, re-run passed |
| ClickHouse restarted | sink retries with back-off | as expected | 0 of 7,500,000 | 68 s | none |
| Kafka broker restarted | producers retry, workers resume | **first run: worker died** on a failed offset commit → commits retried (also in the alert service) → rerun; 469 duplicate records from sender retries collapsed by the idempotent path | 0 of 7,500,000 | 97 s | yes, fixed, re-run passed |
| Gateway killed | senders retry | **NOT RUN** | – | – | – |
| PostgreSQL restart / failover | writes fail closed, stream continues | **NOT RUN** (stack restart exercised it incidentally: all data survived, `full-restart.txt`) | not assessed | – | – |
| Whole-stack stop/start (live profile) | state survives, stream resumes | PostgreSQL (alerts, 100,000 vehicles), Keycloak realm, ClickHouse history survived. **Found:** restarted simulator numbered events from 1 again, so every event was classified stale and the demo went dead → `vssim -seq-base=-1` → rerun: `new` events resumed, `stale` stayed frozen | none after fix | ~minutes (cold start) | yes, fixed, re-run passed |
| Single-service restarts (API, proxy, Keycloak) | seconds | API/proxy < 1 s polling resolution, Keycloak 13 s | – | – | none |
| Network partition, toxiproxy, pod kill on Kubernetes | – | **NOT RUN** | – | – | – |

Limits: replicated stores were not exercised (no HA locally), recovery times come from an oversubscribed host and are not representative of production hardware, the 99.9 % availability target was not measured.
