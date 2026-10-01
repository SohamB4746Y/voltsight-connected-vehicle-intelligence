# Gate G2 status: simulator

Criteria: [`docs/gates/G2.md`](../../docs/gates/G2.md). Targeted tests were run (per the time-critical build order); the full regression pass is deferred to the final verification phase.

| Criterion | Status | Evidence |
|---|---|---|
| G2.1 deterministic, shard-count independent | PASS | `TestDeterminismIndependentOfShards`: identical delivery digest for 1, 3, 8 and 2 shards with every injection enabled; different seed differs |
| G2.2 road graph connected, snapping correct | PASS | `internal/roadnet` tests (connectivity BFS, symmetric edges, nearest node equals brute force), 100% coverage |
| G2.3 encoders | PASS | hand-written protobuf decodes equal to the reference message; OEM A (v1, v2) and B are valid JSON with every field; charger-status encoding |
| G2.4 injections | PASS | duplicate/out-of-order/malformed rates within 10% of flags over 400K samples; corrupted payloads detected by contract validation; real out-of-order deliveries observed; outage flush burst (>= 1.4x fleet) and 3x sample-rate burst asserted |
| G2.5 physics | PASS | per-VIN `seq` monotonic, odometer monotonic, SoC in range, speeds plausible, positions inside the cities, SoC falls while driving and rises while charging, all key events occur, DTCs reported; **stress scenario produces strandings** (5 in 600 vehicles / 3 h with 97% of chargers out) |
| G2.6 ground truth | PASS | `vehicles.csv`, `trips.ndjson`, `charge_sessions.ndjson`, `strandings.ndjson` written; line counts equal the counters |
| G2.7 100K vehicles, throughput | PASS (MEASURED, see caveats) | below |

## G2.7 measurements (this machine: 16 logical cores, one process, null sink)
| Run | Events | Wall | Rate | Bytes/event | Heap |
|---|---|---|---|---|---|
| protobuf, 100K vehicles, 120 sim s, 07:30 start | 12.0 M | 1.27 s | **9.4 M events/s** | 100.4 | 204 MB |
| OEM JSON + faults (2% dup, 3% ooo, 0.2% malformed, 3x burst), 100K vehicles, 120 sim s | 14.2 M | 2.59 s | **5.5 M events/s** | 292.4 | 419 MB |

Files: `sim_benchmark_proto_peak.json`, `sim_benchmark_oemjson_peak_faults.json`. 100,000 vehicles x 5 s measured at 160 MB heap in `TestHundredThousandVehicles`.

## Caveats (stated, not hidden)
- These measure **generation and encoding only**, in one process with a discarding sink. They say nothing about gateway, Kafka or pipeline throughput (measured at G14). The target of > 150K events/s only establishes that the generator will not be the bottleneck for the 100K events/s requirement (real time = 100K/s).
- At a 07:30 start vehicles depart within the first 5 minutes, so a 2-minute window is not a full-driving steady state; the rate is dominated by encoding, not physics.
- Sample sizes in the physics test are small (600 vehicles); the stranding count is an existence proof, not a calibrated rate.
- Road graphs are synthetic grids with jitter; routes are Manhattan paths. Dijkstra/A* are implemented in the range-risk engine (M5), not here.
- Delivery to the gateway is implemented in M3.
