# Testing summary

Only categories that were actually run are marked RUN. Matrix rows: TS-01…TS-07 in [`../compliance/FINAL_REQUIREMENT_MATRIX.md`](../compliance/FINAL_REQUIREMENT_MATRIX.md).

| Category | Status | What was run | Evidence |
|---|---|---|---|
| Unit (Go) | RUN | `make test`; CI runs `go test ./internal/... ./cmd/... ./tools/reqtrace/...`; coverage gated at ≥ 80% on the G1 packages (vin 100%, jwtverify 93%, realm ~95%, pki ~81%, dbtool ~82%) | `evidence/G1/status.md` |
| Unit (web) | RUN | 14 vitest tests for the token lifecycle: valid token, expired → refresh, margin, 401 renew + single retry, concurrent single refresh, fatal vs transient refresh errors, SSE fresh token and back-off | `web/src/auth.test.ts` |
| Coverage of later packages (rangerisk, api, copilot, batch) | NOT MEASURED | tests exist; percentage not recorded | matrix TS-01 |
| Integration | RUN | `make test-integration` and `go test -tags stack` against real Kafka, PostgreSQL, ClickHouse, Redis, Keycloak, Vault (RLS attack suite on every tenant table, ACID, idempotency) | `evidence/G1/status.md` |
| Map endpoint stack test | RUN IN CI ONLY | `TestMapEndpointsAreTenantBoundAndShaped`; not run locally | `internal/api/api_stack_test.go` |
| E2E, 100K vehicles | RUN | live 60 s run through every stage with reconciliation | `evidence/G4/status.md` |
| E2E, browser | RUN | Playwright: sign-in, dashboard, map markers from backend data, session across a token expiry | `evidence/live-deployment/auth-map/` |
| Scripted live checks | RUN | 31/31: authentication, RBAC, tenant isolation, CORS, IdP exposure, headers, Copilot, API latency | `evidence/live-deployment/live_checks.json` |
| Security (SAST, deps, secrets, image) | RUN | semgrep (1 waived), gitleaks, govulncheck, npm audit, Trivy fs and image (0 HIGH/CRITICAL), TLS checks, SBOM | `evidence/G12/status.md`, `evidence/live-deployment/image-scan.txt`, `evidence/security/sbom-image.cdx.json` |
| DAST | RUN, PARTIAL | OWASP ZAP 2.17.0 baseline, unauthenticated, against the live edge: 58 passed, 0 failures, 3 warning groups; ran before the CSP was tightened and was not re-run | `evidence/security/zap/` |
| Chaos / restart | RUN | worker-kill, sink-kill, redis-restart, clickhouse-restart, kafka-restart at 100K vehicles: 0 events lost; full-stack restart recovery. Gateway-kill and Kubernetes pod kill not run | `docs/testing/CHAOS_RESULTS.md`, `evidence/G13/status.md`, `evidence/live-deployment/restart-recovery.txt` |
| Contract | PARTIAL | Protobuf round-trip and `buf breaking` tests (with a negative test); **no Pact** | `evidence/G1/status.md` |
| BDD | NOT IMPLEMENTED | browser scripts are assertion suites, not Gherkin | matrix TS-04 (FAIL) |
| Load | RUN | 100K vehicles 60 s (98.3K ev/s); 3× burst 40 s; API 829 req/s | `evidence/G4`, `evidence/G14`, `evidence/G9b` |
| Soak | NOT RUN | | matrix TS-05 |
| Accessibility | NOT RUN | | matrix MB-04 |
| Copilot scenarios | RUN | 8 scenarios incl. injection and cross-tenant attempts, with the deterministic stub provider | `evidence/G10/status.md` |
| ML evaluation | RUN | two hold-outs, leakage checks | `evidence/G8/ml_report.json` |
| Traceability gate | RUN | `go run ./tools/reqtrace` (also a CI step): every PASS needs existing evidence paths | `tools/reqtrace` |
