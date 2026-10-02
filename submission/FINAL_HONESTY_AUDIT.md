# Final honesty audit

No marketing language. “Measured” = a file in `evidence/` records it. All measurements: 4-vCPU Linux sandbox, every component on one host, synthetic data.

## Directly measured
- 100,000 vehicles streamed through the mTLS gateway: 98.3K events/s for 60 s, 6.0M events, 0 failed, ClickHouse distinct events = worker distinct events (evidence/G4).
- 3× burst for 40 s inside a 100 s run: 18.0M events, 0 lost (evidence/G14).
- Five fault-injection scenarios (worker, sink, Redis, ClickHouse, Kafka restarts): 0 events lost after fixing two defects found by the method (evidence/G13).
- API p50 16.9 / p95 26.8 / p99 41.0 ms at 829 req/s, 16 clients, ingest idle (evidence/G9b).
- EXPLAIN ANALYZE before/after for six queries; the AFTER inbox probe returned 0 rows (stated in the document).
- Alert latency at 100K ev/s: receive→persisted p50 9.8 s, p99 17.0 s (evidence/G14). At demo scale receive→detect p50 28 ms, p95 133 ms, n=33 (evidence/live-deployment).
- ML: MAE 0.01736 vs 0.0274 kWh/km (vehicle hold-out) and 0.01718 vs 0.02489 (time hold-out) on simulated data (evidence/G8).
- Security: 31/31 scripted live checks; ZAP baseline 58 passed, 0 failures, 3 warning groups (unauthenticated, before CSP tightening); gitleaks/govulncheck/npm audit/semgrep/Trivy results (evidence/G12, live-deployment, security).
- Browser: dashboard, map markers (800 vehicles, 1,594 chargers), alert evidence with A* route, Copilot answer, audit page; session kept across a token expiry in an earlier run (90 s lifetime).

## Architecture targets (not achieved results)
- 100K events/s sustained, 3× burst for 5 minutes, < 5 s critical-alert latency, < 2 s dashboard latency, horizontal scale-out, 99.9% availability, hot/warm/cold lifecycle with a Parquet cold tier, production cloud deployment.

## Partially demonstrated
- Map (markers, clusters, filters, route verified; real basemap not seen rendering); DAST (unauthenticated baseline); Helm (API-server acceptance in CI, not a full pod run); Terraform (validate only); contract tests (Protobuf, no Pact); load (60 s and 40 s, not the specified durations).

## Blocked
- Tag `v1.0-submission` on GitHub (push refused HTTP 403; local tag on an earlier commit); production cloud deployment (needs the owner's cloud account).

## Missing / failed
- Critical alert < 5 s at 100K ev/s: FAIL (single host). BDD acceptance tests: not implemented. Soak, scale-out, authenticated/active DAST, accessibility scan, billion-row batch, Loki/Tempo, Parquet archive: not done.

## Screenshots and video
- All 14 screenshots in `03_Technical_Artifacts/15_Screenshots` are unedited captures of the live profile running in the build sandbox on 2026-10-02 (not the owner's Codespaces URL). The map shows the markers-only fallback.
- Video: browser scenes are recordings of that same running application; slides are built from repository content; narration is synthetic speech. No mock UI.

## Performance, security, deployment, ML, agent claims
- Performance: every number above has a file; the document separates target and measured and marks the alert target FAIL.
- Security: controls are backed by scripted checks or tests; Vault is dev mode; secrets are generated per deployment; no legal compliance claim.
- Deployment: Docker, Compose and the Codespaces profile verified; Helm/Terraform validated only; Codespaces is a live demo environment, not production.
- ML: simulated labels; no real-fleet claim; GBM is a batch artifact.
- Agentic AI: 8 tools, guardrails and 8 scenarios tested with a deterministic stub provider; the Anthropic provider was never exercised.

## Live-demo caveat found during this work
- The Codespaces demo runs the image published from `main`. A real-browser run in this sandbox on an earlier local image (built before commit 683bf6e) showed MapLibre “Worker failed to load” (blank map). The fix is merged to `main` (PR #4), so a re-pull of the new GHCR image (`deploy/live/up.sh` after the CI image job finishes) is needed for the Codespaces map to draw; this was not verified on the Codespaces URL.
