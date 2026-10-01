# SQL optimisation (G9b)
Real `EXPLAIN (ANALYZE, BUFFERS)` plans before and after each change are in [`evidence/G9b/plans/sqlopt.txt`](../../evidence/G9b/plans/sqlopt.txt), produced by `tools/sqlopt/bench.sql` (`docker exec -i voltsight-postgres-1 psql -U voltsight -d voltsight < tools/sqlopt/bench.sql`) on a database with **1.5M alerts, 1.5M trips, 600K audit rows** for one tenant (box: 4 vCPU, parallelism disabled for comparability). The kept changes are migration `000004_performance`.

| # | Query | Change | Before | After |
|---|---|---|---|---|
| 1 | alert inbox, deep page | OFFSET 30,000 → keyset `(detected_at, id) <` + partial index `WHERE status='open'` | OFFSET: **1,306 ms**; keyset without index: 270 ms | **0.28 ms** |
| 2 | vehicle list with open-alert counts | per-row subquery pattern (N+1) → one query with `LEFT JOIN LATERAL` | — | `TestNoNPlusOneQueries`: **8 statements per request for 5 rows and for 200 rows** |
| 3 | trips of one vehicle in a window | composite index `(tenant_id, vin, started_at DESC)` | 0.67 ms | 0.19 ms |
| 4 | daily energy report | aggregate over 1.5M trips → materialised view `trip_daily` (unique index, refreshed by the batch job through a SECURITY DEFINER function; read through a tenant-filtered security-barrier view because materialised views cannot carry RLS) | **1,310 ms** | **0.024 ms** |
| 5 | audit by actor in the last hour | composite index `(tenant_id, actor, ts DESC)` (monthly partitions are pruned by the `ts` range) | 0.68 ms | 0.28 ms |
| 6 | RLS predicate form | bare `app_tenant()` vs `(SELECT app_tenant())` | 10.2 ms | 9.9 ms — **no meaningful difference** on this data; reported as measured |

API latency (G9b, `cmd/vsbench`, 16 concurrent clients, 20 s, mixed read workload over the 100K-vehicle dataset, ingest idle): 16,582 requests at 829 req/s, **p50 16.9 ms, p95 26.8 ms, p99 41.0 ms**, 0 errors (`evidence/G9b/api_latency.json`). Targets were p95 < 200 ms and p99 < 500 ms. Not measured while ingest runs at 100K ev/s.
