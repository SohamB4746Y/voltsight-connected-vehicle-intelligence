# Evidence index
Machine-produced results; each gate folder has a `status.md` stating what was and was not verified. Machine differences: *Win* (14-CPU Windows laptop, M0–M4) vs *box* (4-vCPU Linux sandbox, later work).

| Gate | Scope | Status file |
|---|---|---|
| G0 | toolchain, CI, base stack | [G0](G0/static-and-status.md) |
| G1 | contracts, PostgreSQL + RLS, identity, PKI, seed | [G1](G1/status.md) |
| G2 | simulator | [G2](G2/status.md) |
| G3 | gateway (mTLS, normalisation, back-pressure) | [G3](G3/status.md) |
| G4/G5 | stream worker, ClickHouse sink, reconciliation, the data-loss defect | [G4](G4/status.md) |
| G6 | range-risk engine, alerts, detection quality | [G6](G6/status.md) |
| G8 | consumption model vs baselines | [G8/ml_report.json](G8/ml_report.json) |
| G9 | API security tests | [G9](G9/status.md) |
| G9b | SQL optimisation, API latency | [G9b](G9b/plans/sqlopt.txt), [latency](G9b/api_latency.json) |
| G10 | Copilot, vector search | [G10](G10/status.md) |
| G11 | web console journey | [G11](G11/status.md) |
| G12 | security scans | [G12](G12/status.md) |
| G13 | chaos | [G13](G13/status.md) |
| G14 | load, capacity | [G14](G14/status.md) |
