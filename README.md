# VoltSight — EV fleet range-risk & charge orchestration

Tells every EV in a **100,000-vehicle** connected fleet whether it can still reach a charger — within seconds of the telemetry that changes the answer — and schedules charging at the lowest time-of-use cost. Built for the Connected Vehicle Intelligence hackathon; Motorq is named only as an industry reference in the problem statement and this project has no affiliation with it. **All data is synthetic.**

> **How to read this repository.** Every capability is backed by a committed test or measurement under [`evidence/`](evidence/); anything not verified is listed as such in [`docs/requirements-status.md`](docs/requirements-status.md). Headline numbers below say *where* they were measured (a 4-vCPU Linux sandbox unless stated) — they are not extrapolations.

## What it does (all running, all real data)
- **Simulator** (`vssim`): deterministic physics for 100,000 vehicles on three synthetic city road graphs — trips, shifts, charging curves, strandings, battery faults, GPS noise, duplicates, out-of-order, outages, two OEM dialects + a schema roll-out, malformed payloads.
- **Secure ingest** (`vsgateway`): HTTPS/2 + **mutual TLS 1.3**, tenant taken from the client certificate, VIN/DTC validation, per-event DLQ with reason, back-pressure (429 + Retry-After).
- **Real-time path** (`vsworker`): exact per-VIN dedup window, Bloom negative cache, latest state in Redis, and the **range-risk engine** — a multi-source Dijkstra overlay (distance to the nearest *available* charger per tenant, updated incrementally when a charger fails) read in O(1) per event, EWMA range estimator, hysteresis, deterministic alert windows → `alerts.v1` → PostgreSQL (idempotent) → Redis → SSE.
- **History & batch**: ClickHouse (`vssink`), trips from noisy GPS, battery state-of-health from charging sessions, a GBM consumption model vs baselines, time-of-use **charge-plan optimiser (DP)**.
- **Platform API + web console**: Keycloak OIDC (PKCE), RBAC + subscription entitlements, PostgreSQL row-level security, keyset pagination, rate limiting, audit trail with a **hash chain**, role-based location masking, GDPR/DPDP erasure workflow.
- **Charge Ops Copilot**: 8 typed tools, guardrails outside the model, citations or "insufficient evidence", two-phase human-approved writes, pgvector incident search, every step audited.

