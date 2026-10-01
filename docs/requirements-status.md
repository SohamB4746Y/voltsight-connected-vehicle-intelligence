# Requirement status (honest ledger)
Legend: **PASS** = verified by a committed test/measurement · **PARTIAL** = built, with named gaps · **MEASURED** = a number exists but below/at a different scale than the target · **NOT DONE / NOT MEASURED / NOT RUN** = no evidence · **LIMITATION** = known, documented. Machines: *Win* = 14-CPU Windows laptop (M0–M4), *box* = 4-vCPU Linux sandbox (M4 closure onward). Evidence paths are relative to the repository root.

## Scale and NFRs
| ID | Requirement | Status | Evidence / gap |
|---|---|---|---|
| SC1 | 100,000 vehicles | PASS | seeded + verified against `db/seed/manifest.json`; simulator runs all 100K in ~200 MB heap |
| SC2/N1 | ~100K events/s sustained | **MEASURED, below target, short**: 6.0M events at **98.3K ev/s for 60 s** (box, everything on one 4-vCPU host, CPU oversubscribed) | `evidence/G4/live_fixed_faults_adversarial` — not a sustained/soak result |
| SC3/N2 | 3× burst for 5 min, no loss | see `evidence/G14/status.md` | |
| SC4 | ~1 KB event, TB/day analysis | PARTIAL | measured bytes/event in `docs/capacity.md`; the TB/day figure is an extrapolation |
| SC5 | single SQL DB vs right store, with a benchmark | PARTIAL | argument in ADR-002; ClickHouse insert rate measured; **no PostgreSQL-vs-ClickHouse insert comparison was run** |
| SC6 | ACID for ownership/billing/access/audit | PASS | G1 transaction + RLS tests |
| N3 | ingest → dashboard < 2 s | NOT MEASURED | SSE path built; no latency probe was run |
| N4 | critical alert < 5 s | NOT MEASURED | `vsalerts -latency-log` is ready; the live run was aborted (priority change) and the one overloaded demo run showed ~90 s lag when the 4 cores were shared with training/test jobs |
| N5 | API p95 < 200 ms, p99 < 500 ms | PASS (ingest idle) | p95 26.8 ms, p99 41.0 ms, 829 req/s, 16 clients (`evidence/G9b/api_latency.json`); not measured under 100K ev/s ingest |
| N6/N7 | horizontal scale, add brokers without code change | NOT MEASURED | stateless services + 64 partitions by design; no scale-out experiment |
| N8/N9/N10 | no SPOF, 99.9%, recovery | N10 PARTIAL (see `evidence/G13/status.md`: component kills/restarts recover with zero loss); N8 LIMITATION (single broker/ClickHouse/Redis locally); N9 NOT MEASURED | |
| N11–N14 | OIDC/JWT, RBAC, tenant isolation, device mTLS | PASS | API/G3/G1 test suites (`docs/threat-model.md`) |
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
| MB5 | PASS (tests); DAST **not run** | |
| MB6 | PASS | Playwright journey through real Keycloak sign-in (`tests/e2e`, screenshots in `evidence/G11`); accessibility scan not run |
| MB7/DL10 | PARTIAL | Dockerfile written; **image never built** here; hadolint not run |
| MB9/T12/DL11/DL12 | PARTIAL | Helm lint + kubeconform 21/21; `terraform validate` AWS+GCP; **no kind install, no cloud apply** |
| T5 | PARTIAL | batch over 22M rows measured (trips in 15 s); the "billion-row" benchmark was **not** run |
| T11 | PARTIAL | Prometheus metrics on gateway/worker; no Loki/Tempo traces |
| D1–D3, D8–D13 | PASS | `docs/3nf.md`, `docs/er`, `docs/sql-optimisation.md`, migration 4; D5 skew histogram not run |
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
| DL1 Solution Document | **NOT DONE** | the template was never provided |
| DL2/DL3/DL8 | PARTIAL | `make up-lowulimit && make seed && make build && make web && tools/live/demo.sh` works in this sandbox; no clean-clone run |
| DL4–DL7, DL13, DL14 | PASS | |
| DL9 | PARTIAL | evidence folders; no rendered claims-check tooling |
| DL15 demo video | **NOT DONE** | script in `docs/demo.md` |
| RL1, RL2, RL5 | PASS | `docs/ai-oss-declaration.md` (SBOM not generated) |
| RL3 | see git tags | |
