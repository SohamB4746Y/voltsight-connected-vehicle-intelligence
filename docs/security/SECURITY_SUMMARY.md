# Security summary

Detail: threat model [`STRIDE.md`](STRIDE.md), OWASP mapping [`../owasp-map.md`](../owasp-map.md), compliance [`../compliance.md`](../compliance.md), tenant isolation [ADR-006](../adr/006-tenant-isolation.md), session lifecycle [ADR-009](../adr/009-session-and-token-lifecycle.md).

## Controls

| Control | Implementation | Evidence |
|---|---|---|
| Authentication | Keycloak OIDC, Authorization Code + PKCE for the web client; password grant refused; the DEV-only test client does not exist in the public realm | `evidence/live-deployment/live_checks.json` (EXP-3, EXP-4) |
| Tokens / JWT | RS256 validated against JWKS (`internal/jwtverify`); 300 s access tokens; refresh-token grant with single-flight refresh, one retry on 401, fatal vs transient errors | `web/src/auth.test.ts` |
| RBAC | viewer / dispatcher / energy manager / tenant admin / platform admin plus subscription entitlements; e.g. viewer cannot acknowledge alerts or use the Copilot, only tenant admin reads the audit log | live_checks RBAC-1…6 |
| Tenant isolation | Tenant from token claim; PostgreSQL RLS enabled and forced on every tenant table; composite tenant foreign keys; no role with BYPASSRLS; cross-tenant reads return 404 | `evidence/G1/status.md` (RLS attack suite), live_checks ISO-1…4 |
| Transport | Devices: HTTPS/2 + mutual TLS 1.3, certificate per tenant×OEM connector from Vault PKI, tenant from URI SAN. Browsers: TLS at the Caddy edge | `evidence/G12/status.md` |
| Secrets | Generated per deployment into git-ignored `.env`; gitleaks clean | `evidence/G12/status.md`, `evidence/live-deployment/secrets-scan.txt` |
| OWASP controls | Input validation (VIN/DTC/schema, strict JSON, size and rate limits), CORS allow-list of the deployment origin, security headers (CSP, HSTS, nosniff, DENY framing), Keycloak admin/master realm/metrics blocked at the proxy | live_checks CORS-1…3, EXP-1…5 |
| Audit | Append-only `audit_log` with request id, hash chain (`internal/auditchain`); AI actions audited | `evidence/G1/status.md` |
| Privacy | Role-based location masking; erasure workflow; synthetic data only | `docs/compliance.md` |
| Supply chain | govulncheck, npm audit 0, Trivy, SBOM (CycloneDX); MapLibre pinned to 6.11.2 after an npm-audit critical advisory on ≤ 6.4.0 | `evidence/security/sbom-image.cdx.json` |

## ZAP DAST result (honest)

OWASP ZAP 2.17.0 baseline (spider, depth 1) against the edge proxy on 2026-10-01: **58 checks passed, 0 failures, 3 warning groups**.

| Warning | Risk | Note |
|---|---|---|
| CSP: Failure to Define Directive with No Fallback | Medium | reported on the static SPA responses |
| CSP: `style-src` unsafe-inline | Medium | inline styles used by the UI/MapLibre |
| Timestamp Disclosure – Unix | Low | numeric values resembling timestamps |
| Modern Web Application | Informational | ZAP note that the app is a SPA |

Scope limits: unauthenticated surface only; no authenticated or active scan; the scan ran **before** the CSP-tightening commit, so the CSP findings may differ now and were not re-run. Files: [`evidence/security/zap/`](../../evidence/security/zap/).

## Known gaps

Vault runs in dev mode (in-memory); passwords come from a generated `.env`, not a secret manager; three OWASP items have no dedicated test; PostgreSQL audit-retention purge not implemented; no authenticated DAST; no legal-compliance claim (GDPR/DPDP/UNECE are mapped at awareness level).
