# OWASP Top 10 (2021) and API Security Top 10 (2023) mapping
Each row points to the control and an automated test. "—" means no test exists (stated, not hidden).

| Item | Control in VoltSight | Test |
|---|---|---|
| A01 Broken access control / API1 BOLA | RLS + explicit tenant predicate, 404 for foreign ids, RBAC table | `TestTenantIsolationAttacks`, `TestRBACAndEntitlements` |
| A02 Cryptographic failures | TLS 1.3 (gateway, API `-tls`), mTLS for devices, KMS in IaC, hashed audit chain | `evidence/G12/tls_api.txt`, G3 handshake tests |
| A03 Injection | parameterised SQL, filter whitelists, CH named params | injection cases; semgrep |
| A04 Insecure design | threat model, two-phase writes, budgets | `docs/threat-model.md` |
| A05 Security misconfiguration | secure headers (CSP, nosniff, frame deny, HSTS), non-root read-only pods | headers set in `ServeHTTP`; no automated header test — |
| A06 Vulnerable components | govulncheck, npm audit, Trivy | `evidence/G12` |
| A07 Identification & authentication failures / API2 | Keycloak OIDC + PKCE, JWT validation | `TestAuthenticationRejectsBadTokens` |
| A08 Software & data integrity | audit hash chain, signed device certs, replay-safe pipeline | `internal/auditchain` |
| A09 Logging & monitoring failures | audit trail, request ids, Prometheus metrics | `TestAuditCompleteness` |
| A10 SSRF | no outbound fetch of user-supplied URLs; Copilot tools take no URLs | by construction — |
| API3 Broken object property level authorization | role-based masking of evidence/locations; `DisallowUnknownFields` (no mass assignment) | `TestLocationMaskingByRole`, copilot message validation |
| API4 Unrestricted resource consumption | rate limit, body limit, page-size caps, Copilot budgets | `TestRateLimit` |
| API5 Broken function level authorization | permission per route | `TestRBACAndEntitlements` |
| API6 Sensitive business flows | plan approval single-use/human-only | `TestWritesNeedHumanApproval` |
| API7 SSRF | see A10 | — |
| API8 Security misconfiguration | see A05 | — |
| API9 Improper inventory | one route table in `routes.go`; no undocumented debug routes | — |
| API10 Unsafe consumption of APIs | LLM/provider output validated against the tool allow-list and schemas | copilot guardrail tests |
