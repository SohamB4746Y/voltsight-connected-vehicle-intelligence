# Requirement status (honest ledger)
Legend: **PASS** = verified by a committed test/measurement · **PARTIAL** = built, with named gaps · **MEASURED** = a number exists but below/at a different scale than the target · **NOT DONE / NOT MEASURED / NOT RUN** = no evidence · **LIMITATION** = known, documented. Machines: *Win* = 14-CPU Windows laptop (M0–M4), *box* = 4-vCPU Linux sandbox (M4 closure onward). Evidence paths are relative to the repository root.

## Scale and NFRs
| ID | Requirement | Status | Evidence / gap |
|---|---|---|---|
| SC1 | 100,000 vehicles | PASS | seeded + verified against `db/seed/manifest.json`; simulator runs all 100K in ~200 MB heap |
| SC2/N1 | ~100K events/s sustained | **MEASURED, below target, short**: 6.0M events at **98.3K ev/s for 60 s** (box, everything on one 4-vCPU host, CPU oversubscribed) | `evidence/G4/live_fixed_faults_adversarial` — not a sustained/soak result |
| SC3/N2 | 3× burst for 5 min, no loss | **PARTIAL**: 3× burst for **40 s** inside a 100 s run: 18.0M events, 0 lost/DLQ/failed, no 429s, books balanced (164K ev/s average); the 5-minute duration was not run | `evidence/G14/burst_3x` |
| SC4 | ~1 KB event, TB/day analysis | PARTIAL | measured bytes/event in `docs/performance/CAPACITY_ANALYSIS.md`; the TB/day figure is an extrapolation |
| SC5 | single SQL DB vs right store, with a benchmark | PASS (benchmark) | 1M rows: PostgreSQL COPY 97K rows/s, 299 B/row with PK index vs ClickHouse 405K rows/s (4.2×); the 2.7 B/row ClickHouse figure is inflated by uniform synthetic rows — simulated telemetry measures 17.3 B/row (`evidence/G14/pg_vs_clickhouse.txt`) |
| SC6 | ACID for ownership/billing/access/audit | PASS | G1 transaction + RLS tests |
| N3 | ingest → dashboard < 2 s | NOT MEASURED | SSE path built; no latency probe was run |
| N4 | critical alert < 5 s | **MEASURED, NOT met on this host** | receive→persisted p50 9.8 s, p99 17.0 s (236 alerts, full stack on one 4-vCPU box at ~100K ev/s); worker-only ingest→process mean 1.9 s, ≤ 5 s for all sampled events (`evidence/G14/status.md`). Multi-host sizing was not tested |
| N5 | API p95 < 200 ms, p99 < 500 ms | PASS (ingest idle) | p95 26.8 ms, p99 41.0 ms, 829 req/s, 16 clients (`evidence/G9b/api_latency.json`); not measured under 100K ev/s ingest |
| N6/N7 | horizontal scale, add brokers without code change | NOT MEASURED | stateless services + 64 partitions by design; no scale-out experiment |
| N8/N9/N10 | no SPOF, 99.9%, recovery | N10 PARTIAL (`evidence/G13/status.md`: worker/sink kill, Redis/ClickHouse/Kafka restart recover with zero loss; gateway kill, PostgreSQL failover and Kubernetes pod kills not run); N8 LIMITATION (single broker/ClickHouse/Redis locally); N9 NOT MEASURED | |
| N11–N14 | OIDC/JWT, RBAC, tenant isolation, device mTLS | PASS | API/G3/G1 test suites (`docs/security/STRIDE.md`) |
| N15 | TLS 1.3 | PASS (local, openssl) | API `-tls` and gateway accept only 1.3 (`evidence/G12/tls_api.txt`, G3); no scanner run |
| N16 | AES-256 at rest | LIMITATION | local volumes unencrypted; KMS/CMEK in Terraform (validated, not applied) |
| N17 | secrets in a vault | PARTIAL | Vault issues device/server certificates; DB/Redis passwords are generated into a git-ignored `.env`, referenced by Kubernetes Secret in Helm |
| N18 | OWASP Top 10 + API Top 10 | PARTIAL | `docs/owasp-map.md`: tests for most items, three items without a test (stated) |
| N19/N20 | audit of every data access / agent action | PASS | `TestAuditCompleteness`, copilot audit test, hash chain |
| N21 | location masking | PASS | `TestLocationMaskingByRole` |
| N22 | retention | PARTIAL | ClickHouse TTL (30 d) and Kafka retention configured; no accelerated-clock test; PostgreSQL audit has no purge |
| N23 | erasure | PASS | `TestErasureFlowEndToEnd` (PostgreSQL; the other stores hold no personal data by design) |

