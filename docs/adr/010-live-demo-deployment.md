# ADR-010 Live demo deployment: one compose profile, any Docker host
**Status:** accepted (2026-10) · **Context:** a public URL had to be free, card-free and run the real stack (Kafka, PostgreSQL, ClickHouse, Redis, Keycloak, Vault, gateway, worker, sink, alert service, API, simulator). Free hosts surveyed on 2026-10-01 (`docs/deployment-live.md` §2) either require a paid plan for containers (Hugging Face Docker Spaces, Koyeb), lack private services/workers/disks (Render free), or need an account step we cannot perform.

**Decision.** One Docker Compose profile (`deploy/compose/docker-compose.live.yml` + `deploy/live/up.sh`) with a single Caddy origin, run on whatever host the owner has: GitHub Codespaces (free allowance) for the demo, a VM with `PUBLIC_HOST` for an always-on host. Kubernetes (Helm) and Terraform remain the production path and are validated, not applied.

**Consequences.** The Codespaces URL is a **live demo environment**, not a production cloud deployment; it stops when idle. Demo scale (default 2,000 simulated vehicles) is chosen for a 4-core host. The same image is built and published by CI (`ghcr.io/sohamb4746y/voltsight-connected-vehicle-intelligence`).
