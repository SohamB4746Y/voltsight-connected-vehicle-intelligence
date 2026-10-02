# Gate G14 status (load, capacity) — PARTIAL

All runs: 4-vCPU / 15.7 GB Linux sandbox, **every component on one host** (simulator, gateway, worker, sink, alert service, Kafka, ClickHouse, Redis, PostgreSQL), so the load generator competes with the system under test. These are honest single-host measurements, not a capacity claim.

## 3× telemetry burst (40 s at 3 samples/vehicle/s inside a 100 s real-time run, 100,000 vehicles)
- events emitted 18,000,000; accepted by the gateway 18,000,000; to DLQ 0; sender gave up on 0; 429 retries 0; 503 retries 0; achieved 164,107 events/s over 110 s wall
- reconciliation: balanced = **True**; Kafka records 18,000,000; ClickHouse distinct 18,000,000 = worker distinct 18,000,000; lags 0/0

## Alert latency (G6.10 / N4), live pipeline at ~100K events/s, stress scenario
- 236 alerts; gateway-receive → alert **persisted in PostgreSQL**: p50 9.77 s, p95 13.73 s, p99 17.04 s, max 17.41 s (target p99 < 5 s: **NOT met** on this single oversubscribed host)
- **Context (separate 70 s run, worker only, no sink/alert service, `ingest_to_process_histogram.txt`):** gateway-receive → worker processing for sampled events: mean 1.87 s, 100% ≤ 5 s, 57% ≤ 2 s at 103K events/s with ~0 consumer lag — i.e. the latency is in the single oversubscribed host (gateway batch handling, Kafka acks, CPU shared with the simulator, Kafka, ClickHouse, Redis), not in consumer backlog. The full stack run above adds the ClickHouse sink and alert service to the same 4 cores.
- gateway-receive → decision in the worker: p50 9.47 s, p95 13.68 s, p99 16.92 s

## PostgreSQL vs ClickHouse insert (SC5)
```
pgvsch_stack_test.go:91: MEASURED (1000000 rows incl. client-side row generation): PostgreSQL COPY 97231 rows/s, 299.0 B/row on disk (with its primary-key index) | ClickHouse 405464 rows/s, 2.7 B/row | ClickHouse is 4.2x faster and 112.3x smaller
ok  	voltsight/internal/sink	14.033s
```

## ClickHouse history benchmark (SYNTHETIC rows generated inside ClickHouse, production schema/ordering/codecs)
- 29,200,000 rows (100,000 vehicles × 292 samples), 13.26 B/row on disk, compression 9.13×
- `q_vehicle_day`: 129 ms wall, 8,192 rows read
- `q_tenant_hourly_5h`: 234 ms wall, 4,874,240 rows read
- `q_low_soc_hour`: 176 ms wall, 4,874,240 rows read
- `q_km_per_vehicle_day`: 244 ms wall, 4,874,240 rows read
- `q_full_scan`: 720 ms wall, 29,200,000 rows read
- wall times include a `docker exec` client round trip (~100 ms); `rows_read` is from the query log (index pruning: a one-vehicle one-day scan reads 8,192 rows of 29.2M)
- the generator stopped at 29.2M rows because background merges exceeded the 3 GB container's memory limit (a limit this very benchmark exposed); smooth synthetic signals compress better (9.1×) than the simulated telemetry (6.8×, 17.3 B/row); this is **not** a billion-row run and not real data

## Not measured
- ≥ 100K events/s *sustained* beyond the 60 s run in `evidence/G4`; 5-minute 3× burst; soak (heap/RSS/lag trend); scale-out 1→2→4→8 workers; adding Kafka brokers; per-hop ceilings under isolation; billion-row batch scan; CPU/memory plots.
