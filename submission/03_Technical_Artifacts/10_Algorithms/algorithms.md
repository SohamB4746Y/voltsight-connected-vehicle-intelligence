# Algorithms, complexity and measurements
Every algorithm below is used in the product (not decoration). "MEASURED" numbers come from the committed tests/benchmarks on the stated machine; the first two machines differ: *Win* = the 14-CPU Windows development laptop (M0–M4), *box* = the 4-vCPU Linux sandbox (M5 onward).

| Algorithm | Job in the product | Time / space | Verification and measurement |
|---|---|---|---|
| **Multi-source reverse Dijkstra** (binary heap) over a 48,400-node CSR road graph | distance from every node to the nearest *available* charger usable by a tenant, per (city, tenant) | O((V+E) log V) rebuild, O(V) space | property test vs an independent O(V²) Dijkstra on 120 random graphs (incl. disconnected, co-located sources). MEASURED (box): full rebuild 10.1 ms (48K nodes, 300 chargers) |
| **Incremental overlay update** (remove: invalidate the charger's catchment, re-seed from valid neighbours; add: relax from the new source) | charger outage/restore reflected in decisions without a rebuild | proportional to the catchment | 60 random status walks × 40 steps == full rebuild. MEASURED (box): remove+add 70.7 µs |
| **O(1) lookup** | per-event decision | O(1) | MEASURED (box): 3.6 ns/op, 0 allocs |
| **A\*** (admissible straight-line heuristic) | route polyline in alert evidence | O(E log V) worst | equals Dijkstra length on 80 routes. MEASURED (box): 3.4× fewer node expansions (47,644 vs 161,701) |
| Geohash + O(1) node snap | position → graph node; map cells; depot clustering | O(1) | tests; neighbours validated across cell borders |
| **Per-VIN exact sequence window** (256-bit bitmap) | dedup without false drops, tolerates reordering | O(1)/event, 32 B/VIN | property test vs a reference set; MEASURED (Win): 4.3 ns/event |
| **Rotating Bloom filter** | negative cache for events older than the window | O(k)/event, ~10 bits/element | MEASURED (Win): false-positive rate 0.0096 vs 0.0100 design; no false negatives; a positive is never used to drop |
| **Count-Min sketch + heap** | top-K diagnostic codes | O(d)/update | test: never under-counts, 0 keys over ε·N, top-10 recall ≥ 9/10 |
| **EWMA of %SoC per km** | per-vehicle consumption → range | O(1)/event | decision-level evaluation (G6.9) |
| **Dynamic programming over (slot × energy level)** with a charge-curve taper and charger-occupancy mask | minimum-cost charging under time-of-use tariffs | O(T·S·A) per vehicle | property test: feasible and never more expensive than charge-on-plug-in (392 random cases; strictly cheaper in 389; MEASURED total cost reduction 41.8% on random synthetic tariffs). MEASURED (box): 0.53 ms per vehicle (14 h, 15-min slots). Synthetic depot: 45% cheaper than plug-in charging |
| **Stop/trip segmentation** (speed hysteresis + dwell) | GPS/speed → trips | O(n) | MEASURED (simulator truth, box): precision 0.911, recall 1.000, F1 0.954 on 1,962 trips |
| **Douglas-Peucker** | polyline simplification | O(n log n) avg | unit test (shape and end points kept) — not used in the live UI yet |
| **Union-Find on geohash cells** | clusters of stops at unapproved locations | O(n·α(n)) | planted-cluster test; run over live data not evaluated |
| **Charge-session capacity estimate** (∫\|V·I\| dt / ΔSoC, median) | battery state of health | O(n) | MEASURED (simulator truth): MAPE 0.51%, p90 1.16% on 358 vehicles — *optimistic*: the assumed 0.93 charge efficiency equals the simulator's hidden constant |
| **VIN check digit / DTC parser** | normalisation | O(1) | exhaustive DTC round trip; VIN property tests |
| **Keyset pagination** | API lists | O(log n) seek | `TestKeysetPagination`; 0.28 ms vs 1,306 ms for OFFSET (`docs/database/SQL_OPTIMIZATION.md`) |
| **Feature-hashing embedder** + HNSW cosine | similar-incident search | O(len) embed; HNSW ~O(log n) | recall@5 0.958 on a labelled paraphrase set (lexical; limitation) |
| **Gradient-boosted trees** (scikit-learn HistGB) | trip consumption | — | MAPE 7.17% vs 11.52% best baseline (vehicle-level hold-out), paired-bootstrap CI excludes 0 (`evidence/G8`) |

## Detection quality (G6.9, simulator ground truth, stress scenario)
25 strandings in 21.6M events. Alert correct if the vehicle strands within 2 h. Not tuned on this run.
| Detector | Recall | Precision | Median lead |
|---|---|---|---|
| catalogue estimator + overlay | 100% (25/25) | 0.34 | 30 min |
| **EWMA estimator + overlay** | 100% (25/25) | 0.29 | **53 min** |
| plain "SoC < 15%" | 92% | 0.07 | 91 min |
Declared limitation: the world is simulated; hidden terms (payload, driver style, state of health) are not visible to the estimators, but absolute numbers do not transfer to real fleets.
