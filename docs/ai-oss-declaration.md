# AI and open-source declaration

## AI assistance
This repository was built with **Claude Code** (Anthropic): architecture, implementation, tests and documentation were produced in interactive sessions with the author and committed with co-author trailers. The product itself contains an assistant ("Charge Ops Copilot") whose default provider is a deterministic rule engine; an optional Claude tool-use provider is implemented but was **not exercised** (no API key). No model was used to generate evidence: every number in `evidence/` and the documentation comes from a command recorded in the repository.

## Data
Only synthetic data: the fleet, VINs (valid ISO 3779 check digits), tenants, road networks, telemetry and incident narratives are generated deterministically by `internal/seedgen` and `internal/sim`. No real vehicle, driver or customer data. The project has no affiliation with Motorq (named only as an industry reference in the problem statement).

## Open-source components (principal)
Kafka (Apache-2.0), PostgreSQL + pgvector, ClickHouse, Redis, SeaweedFS, Keycloak, Vault (BUSL — used as a local dev server), Prometheus, Grafana, OpenTelemetry Collector; Go libraries: franz-go, pgx, clickhouse-go, go-redis, golang-jwt, protobuf, prometheus client, testcontainers; web: React, Vite, oidc-client-ts; ML: scikit-learn, pandas, numpy; tools: buf, Helm, Terraform, kubeconform, gitleaks, Trivy, Semgrep, govulncheck. The complete dependency lists are `go.mod`/`go.sum`, `web/package-lock.json` and `ml/` imports; a generated SBOM (syft) was **not** produced.
