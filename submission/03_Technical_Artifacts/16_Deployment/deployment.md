# Deployment

## Local (Docker Compose) — what is verified
`make up` (or `make up-lowulimit` on hosts whose hard `RLIMIT_NOFILE` is below 262,144, e.g. restricted sandboxes) starts Kafka (KRaft), PostgreSQL+pgvector, ClickHouse, Redis, SeaweedFS (S3), Keycloak, Vault, an OTel collector, Prometheus and Grafana, migrates the database, sets role passwords, creates the device CA and the Kafka topics. `make seed` loads and verifies the 100,000-vehicle dataset. See [`demo.md`](demo.md) for the end-to-end run.

Notes for restricted environments (all observed in this repository's sandbox): Docker Hub rate-limits shared egress — pull from `mirror.gcr.io` and retag; ClickHouse needs `docker-compose.lowulimit.yml` when the hard file-descriptor limit is 20,000.

## Kubernetes (Helm) — validated statically, **not deployed**
`deploy/helm/voltsight` renders 21 resources (Deployments, Services, HPAs, PDBs, CronJobs, NetworkPolicies; Ingress optional). `helm lint` passes; `helm template | kubeconform -strict` reports 21/21 valid. One image (`deploy/docker/Dockerfile`, multi-stage, distroless non-root) carries every binary and the web console; each Deployment selects its binary. Pods run non-root with a read-only root filesystem, dropped capabilities, seccomp RuntimeDefault, spread constraints and `maxUnavailable: 0` rollouts. Secrets are referenced from an existing Secret (External Secrets/Vault Agent in a cloud).
**Not verified:** installing on a cluster (kind), pod-kill recovery on Kubernetes, the image build in this sandbox, the in-cluster data tier (`infra.enabled` is a placeholder — use managed services or operators: Strimzi for Kafka, the Altinity operator for ClickHouse).

## Cloud (Terraform) — validated, **never applied**
`deploy/terraform/aws` (VPC, flow logs, EKS with KMS-encrypted secrets, RDS PostgreSQL multi-AZ, ElastiCache, MSK, S3 with SSE-KMS, KMS rotation) and `deploy/terraform/gcp` (VPC, private GKE with workload identity and authorised networks, Cloud SQL HA with CMEK and enforced TLS, Memorystore, CMEK-encrypted GCS) both pass `terraform validate` (providers fetched, config checked). Trivy IaC: 2 GCP findings waived with reasons in `.trivyignore`. **"Deployed on a real cloud" is NOT VERIFIED**: no account was available. Cloud-agnosticism rests on the services using only standard protocols (Kafka, PostgreSQL, Redis, ClickHouse native, OIDC, Vault HTTP); `grep` finds no cloud SDK in the code.

## Configuration
Environment (or `.env`): `POSTGRES_HOST/PORT`, `REDIS_ADDR`, `CLICKHOUSE_ADDR`, `VAULT_ADDR`, `KAFKA_BROKERS` (flag `-brokers`), per-role DB passwords, `OIDC` issuer/JWKS (flags), `ANTHROPIC_API_KEY` + `COPILOT_MODEL` (optional).