## Measured results (and where)
| What | Result | Evidence |
|---|---|---|
| End-to-end real-time run, 100K vehicles, 60 s, with 2% duplicates / 3% out-of-order / 0.2% malformed | **98.3K events/s**, 6.0M events, 0 failed, worker lag ≤ 5.3K, ClickHouse = worker distinct events exactly (5,897,756), heavy analytical query running concurrently | [`evidence/G4`](evidence/G4/status.md) |
| Bug found by that run: silent loss of the first ~7 s when a consumer group started at the log end (lag reported 0) — fixed, reconciliation now checks distinct events | | [`evidence/G4/status.md`](evidence/G4/status.md) |
| Range-risk detection vs simulator truth (stress scenario, 25 strandings) | EWMA estimator: recall 100%, precision 0.29, median lead **53 min** (SoC<15% rule: recall 92%, precision 0.07) | [`evidence/G6`](evidence/G6/status.md) |
| Overlay lookup / rebuild / incremental update | **3.6 ns** / 10.1 ms (48K nodes) / 70.7 µs | `internal/rangerisk` benchmarks |
| Charge optimiser | cheaper than charge-on-plug-in in 389 of 392 random cases (41.8% lower total cost); 0.53 ms per vehicle | `internal/chargeplan` tests |
| Consumption model (vehicle hold-out) | GBM MAPE **7.2%** vs 11.5% best baseline, CI excludes 0 | [`evidence/G8`](evidence/G8/ml_report.json) |
| Battery SoH from charging sessions / trip F1 (simulator truth) | MAPE 0.5% (optimistic: assumed efficiency equals the simulator's) / F1 0.954 | `internal/batch` tests |
| API latency, 100K-vehicle PostgreSQL, 16 clients | p50 16.9 / **p95 26.8** / p99 41.0 ms, 829 req/s (ingest idle) | [`evidence/G9b`](evidence/G9b/api_latency.json) |
| SQL tuning on 1.5M alerts/trips | inbox 1,306 → 0.28 ms; report 1,310 → 0.024 ms | [`docs/sql-optimisation.md`](docs/sql-optimisation.md) |
| Chaos: kill/restart one component mid-run at 100K vehicles; the faulted path must equal a clean replay of the log | worker-kill, sink-kill, redis-restart, clickhouse-restart, kafka-restart: **0 events lost** (7.5M = 7.5M each); the method found and fixed 2 real defects (worker exited on a Redis restart / on a failed commit during a broker restart) | [`evidence/G13/status.md`](evidence/G13/status.md) |
| Security scans | gitleaks clean, govulncheck clean, npm audit 0, semgrep 1 waived, Trivy fs clean after fixes/waivers | [`evidence/G12/status.md`](evidence/G12/status.md) |

| 3× burst (40 s inside a 100 s run, 100K vehicles) | 18.0M events, **0 lost**, no 429s, books balance | [`evidence/G14`](evidence/G14/status.md) |
| **Alert latency (target p99 < 5 s): NOT met on this host** | receive→persisted p50 9.8 s / p99 17.0 s with the whole stack on one 4-vCPU box; worker-only ingest→process mean 1.9 s, all ≤ 5 s | [`evidence/G14`](evidence/G14/status.md) |
| PostgreSQL vs ClickHouse insert (1M rows) | 97K vs 405K rows/s (4.2×); simulated telemetry 17.3 B/row in ClickHouse | `evidence/G14/pg_vs_clickhouse.txt` |

**Not measured / not done** (full list in [`docs/requirements-status.md`](docs/requirements-status.md)): ≥100K ev/s sustained beyond 60 s, 3× burst for 5 minutes, soak, scale-out, billion-row benchmark, DAST, container image build/scan, Kubernetes install, real-cloud deployment, Pact/BDD suites, the Solution Document (template not provided), demo video.

## Run it
Prerequisites: Docker (≥ 12 GB RAM), Go ≥ 1.27, Node 22, `make`, Python ≥ 3.11 (ML only).
```bash
make up                 # random credentials in .env, start the stack, migrate, set roles, device CA, topics
                        # (restricted sandboxes: `make up-lowulimit`; Docker Hub rate-limited: pull from mirror.gcr.io)
make seed               # deterministic 100,000-vehicle dataset, verified against db/seed/manifest.json
make demo               # builds binaries + console, loads incidents, starts the pipeline and a 100K-vehicle simulation
# open http://localhost:8081  ->  dispatcher@meridian.example / DEMO_USER_PASSWORD from .env   (script: docs/demo.md)
make test               # unit tests (no Docker)         make test-integration   # + real Kafka/PG/CH/Redis/Keycloak/Vault
make chaos              # all chaos scenarios (~30 min)  make e2e                # live 100K run + reconciliation
```
Demo users (password in `.env`): `viewer|dispatcher|energy_manager|tenant_admin@meridian.example` (enterprise plan, has the Copilot), `…@coastal.example` (pro plan, no Copilot), `admin@platform.example`.

## Layout
`cmd/` services and CLIs · `internal/` libraries (`rangerisk`, `rtp`, `sim`, `ingest`, `api`, `copilot`, `chargeplan`, `batch`, `auditchain`, …) · `web/` React console · `db/` migrations, seed manifest · `proto/` contracts · `ml/` training pipeline · `deploy/{compose,helm,terraform,docker}` · `chaos/`, `tools/` · `docs/` (architecture, ADRs, threat model, algorithms, deployment, demo, requirement ledger) · `evidence/` machine-produced results.

CI: [GitHub Actions](https://github.com/SohamB4746Y/voltsight-connected-vehicle-intelligence/actions) (private repository). The stack-backed tests run locally; CI covers build, unit tests, web, Helm, Terraform, scans and the G1 gate.
