# Live deployment

> Status of this document: written while the live profile was being built and verified. Every measured value
> below is copied from `evidence/live-deployment/`; anything not measured says so. Nothing here claims a public
> URL exists unless the **Public URL** section names one with a verification timestamp.

## 1. What "live" means here

The same platform that runs on a laptop (`docs/architecture.md`) runs as one Docker Compose project behind one
HTTPS origin: Kafka, PostgreSQL + pgvector, Redis, ClickHouse, Keycloak, Vault, Prometheus/Grafana/OTel, the
mTLS ingest gateway, the stream worker with the range-risk engine, the ClickHouse sink, the alert service, the
API + web console, and the simulator. Nothing is mocked and no component was removed to make it fit.

```
Internet ──HTTPS──▶ edge TLS (Codespaces forwarder, or Caddy + Let's Encrypt on a VM)
                       │
                     Caddy :8080  (the only published port)
                       ├── /realms/voltsight/*, /resources/*  ─▶ Keycloak 26 (production mode, public realm)
                       └── everything else                    ─▶ vsapi  (console + /v1 API + SSE)
                                                                   │  JWT (RS256) verified against Keycloak JWKS
        simulator ─mTLS 1.3─▶ gateway ─▶ Kafka ─┬▶ worker (dedup, Bloom, Count-Min, range-risk) ─▶ alerts.v1 ─▶ alert svc ─▶ PostgreSQL + Redis ─▶ SSE
        (Vault-issued certs)                    └▶ sink ─▶ ClickHouse (history, batch analytics)
```

Entry point: `deploy/live/up.sh`. Compose files: `deploy/compose/docker-compose.yml` (infrastructure, shared with
development) + `deploy/compose/docker-compose.live.yml` (public profile). Image: `deploy/docker/Dockerfile`
(one 75 MB distroless, non-root image that carries all service binaries and the console).

### What differs from the development stack

| Concern | Development | Live profile |
|---|---|---|
| Published ports | every infrastructure port on the host | **only** the proxy (`8080`, or `80/443` with the `tls` profile) |
| Keycloak | `start-dev`, localhost redirects, DEV-only password-grant client | `start`, hostname = public URL, **public realm**: only redirect `${PUBLIC_URL}/*`, no password-grant client, no test secret |
| API CORS | localhost origins | exactly `PUBLIC_URL` (`-origins`); a wildcard is rejected in code |
| Services | host binaries | project image, read-only root FS, all capabilities dropped, `no-new-privileges` |
| Simulator | 100,000 vehicles, 60 s benchmark runs | `DEMO_VEHICLES` (default 2,000) streaming continuously, same code path |
| Observability | Prometheus/Grafana/OTel on host ports | same components, internal only |

## 2. Free-tier investigation (verified 2026-10-01)

The brief asks for free hosting only (no card). Current provider terms were read from their own documentation
today rather than assumed:

| Provider | What was verified | Verdict for this platform |
|---|---|---|
| **Hugging Face Spaces** | Docker Spaces "run on compute and require a paid plan to create"; only Static Spaces are free (`huggingface.co/docs/hub/spaces-overview`). | Not usable: the platform needs containers. |
| **Render** | Free web services spin down after 15 min idle (~1 min to wake), 750 free instance hours/month; free Postgres is 1 GB and **expires after 30 days**; free Key Value "does not continually persist … all data is lost on restart"; background workers, private services and persistent disks are not part of the free plan (`render.com/docs/free`). | Cannot host Kafka, ClickHouse, Keycloak, Vault, a stream worker and a gateway. Hosting only the UI/API there would mean removing core components, which was ruled out. |
| **Koyeb** | Pricing page lists Pro ($29/month) and above; no free compute tier (a 5-hour free Postgres only). | Not usable. |
| **Oracle Cloud Always Free** | Ampere A1 up to 2 OCPU / 12 GB (per the page fetched), 200 GB block storage. The page does not state whether sign-up verification needs a payment card. | Technically the best fit for an always-on host (the whole compose project fits). Account creation is a human action; whether a card is required is not stated by the source. |
| **GitHub Codespaces** | Free personal allowance; machine up to 4 cores / 16 GB; forwarded ports get an HTTPS URL that can be made public. No card needed for the free allowance. | Runs the entire stack unchanged. Not always-on: the codespace stops after an idle timeout. |
| Cloudflare Pages/Workers | Static/edge functions only for the free tier; no long-running containers or Kafka. | Not usable for the stateful platform. |

Decision: **one compose project that runs unchanged on any Docker host.** The free, no-card way to put it on a
public HTTPS URL is a GitHub Codespace (section 5); an always-on free host is possible on an Oracle Always Free
VM or any VPS with the `tls` profile (section 6). Neither can be created from inside the build sandbox: both need
the owner's account, so the final step is a human action, stated exactly in section 5.

## 3. Components: real, substituted, not hosted

| Component | Live status |
|---|---|
| Kafka (KRaft, 64 partitions), PostgreSQL 16 + pgvector (RLS), Redis, ClickHouse, Keycloak (OIDC + PKCE), Vault (PKI for device mTLS) | **Real**, same images and configuration as development. |
| Vault | **Dev-mode** Vault, in-memory, root token generated per deployment and never published. The device CA is recreated when Vault restarts (`up.sh` re-runs `vspki bootstrap`). Not a production secret store. |
| Gateway, worker (range-risk, ML-free estimator `ewma`), sink, alert service, API, web console, simulator | **Real**, from the project image. |
| Copilot | Deterministic **stub** provider unless `ANTHROPIC_API_KEY` is supplied; typed tools, guardrails, tenant enforcement, citations, approval-gated writes and audit are real either way. The stub is not an LLM. |
| Prometheus, Grafana, OTel collector | Real, internal only (reach them with port forwarding). |
| Object store (cold tier) | Disabled in the live profile (profile `disabled`): the Parquet archive is documented as not implemented. |
| Kubernetes | Helm chart is linted/rendered/validated, see section 8. **No public Kubernetes cluster exists**; none is claimed. |
| Terraform | Validated only; not applied (needs paid cloud resources). |

## 4. Scale, honestly

The live profile streams `DEMO_VEHICLES` vehicles (default 2,000, i.e. about 2,000 events/s at 1 Hz) through the
real pipeline. The 100,000-vehicle simulator, the 100,000-vehicle database seed and the benchmark scripts are
unchanged and remain in the repository; their measured results are in `docs/capacity.md` and `evidence/G14/`
(98.3K events/s for 60 s on a 4-core host). The 5 s alert-latency target was **not met** on that host and no live
deployment result changes that. Nothing in this document claims 100K events/s, a 3x burst, or the latency
targets on the free deployment.

<!-- sections 5 onwards are completed after verification -->
