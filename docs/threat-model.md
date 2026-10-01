# Threat model (STRIDE)

Trust zones: **device** (untrusted) → **edge** (gateway, API listener) → **services** → **data** → **LLM** (untrusted output). Each threat lists the implemented control and the test that exercises it. "Residual" marks what is not covered.

| # | Zone | STRIDE | Threat | Control | Test |
|---|---|---|---|---|---|
| T1 | device | Spoofing | a device pretends to be another vehicle/tenant | mTLS TLS1.3; certificate chain + registered fingerprint + CRL; tenant derived from the certificate, payload tenant ignored | `internal/ingest` stack tests: 8 rejection cases (no cert, wrong CA, unregistered, mismatched, revoked ×2, expired, TLS1.2) |
| T2 | device | Tampering | malformed / oversized / out-of-range payloads | strict validation (VIN check digit, DTC regex, ranges), DLQ with reason, body size limit | `internal/normalise` tests, gateway DLQ test |
| T3 | device | DoS | event flood | inflight byte cap → 429 + Retry-After; per-connector limit | G3 back-pressure test (106 refusals, 0 loss) |
| T4 | edge | Spoofing | forged / expired / alg=none JWT | RS256 pinned, issuer + audience + expiry checks, JWKS key-id lookup with refresh limits | `TestAuthenticationRejectsBadTokens`, `internal/jwtverify` tests, Keycloak forgery test (tenant attribute cannot be self-edited) |
| T5 | edge | Elevation | role escalation | permission table in code; roles only from the signed token; subscription entitlements | `TestRBACAndEntitlements` |
| T6 | edge | Info disclosure | cross-tenant read by guessing ids (BOLA) | RLS + explicit tenant predicate; 404 for foreign ids | `TestTenantIsolationAttacks`, G1 RLS suite |
| T7 | edge | Tampering | SQL / ClickHouse injection | parameterised queries only; whitelist filters; table names validated by regex | injection cases in `TestTenantIsolationAttacks`; semgrep clean |
| T8 | edge | DoS | API abuse | per-user token bucket (429), request body limit, keyset pagination caps | `TestRateLimit`, `TestKeysetPagination` |
| T9 | services | Tampering | silent change of audit history | append-only inserts; separate sealer role writes only chain columns; hash chain verifier | `internal/auditchain` test (modify/delete/inject detected; sealer cannot rewrite) |
| T10 | services | Repudiation | a user denies an action | every data access/write audited with actor, request id, status; Copilot steps audited | `TestAuditCompleteness`, copilot audit test |
| T11 | services | Info disclosure | precise driver location to a viewer | role-based masking (2-decimal coordinates, no route) | `TestLocationMaskingByRole` |
| T12 | services | Elevation | service compromise widens access | one PostgreSQL role per service with minimal grants; pods non-root, read-only FS, dropped capabilities; NetworkPolicy default-deny | G1 grant tests; Helm lint/kubeconform (cluster behaviour **not** exercised) |
| T13 | data | Info disclosure | stolen disks/backups | KMS/CMEK encryption for all managed stores in Terraform | `terraform validate`, Trivy IaC (applied: **no**); local volumes unencrypted — LIMITATION |
| T14 | data | Tampering | replayed/duplicated telemetry | exact dedup window, idempotent producer, ReplacingMergeTree, alert unique key | G4 tests, chaos runs |
| T15 | data | Repudiation/Privacy | personal data cannot be erased | PII isolated in `driver.pii_enc`; erasure worker + verification evidence | `TestErasureFlowEndToEnd` |
| T16 | LLM | Tampering | prompt injection through incident text | retrieved text flagged untrusted; tool results never create tool calls by themselves; the provider can only pick allow-listed tools which are validated and authorised independently; writes need a human approval | `TestPromptInjectionInRetrievedTextExecutesNothing`, `TestGuardrailsRejectUnknownMalformedAndUnauthorisedCalls` |
| T17 | LLM | Elevation | agent approves its own plan | approval endpoint is separate, needs `plans.write`, single-use, tenant-bound | `TestWritesNeedHumanApproval` |
| T18 | LLM | Info disclosure | cross-tenant request via the model | tenant never an argument; foreign VIN indistinguishable from unknown | `TestCrossTenantVehicleIsIndistinguishableFromUnknown` |
| T19 | all | Info disclosure | secrets in repo | gitleaks (history clean), generated `.env` is git-ignored, Vault for PKI; Kubernetes secrets by reference | gitleaks evidence |

**Residual / not done:** DAST (ZAP) was not run; the image scan was not run; mutual TLS between services inside the cluster is left to the platform (service mesh optional); local Docker volumes are unencrypted; the LLM provider has not been exercised against a real model.
