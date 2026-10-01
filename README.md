# VoltSight

EV fleet range-risk detection and charge orchestration for a 100,000-vehicle connected fleet,
built for the Connected Vehicle Intelligence hackathon. Motorq is used only as an industry
reference in the problem statement; this project has no affiliation with Motorq.

**Status: work in progress (milestones M0-M1 complete).** Nothing in this README claims a capability
that has not been demonstrated by evidence under [`evidence/`](evidence/). Numbers will appear here
only when rendered from that evidence.

| Gate | Status | Evidence |
|---|---|---|
| G0 toolchain, repo, CI, base infrastructure | PASS | [`evidence/G0`](evidence/G0/static-and-status.md) |
| G1 contracts, PostgreSQL core, tenant isolation, identity, device PKI, 100K seed | see [`evidence/G1`](evidence/G1/status.md) | `evidence/G1` |

CI: [GitHub Actions](https://github.com/SohamB4746Y/voltsight-connected-vehicle-intelligence/actions)
(private repository).

## Quick start

Prerequisites: Docker Desktop (WSL2 backend, >= 11 GB memory), Go 1.27, Python 3.12, GNU make, buf.

```bash
make up          # random .env credentials, start the stack, migrate the DB, set role passwords, create the device CA
make seed        # deterministic 100,000-vehicle dataset, verified against db/seed/manifest.json
make gate-G1     # full G1 evidence run (contracts, DB, tenant-isolation attacks, Keycloak, Vault, static, coverage)
make nuke        # remove containers and volumes
```

On Windows, run `make` from Git Bash (or `mingw32-make`). Testcontainers needs
`TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE=//var/run/docker.sock` on Docker Desktop for Windows (the gate
runner sets it); Ryuk stays enabled.

The compose PostgreSQL is published on host port **55432** because a native PostgreSQL often owns 5432.

## What exists so far

- `proto/` versioned protobuf contracts (telemetry, alerts, charger status, DLQ) with `buf` lint, format and breaking-change checks; field names mirror the problem statement's example event, which parses unchanged.
- `db/migrations/` PostgreSQL 16 schema (31 tables, 3NF with one documented, FK-enforced denormalisation), forced row-level security, least-privilege service roles; ER diagram in [`docs/er`](docs/er/README.md), normal-form analysis in [`docs/3nf.md`](docs/3nf.md).
- `internal/seedgen`, `cmd/vsdb` deterministic synthetic master data (100,000 vehicles with ISO 3779 VINs), bulk loader and manifest verifier.
- `internal/pki`, `cmd/vspki` per-connector mTLS client certificates from a Vault PKI engine.
- `internal/realm`, `internal/jwtverify` Keycloak realm generated from the seed identities, and a JWT verifier.

## Repository layout (grows with the milestones)

`proto/` contracts · `db/` schema, seed manifest, DB tests · `cmd/` CLIs · `internal/` libraries ·
`deploy/` compose (Helm and Terraform to come) · `tools/` dev and gate tooling · `docs/` architecture,
ADRs, gate definitions · `evidence/` machine-produced results.
