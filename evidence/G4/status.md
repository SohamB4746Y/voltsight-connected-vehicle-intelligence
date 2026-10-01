# Gate G4/G5 status (M4): PARTIAL

Criteria: [`docs/gates/G4.md`](../../docs/gates/G4.md). Targeted tests only; no full regression run yet.

| Criterion | Status | Evidence |
|---|---|---|
| G4.1 exact dedup window | PASS | property test vs reference set (`internal/dedup`), 4.3 ns/event |
| G4.2 Bloom filter | PASS | measured false-positive rate 0.0096 vs 0.010 design (theory 0.0100); no false negatives |
| G4.3 Count-Min / top-K | PASS | never under-counts; 0 keys over eps*N; top-10 recall >= 9/10 |
| G4.4 worker correctness | PASS | real Kafka + Redis: verdict totals equal the reference model (new/late/duplicate/stale) |
| G4.5 replay identical state | PASS | wipe Redis, new group from earliest offset, identical state hash |
| G4.6 crash safety | PASS | killed after 16,500 of 63,068 events; restart converges to the clean-run state |
| G4.7 sink | PASS | rows == records, FINAL collapses duplicates to distinct events, enum/array mapping; 344K rows/s insert (insert time only) |
| G4.8 reconciliation | PARTIAL | `vsrecon` balances Kafka vs DLQ and the worker; the sink side could not be completed (below) |
| G4.9 lag bounded at 100K events/s | **FAIL for the sink, PASS for the worker** | see below |

## Live run (real binaries, 100,000 vehicles, real time, 60 s)
Simulator -> mTLS gateway -> Kafka: 6,012,412 events at **99.8K events/s**, 6,000,557 accepted, 11,855 to the DLQ, 0 failed, 0 throttled. Stream worker processed all 6,000,557 (lag <= 2K). `evidence/G4/e2e_realtime_100k.json`, `e2e_reconciliation.json`.

## Open defect: ClickHouse sink died mid-run
At ~3.0M rows the insert failed with `memory limit exceeded: would use 2.70 GiB` (container limit 3 GB, `max_server_memory_usage` 2.9 GB, default `mark_cache_size` 5 GB / `uncompressed_cache_size` 8 GB assume a large host); the sink process exited and its lag grew to 2.9M records. Post-mortem: tracked memory 304 MB, resident 1.1 GB, 7 parts, so the peak was transient. Root cause not yet confirmed. Next steps: reduce CH cache sizes and background pool in a mounted config, cap `max_insert_block_size`, make the sink retry with back-off instead of exiting, rerun the live test. Until then G4.9 and G4.8 stay open.

## Not done in this milestone
Parquet cold archive; wiring `cmd/vsworker`/`vssink`/`vsrecon` into `make`; CI confirmation of the M2-M4 commits was not read.