## Product, algorithms, data
| ID | Status | Notes |
|---|---|---|
| MB1–MB4, T1–T4, T6–T10 | PASS | simulator, real-time path, batch analytics, polyglot stores (PostgreSQL/pgvector, ClickHouse, Redis), secure API, console, ML vs baselines, vector search, audited Copilot |
| MB5 | PASS (tests); DAST: ZAP baseline run (unauthenticated; 58 pass, 0 fail, 3 warning groups), see `evidence/security/zap/` | |
| MB6 | PASS | Playwright journey through real Keycloak sign-in (`tests/e2e`, screenshots in `evidence/G11`); accessibility scan not run |
| MB7/DL10 | PARTIAL | Dockerfile written; **image never built** here; hadolint not run |
| MB9/T12/DL11/DL12 | PARTIAL | Helm lint + kubeconform 21/21; `terraform validate` AWS+GCP; **no kind install, no cloud apply** |
| T5 | PARTIAL | batch over 22M rows measured (trips in 15 s); ClickHouse history queries on 29.2M synthetic rows 129–720 ms (`evidence/G14/ch_bench.json`); the "billion-row" benchmark was **not** run |
| T11 | PARTIAL | Prometheus metrics on gateway/worker; no Loki/Tempo traces |
| D1–D3, D8–D13 | PASS | `docs/3nf.md`, `docs/er`, `docs/database/SQL_OPTIMIZATION.md`, migration 4; D5 skew histogram not run |
| AL1–AL6, AL8–AL11 | PASS | `docs/algorithms.md` |
| AL7 | PARTIAL | sequence window and EWMA are implemented; no general time-window aggregation operator |
| SD1, SD4, SD5, SD9, SD12, SD14, SD16 | PASS | ADRs + replay/crash/chaos tests |
| SD2, SD3, SD6, SD8, SD10, SD11, SD13, SD15 | PARTIAL / NOT MEASURED | Kafka RF3 only in IaC; back-pressure proven at the gateway (G3); no circuit-breaker library (bounded retries with back-off instead); CQRS is the Redis/ClickHouse/PostgreSQL read-model split without a formal framework; cache-invalidation ≤ 2 s not tested; scale-out not run |
| X6, X8 | PARTIAL | cloud-agnostic by protocol; compliance mapping is awareness-level |

## Testing and deliverables
| ID | Status | Notes |
|---|---|---|
| TS1 | NOT MEASURED for the new packages | G1 packages ≥ 80% (see `evidence/G1`); no coverage run for M2–M9 packages |
| TS2 | PARTIAL | CI builds, unit-tests, type-checks, lints Helm/Terraform and scans; the stack-backed tests (Kafka/PG/CH/Redis/Keycloak/Vault) run locally, not in CI |
| TS3 | PASS locally | real infrastructure (compose) rather than mocks |
| TS4 Pact, TS5 BDD | **NOT DONE** | |
| TS6/TS7 | PARTIAL / NOT DONE | 1× 60 s load measured; no soak |
| TS8/TS10 | PASS | semgrep, gitleaks, govulncheck, npm audit, Trivy fs; image scan not run |
| TS9 | NOT RUN | OWASP ZAP |
| TS11–TS13 | PASS | |
| DL1 Solution Document | **PARTIAL** | the template was never provided; a structured substitute is `docs/solution/SOLUTION_DOCUMENT.md` |
| DL2/DL3/DL8 | PARTIAL | `make up-lowulimit && make seed && make build && make web && tools/live/demo.sh` works in this sandbox; no clean-clone run |
| DL4–DL7, DL13, DL14 | PASS | |
| DL9 | PARTIAL | evidence folders; no rendered claims-check tooling |
| DL15 demo video | PARTIAL | three silent screen-recording clips of the real journey (dispatcher, energy manager, viewer; 49 s + 17 s + 13 s) in `docs/demo/*.webm`, recorded by `tests/e2e/record_demo.mjs` against the live system; **no narration**, shorter than the 5-minute slot; the spoken script is `docs/demo.md` |
| RL1, RL2, RL5 | PASS | `docs/AI_OSS_DISCLOSURE.md` (SBOM not generated) |
| RL3 | see git tags | |

