# Capacity analysis (measured sizes → extrapolation)
Labels: **MEASURED** = produced by a committed run; **EXTRAPOLATED** = arithmetic on measured sizes (no run at that scale).

## Measured sizes
| Quantity | Value | Source |
|---|---|---|
| Wire size per event, protobuf (what the gateway forwards) | **98–100 B** | `evidence/G2/sim_benchmark_proto_peak.json` (100.4 B), `evidence/G4/e2e_realtime_100k.json` (98.7 B) |
| Wire size per event, OEM JSON dialects (with injected faults) | **292 B** | `evidence/G2/sim_benchmark_oemjson_peak_faults.json` |
| The brief's example event (~1 KB JSON) | ~1,000 B (the brief's figure; our dialects are more compact) | brief §4 |
| ClickHouse on disk, `telemetry_raw` (7.5M simulated rows, ZSTD + Gorilla/DoubleDelta codecs) | **17.3 B/row**, compression ratio 6.8× | `system.parts` after the chaos runs (`telemetry_raw`, `telemetry_chk` both 17.3–17.4 B/row) |

## Extrapolation to 100,000 events/s (EXTRAPOLATED)
| | Per second | Per day |
|---|---|---|
| Ingest wire, protobuf @ 99 B | 9.9 MB | ≈ 0.86 TB |
| Ingest wire, OEM JSON @ 292 B | 29 MB | ≈ 2.5 TB |
| Ingest wire, the brief's ~1 KB JSON | 100 MB | ≈ 8.6 TB (the brief's own figure) |
| ClickHouse, full resolution @ 17.3 B/row | 1.7 MB | ≈ 150 GB/day → ≈ 4.5 TB for the 30-day warm tier |
| Kafka, 72 h retention, protobuf before compression/replication | | ≈ 2.6 TB (× replication factor 3 = 7.8 TB; zstd would reduce it) |
Caveats: simulated telemetry is smoother than real telemetry, so real compression will be worse; Kafka record overhead (keys, headers, batching) is not included in the 99 B; 17.3 B/row comes from a 7.5M-row table, not from a multi-week one.

## Lifecycle (design, partially implemented)
Hot: Kafka 72 h + Redis. Warm: ClickHouse, TTL 30 days (`internal/sink` DDL; enforced by ClickHouse, no accelerated-clock test). Cold tier (Parquet on object storage, coarsened location) and a PostgreSQL audit purge are **not built**. A cloud cost model from list prices was **not produced** (no price sources were gathered).

## What was measured at scale on this machine
Ingest through the gateway at ~266K events/s (6.0M events in 22.6 s, generator-limited, 4 vCPU), end-to-end 98.3K events/s for 60 s (see `evidence/G4`), ClickHouse insert 453K rows/s (insert time only). One host cannot reproduce a production deployment; horizontal scaling is by design (stateless services, 64 partitions) and **not measured** (`evidence/G14/status.md`).
