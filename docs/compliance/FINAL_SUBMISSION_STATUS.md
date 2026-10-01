# Final submission status

Counts come from `go run ./tools/reqtrace docs/compliance/FINAL_REQUIREMENT_MATRIX.md` (60 requirements): **PASS 28 · PARTIAL 28 · BLOCKED 2 · FAIL 2**. Per-requirement evidence: [`FINAL_REQUIREMENT_MATRIX.md`](FINAL_REQUIREMENT_MATRIX.md). Freeze record: [`CODE_FREEZE.md`](CODE_FREEZE.md). All measurements are from a 4-vCPU Linux sandbox with every component on one host.

## GREEN: implemented and verified (evidence exists)

| Area | What is verified | Evidence |
|---|---|---|
| 100,000-vehicle simulation | Streamed in real time through the mTLS gateway, 98.3K events/s for 60 s, 0 failed, books reconcile | `evidence/G4/status.md` |
| Real-time + batch | Range-risk engine, trips, SoH, charge optimiser, history over 29.2M rows | `evidence/G6`, `evidence/G14` |
| Polyglot storage | PostgreSQL+pgvector, Redis, Kafka, ClickHouse with measured justification | `evidence/G14/pg_vs_clickhouse.txt` |
| API performance | p95 26.8 ms, p99 41.0 ms at 829 req/s (targets 200/500 ms) | `evidence/G9b/api_latency.json` |
| SQL optimisation | Before/after plans on 1.5M rows | `docs/database/SQL_OPTIMIZATION.md` |
| Security controls | OIDC+PKCE, RBAC, RLS, tenant isolation, CORS, admin-surface blocking: 31/31 scripted checks as real users | `evidence/live-deployment/live_checks.json` |
| Auth session | Unit-tested refresh design; real browser kept session over a token expiry | `web/src/auth.test.ts`, `evidence/live-deployment/auth-map/auth_session_check.json` |
| Automated alerts | Real pipeline alerts reach the dashboard | `evidence/live-deployment/shots`, `evidence/G11/status.md` |
| Chaos | 5 scenarios, 0 events lost, 2 defects found and fixed | `evidence/G13/status.md` |
| ML | GBM beats three baselines on two hold-outs; leakage checks pass | `evidence/G8/ml_report.json` |
| Copilot | 8 typed tools, guardrails, approval-gated writes, 8 scenarios | `evidence/G10/status.md` |
| Scans | gitleaks, govulncheck, npm audit, semgrep, Trivy fs/image clean; SBOM generated | `evidence/G12/status.md`, `evidence/security/sbom-image.cdx.json` |
| ZAP baseline | 58 passed, 0 failures, 3 warning groups | `evidence/security/zap/` |
| Packaging | Docker image on GHCR, Compose, Helm lint + kubeconform, Terraform validate, CI | `evidence/live-deployment/helm-terraform-validation.txt`, `image-scan.txt` |

## YELLOW: partial (implemented, with a named gap)

| Requirement | Gap |
|---|---|
| NF-01 sustain 100K ev/s | 98.3K ev/s for 60 s only |
| NF-02 3× burst for 5 min | 40 s inside a 100 s run, 0 lost |
| NF-03 ingest to dashboard < 2 s | not measured end to end (receive→detect p50 28 ms only) |
| NF-06 / NF-07 horizontal scale, no SPOF | no scale-out experiment; single instances locally; pod kills not run |
| LV-02 geographic map | markers, clusters, filters, route verified from backend data; real OpenFreeMap basemap not seen rendering in the sandbox browser |
| LV-03 public URL | Codespaces live demo; not tested from this environment; stops when idle |
| TE-03 big data pipeline | no billion-row run |
| TE-09 log analytics | Prometheus/OTel/Grafana only; no Loki/Tempo |
| MB-05, TE-10 cloud deployment, IaC | Terraform validated, never applied |
| SD-03 hot/warm/cold lifecycle | no Parquet cold tier |
| NF-08 / NF-09 / NF-10 security, OWASP, audit | Vault dev mode; three OWASP items without a dedicated test; no PostgreSQL audit purge |
| TS-01 coverage | coverage of later packages not measured |
| TS-03 contract tests | Protobuf contract tests only, no Pact |
| TS-05 load + soak | no soak |
| TS-06 DAST | unauthenticated baseline, ran before CSP tightening |
| TS-07 compliance + chaos | no Kubernetes pod kill |
| DL-01 Solution Document | template not provided; structured substitute supplied |
| DL-02 one-command setup | not run from a clean clone elsewhere |
| DL-03 diagrams | runtime/trust-boundary/deployment diagrams not all drawn |
| DL-05 test evidence | coverage incomplete |
| DL-08 demo video | needs a human recording; script is provided |
| AL-02, AL-04, SD-02 | evidence is tests/write-up; no general window operator; no circuit-breaker library |

## RED: unverified, failed or blocked

| Item | Status | Reason |
|---|---|---|
| NF-04 critical alert < 5 s | FAIL | at 100K ev/s receive→persist p50 9.8 s, p99 17.0 s on one host; multi-host not tested |
| TS-04 BDD acceptance tests | FAIL | not implemented |
| RL-03 tag `v1.0-submission` on GitHub | BLOCKED | the push from this environment was refused (HTTP 403); the tag exists locally only and predates later commits |
| LV-04 production cloud deployment | BLOCKED | needs the owner's cloud account |

## Not verified (no row can turn these green)

Soak run; scale-out; accessibility scan; authenticated/active DAST; real LLM provider (Anthropic) behaviour; the 300 s production token lifetime held for 5+ minutes; the public Codespaces URL from this environment; the stack test `TestMapEndpointsAreTenantBoundAndShaped` locally (it runs in CI).
