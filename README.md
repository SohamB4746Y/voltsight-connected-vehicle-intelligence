# VoltSight

EV fleet range-risk detection and charge orchestration for a 100,000-vehicle connected fleet,
built for the Connected Vehicle Intelligence hackathon. Motorq is used only as an industry
reference in the problem statement; this project has no affiliation with Motorq.

**Status: work in progress (milestone M0).** Nothing in this README claims a capability that
has not been demonstrated by evidence under [`evidence/`](evidence/). Numbers will appear here
only when rendered from that evidence.

CI: [GitHub Actions](https://github.com/SohamB4746Y/voltsight-connected-vehicle-intelligence/actions)
(private repository). Gate G0 evidence: [`evidence/G0/`](evidence/G0/static-and-status.md).

## Quick start (base infrastructure only, so far)

Prerequisites: Docker Desktop (WSL2 backend, >= 11 GB memory), Python 3.12, GNU make.

```bash
make up          # generates .env with random credentials, starts the stack, waits for healthy
make gate-G0     # functional probes of every service + evidence under evidence/G0/
make nuke        # remove containers and volumes
```

## Repository layout (grows with the milestones)

`deploy/` compose, Helm and Terraform · `db/` schema and migrations · `tools/` dev and gate
tooling · `docs/` architecture, ADRs, gate definitions · `evidence/` machine-produced results.
