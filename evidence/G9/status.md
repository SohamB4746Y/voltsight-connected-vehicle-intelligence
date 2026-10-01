# Gate G9 status (API, authentication/authorisation, tenant isolation) — PASS locally; Pact contracts NOT DONE
Run: `go test -tags stack -count=1 ./internal/api` against the compose stack (Keycloak, PostgreSQL, Redis). Results from the last run in this session: all pass.
| Criterion | Test |
|---|---|
| OIDC token validation: missing, garbage, `alg=none`, tampered claims, bad signature → 401; valid → 200 | `TestAuthenticationRejectsBadTokens` |
| RBAC matrix + subscription entitlements (pro plan has no Copilot; starter/pro/enterprise features) | `TestRBACAndEntitlements` |
| BOLA/IDOR across tenants (vehicle, telemetry, alert read/ack, charger status) → 404; list endpoints never leak; injection attempts inert | `TestTenantIsolationAttacks` |
| Audit row per data access (7 reads → 7 rows) | `TestAuditCompleteness` |
| Per-user rate limit → 429 | `TestRateLimit` |
| Keyset pagination: disjoint, ordered, complete | `TestKeysetPagination` |
| No N+1: 8 statements per request for 5 and for 200 rows | `TestNoNPlusOneQueries` |
| Role-based location masking | `TestLocationMaskingByRole` |
| Erasure through API + worker with verification | `TestErasureFlowEndToEnd` |
Also: `internal/auditchain` (hash chain tamper detection), `internal/alerts` (idempotency + RLS). **Not done:** Pact consumer/provider contracts; cache-invalidation ≤ 2 s test.
