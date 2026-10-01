# VoltSight — Solution Document

*The hackathon's Solution Document template was not available to the author; this document follows the structure the brief asks for. Every claim names the evidence behind it. Results marked NOT VERIFIED were not measured. Status of each requirement: [`../compliance/FINAL_REQUIREMENT_MATRIX.md`](../compliance/FINAL_REQUIREMENT_MATRIX.md).*

## 1. Problem and users

EV fleets strand vehicles because range estimates ignore battery ageing, load, temperature, route and charger outages; they overpay for charging (peak tariffs, depot congestion); and they cannot see battery degradation. The decision has to be made while the driver can still act, across a 100,000-vehicle fleet.

| User | Need | Role in VoltSight |
|---|---|---|
| Dispatcher | Which vehicles will strand, and where can they charge | `dispatcher`: alerts, map, Copilot, acknowledge alerts |
| Energy manager | Cheapest compliant charging plan | `energy_manager`: charge plans |
| Tenant admin | Governance, audit, erasure | `tenant_admin`: audit log, users, privacy |
| Viewer | Read-only visibility | `viewer`: dashboard, map (location masked) |
| Platform admin | Operate the platform | `admin@platform.example` |

## 2. Solution overview

Simulator (100,000 vehicles) → mTLS gateway → Kafka → stream worker (dedup, state, range-risk) → alerts.v1 → alert service → PostgreSQL + Redis → API → React console with a live map. History goes to ClickHouse for batch analytics and ML. The architecture diagram and data-flow sequence are in the [README](../../README.md#4-system-architecture); the component detail is in [`../architecture.md`](../architecture.md).

## 3. Architecture and data flow

Services (Go): `vssim` (simulator), `vsgateway` (ingest), `vsworker` (real-time), `vssink` (ClickHouse writer), `vsalerts` (alert service), `vsapi` (API + console host), `vsbatch` (batch analytics), `vsrecon` (reconciliation). Contracts are Protobuf with `buf` lint and breaking-change checks.

Data flow: device event → gateway (mTLS identity, validation, DLQ with reason, 429 back-pressure) → `telemetry.v1` (64 partitions keyed by VIN) → worker and sink consumer groups → hot state in Redis, history in ClickHouse, alerts on `alerts.v1` → PostgreSQL (idempotent) → Redis pub/sub → API SSE → browser.

## 4. Real-time path

- **Exact per-VIN dedup window** plus a Bloom negative cache; Count-Min sketch for approximate frequency.
- **Out-of-order tolerance** with event-time handling; duplicates, malformed events and schema variants (two OEM dialects) are handled in `internal/normalise`.
- **Range-risk engine** (`internal/rangerisk`): EWMA consumption estimator, hysteresis, deterministic alert windows; a multi-source Dijkstra overlay holds distance to the nearest *available* charger per tenant and is updated incrementally when a charger fails (lookup 3.6 ns, rebuild 10.1 ms on 48K nodes, incremental update 70.7 µs).
- **Delivery:** at-least-once with idempotent sinks (alert key `(vin, rule, window_start)`, ClickHouse `ReplacingMergeTree`) → effectively-once observable results ([ADR-003](../adr/003-consistency-delivery.md), [CAP/PACELC](../architecture/CAP_PACELC.md)).
- **Measured:** 100K vehicles for 60 s at 98.3K events/s, 6.0M events, 0 failed, ClickHouse distinct events equal worker distinct events ([`evidence/G4/status.md`](../../evidence/G4/status.md)).

## 5. Batch path

`vsbatch` computes trips from noisy GPS (F1 0.954 against simulator truth), battery state of health from charging sessions (MAPE 0.5%, optimistic because the assumed charging efficiency equals the simulator's), depot clusters and a time-of-use **charge-plan optimiser** (dynamic programming; cheaper than charge-on-plug-in in 389 of 392 random cases, 0.53 ms per vehicle). ClickHouse history queries over 29.2 M rows ran in 129–720 ms ([`evidence/G14`](../../evidence/G14/status.md)). A billion-row batch was **not** run.

## 6. Database design and polyglot persistence

| Store | Role | Evidence |
|---|---|---|
| PostgreSQL 16 + pgvector | Transactional system of record, RLS, audit, embeddings | [3NF](../3nf.md), [ER](../er/README.md), [ADR-006](../adr/006-tenant-isolation.md) |
| Redis | Hot state, dedup window, pub/sub | [ADR-002](../adr/002-polyglot-storage.md) |
| Kafka | Durable log and replay | [ADR-001](../adr/001-streaming-engine.md) |
| ClickHouse | Telemetry history, analytics | 1M-row insert: PostgreSQL 97K rows/s vs ClickHouse 405K rows/s; 17.3 B/row (`evidence/G14/pg_vs_clickhouse.txt`) |

The schema is 3NF with one documented, FK-enforced denormalisation (`tenant_id` on child tables with composite foreign keys). SQL tuning with real plans: [`../database/SQL_OPTIMIZATION.md`](../database/SQL_OPTIMIZATION.md) (inbox query 1,306 → 0.28 ms; report 1,310 → 0.024 ms on 1.5M rows). No cold Parquet tier exists.

## 7. Algorithms

Dijkstra overlay (multi-source), A* route, EWMA estimator, Bloom filter, Count-Min sketch, DP charge optimiser, keyset pagination, hash-chain audit. Complexity and measurements: [`../algorithms.md`](../algorithms.md).

## 8. Machine learning

GBM consumption model vs three baselines: MAE 0.01736 vs 0.0274 kWh/km on a vehicle hold-out (95% CI of the difference excludes 0), 0.01718 vs 0.02489 on a time hold-out; leakage checks pass; labels are simulated. Details: [`../ml/ML_RESULTS.md`](../ml/ML_RESULTS.md).

## 9. Agentic AI

Charge Ops Copilot: 8 typed tools, guardrails enforced outside the model, citations or "insufficient evidence", two-phase human-approved writes, every step audited. Default provider is a deterministic stub; the Anthropic provider was not exercised. Details: [`../agent/AGENT_ARCHITECTURE.md`](../agent/AGENT_ARCHITECTURE.md), [ADR-005](../adr/005-agent-guardrails.md), scenarios in [`evidence/G10`](../../evidence/G10/status.md).

## 10. Security

Keycloak OIDC with Authorization Code + PKCE; RS256 JWT validation; RBAC and subscription entitlements; PostgreSQL RLS (enabled and forced, no role with BYPASSRLS); mTLS 1.3 for devices with Vault-issued certificates; hash-chained audit; location masking. Short-lived access tokens are renewed transparently by refresh-token grant ([ADR-009](../adr/009-session-and-token-lifecycle.md)). OWASP ZAP baseline: 58 passed, 0 failures, 3 warning groups. See [`../security/SECURITY_SUMMARY.md`](../security/SECURITY_SUMMARY.md) and [`../security/STRIDE.md`](../security/STRIDE.md).

## 11. Privacy and compliance

Synthetic data only. Role-based location masking, an erasure workflow (GDPR/DPDP-style), audit trail, retention notes; GDPR, DPDP and UNECE R155/R156 are mapped at awareness level in [`../compliance.md`](../compliance.md). PostgreSQL audit-retention purge is not implemented.

## 12. Observability

Structured `slog` logs, Prometheus metrics on gateway and worker, OpenTelemetry collector, Grafana, per-alert latency log, Kafka lag via `vsrecon`, health and readiness endpoints. No Loki/Tempo; end-to-end tracing is partial.

## 13. Scalability and capacity

Design: 64 partitions keyed by VIN, stateless workers scaling by consumer group, ClickHouse for analytics. Measured on one 4-vCPU host: 98.3K ev/s for 60 s, 3× burst for 40 s with 0 lost. **Not verified:** sustained 100K ev/s beyond 60 s, 3× for 5 minutes, scale-out, soak. Calculations and the MEASURED / CALCULATED / DESIGN TARGET / NOT VERIFIED split: [`../performance/CAPACITY_ANALYSIS.md`](../performance/CAPACITY_ANALYSIS.md).

## 14. Deployment

Docker image (non-root, 74 MB) published to GHCR by CI; Docker Compose; one-command live profile `deploy/live/up.sh` run in GitHub Codespaces as a **live demo environment (not production)**; Helm chart (lint + kubeconform; accepted by a kind API server in CI); Terraform AWS/GCP (validated, never applied). See [`../deployment-live.md`](../deployment-live.md), [ADR-010](../adr/010-live-demo-deployment.md).

## 15. Testing

Unit, integration (real stack), 100K-vehicle E2E, browser E2E, scripted live checks (31/31), security scans, ZAP baseline, chaos (5 scenarios). Missing: BDD, Pact, soak, authenticated DAST. See [`../testing/TESTING_SUMMARY.md`](../testing/TESTING_SUMMARY.md).

## 16. Failure handling

Worker, sink, Redis, ClickHouse and Kafka restarts mid-run at 100K vehicles lost 0 events and the method found and fixed two real defects; services retry with capped back-off instead of exiting; the stack recovers from a full restart ([`../testing/CHAOS_RESULTS.md`](../testing/CHAOS_RESULTS.md), [`evidence/live-deployment/full-restart.txt`](../../evidence/live-deployment/full-restart.txt)). The map degrades to markers-only when the basemap host is unreachable. The gateway-kill scenario was not run.

## 17. Design decisions (ADRs)

[001 streaming engine](../adr/001-streaming-engine.md) · [002 polyglot storage](../adr/002-polyglot-storage.md) · [003 consistency/delivery](../adr/003-consistency-delivery.md) · [004 device transport](../adr/004-device-transport.md) · [005 agent guardrails](../adr/005-agent-guardrails.md) · [006 tenant isolation](../adr/006-tenant-isolation.md) · [007 model serving](../adr/007-model-serving.md) · [008 map technology](../adr/008-map-technology.md) · [009 session lifecycle](../adr/009-session-and-token-lifecycle.md) · [010 live demo deployment](../adr/010-live-demo-deployment.md).

## 18. Limitations

- Critical alert < 5 s **not met** at 100K events/s on one host (p50 9.8 s, p99 17.0 s).
- 100K events/s measured as 98.3K for 60 s; 3× burst 40 s, not 5 min; no soak; no scale-out.
- Real basemap not seen rendering in the sandbox browser; markers-only fallback verified.
- No BDD, no Pact; ZAP is an unauthenticated baseline that predates the CSP tightening.
- No cold archive; Vault in dev mode; Terraform never applied; no cloud deployment.
- ML labels are simulated; LLM provider not exercised.
- Tag `v1.0-submission` exists locally only.

Full list: [`../compliance/FINAL_SUBMISSION_STATUS.md`](../compliance/FINAL_SUBMISSION_STATUS.md).
