# Gate G6 status (M5): PARTIAL — engine, alerts and detection quality built and tested; live latency not measured

Criteria: [`docs/gates/G6.md`](../../docs/gates/G6.md).

| Criterion | Status | Evidence |
|---|---|---|
| G6.1 overlay == independent reference (random graphs incl. disconnected, co-located sources) | PASS | `internal/rangerisk/overlay_test.go` (property test, 120 random graphs) |
| G6.2 incremental add/remove == full rebuild | PASS | same file, 60 random status-change walks × 40 steps |
| G6.3 A* length == Dijkstra, expands fewer nodes | PASS | MEASURED: 47,644 vs 161,701 expansions over 80 routes (3.4× fewer) |
| G6.4 O(1) lookup | PASS (MEASURED, this 4-vCPU sandbox) | Lookup 3.6 ns/op, 0 allocs; full rebuild 48.4K nodes/300 chargers 10.1 ms; remove+add one charger 70.7 µs |
| G6.5 tenant visibility | PASS | `TestTenantNeverRoutesToAnotherTenantsDepot` |
| G6.6 decision rule, hysteresis, deterministic windows | PASS | `processor_test.go` (critical / warning / charging-silent / one-alert-per-window / replay keys / hysteresis / unknown vehicle / EWMA learning) |
| G6.7 charger outage changes the decision | PASS at unit level (`TestChargerOutageChangesTheDecision`); the Kafka status feed (`StatusFeed`) is built but its live behaviour was **not** exercised |
| G6.8 alert idempotency + tenant isolation | PASS | `internal/alerts/service_stack_test.go`: 1 alert delivered 5× + 1 garbage record → 1 row, 4 duplicates counted, 1 decode error, Redis message received, other tenant sees 0 rows via the app role |
| G6.9 detection quality vs ground truth | MEASURED (stress scenario) | below |
| G6.10 live ingest→alert latency p99 < 5 s | **NOT MEASURED** | the live run was aborted by the user's change of priorities; `tools/live/e2e.sh` + `vsalerts -latency-log` are ready to produce it (G14) |

## G6.9 detection quality (MEASURED; simulated world, declared limitation)
`go run ./cmd/vseval -vehicles 1500 -hours 4 -low-soc-fraction 0.4 -unaware 0.5 -charger-outage-fraction 0.97 -outage-at-hours 0.17 -outage-hours 4` — 21.6M telemetry events, 25 ground-truth strandings (all by drivers who ignore low-battery warnings). Alert is correct if the vehicle strands within 2 h. Thresholds were set by design, **not tuned on this run**. Result file: `detection_quality_stress.json`.

| Detector | Recall of strandings | Precision | Median / p10 lead time | Vehicles alerted |
|---|---|---|---|---|
| baseline (catalogue capacity & consumption) + overlay | 100% (25/25) | 0.34 | 30 min / 16 min | 74 |
| **per-vehicle EWMA %SoC/km** + overlay | 100% (25/25) | 0.29 | **53 min / 29 min** | 85 |
| plain "SoC < 15%" threshold | 92% (23/25) | 0.07 | 91 min / 49 min | 323 |

Reading: the graph-aware rules alert ~4× fewer vehicles than the SoC threshold at higher precision and never missed a stranding in this run; the EWMA estimator buys ~23 min of extra lead time over the baseline for ~11 more alerted vehicles. A less severe scenario (0.9 outage, 3000 vehicles, 8 h) produced no strandings at all, so there is nothing to score there. Precision is a strict lower bound on usefulness (a correct early warning that makes no difference to an unaware driver counts as a false positive in the simulator only because the simulated driver never reacts).

## Built
`internal/rangerisk` (overlay, A*, engine with per-(city,tenant) overlays, estimators, processor, Kafka publisher + charger-status feed), `internal/alerts` + `cmd/vsalerts` (alerts.v1 → PostgreSQL idempotent + Redis pub/sub), `cmd/vsworker` runs the engine in the stream path (`-risk`, `-estimator`), `cmd/vseval`.