## Live deployment (added 2026-10-01; scope: the live profile on the build sandbox unless stated)
Columns: Implementation = what exists; Live verification = what was actually run and where; Status uses PASS / PARTIAL / FAIL / BLOCKED / NOT RUN / NOT MEASURED.
| ID | Requirement | Implementation | Live verification | Status | Limitation |
|---|---|---|---|---|---|
| LD1 | Public HTTPS URL a judge can open | `deploy/live/up.sh`, Caddy edge, `.devcontainer`, `tls` profile | none: nothing is hosted on the internet | **BLOCKED** (needs the owner's Codespace or VM account) | free hosts that can run the stack were investigated 2026-10-01 (`docs/deployment-live.md` section 2) |
| LD2 | Real frontend + real backend on one origin | console + API + OIDC behind Caddy | browser journey and 31 checks on `http://localhost:8080` | PASS (local profile) | not on the internet |
| LD3 | Real authentication (OIDC) | Keycloak `start` mode, public realm, PKCE | 4 users signed in via the real flow | PASS (local profile) | |
| LD4 | RBAC + tenant isolation | unchanged from G9/G1; public realm without DEV client | `live_checks.json` RBAC-*, ISO-*, EXP-* | PASS (local profile) | |
| LD5 | Real telemetry pipeline | simulator, mTLS gateway, Kafka, worker, sink | ~816 events/s, lag 0, 33 alerts | PASS (local profile) | demo scale |
| LD6 | Range-risk alerts persisted and visible | worker risk engine, alert service, SSE | alert rows in PostgreSQL, alert detail screenshot | PASS (local profile) | |
| LD7 | Copilot | typed tools, guardrails, audit | answered a tool-backed question | **PARTIAL** | deterministic stub provider; no LLM key |
| LD8 | Docker production image builds and runs | `deploy/docker/Dockerfile`; CI `image` job | built, 74.5 MB, non-root, started in compose | PASS (local, and CI run 36913328981 on a plain runner) | sandbox needed a CA workaround, not part of the repo; published to GHCR by CI on `main` (run 36923352796), anonymous pull verified |
| LD9 | Image scan | Trivy in CI (report) | Trivy 0.57.1: 0 HIGH/CRITICAL | PASS | |
| LD10 | Helm validates | chart + kubeconform | lint ok, 21/21 valid | PASS | |
| LD11 | Chart accepted by a real Kubernetes API server | `.github/workflows/kubernetes.yml` (kind in CI) | CI run 36913329082: ephemeral kind v1.32.2, dry run + real install + uninstall all passed (could not run in the sandbox) | **PASS (API-server acceptance only)** | pod readiness not asserted (needs external backing services); no public cluster |
| LD12 | Terraform validates | AWS, GCP | fmt + validate pass | PASS | not applied |
| LD13 | Survives restart | `up.sh`, named volumes, `vssim -seq-base` | single-service restarts and a full stop/start | PASS after one defect found and fixed | PostgreSQL failover, gateway kill, node loss not tested |
| LD14 | Live event rate | | ~816 events/s at 2,000 configured vehicles | MEASURED | 100K/s NOT MEASURED live |
| LD15 | Alert latency | | receive-to-detect p50 28 ms, p95 133 ms (n=33) | MEASURED (partial path) | end-to-end and the 5 s target NOT MEASURED live |
| LD16 | API p95/p99 | | p95 <= 10.3 ms, p99 <= 12.9 ms (loopback, 300 req/endpoint) | MEASURED | not under concurrent load or over a network |
| LD17 | No secrets committed | gitleaks, `.env` ignored | clean over 37 commits | PASS | |
| LD18 | Observability | Prometheus, Grafana, OTel | 6 scrape targets up | PARTIAL | no Loki/Tempo; internal only |
| LD19 | Deployment triggered by a GitHub commit | `image` job publishes to GHCR | not run | NOT RUN | no provider credential exists |
