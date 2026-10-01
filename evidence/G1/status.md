# Gate G1 status

Acceptance criteria: [`docs/gates/G1.md`](../../docs/gates/G1.md). Reproduce with `make gate-G1`.
Evidence bundle (clean tree, commit `031637e`): `evidence/G1/20261001T111419Z-031637e/`
(`results.json`, `go-test.json`, `go-test-summary.json`, `coverage.out`, `coverage.txt`, `proto.json`,
`static.json`, `mutation_rls.json`, `seed_run.json`, `wire_size.json`, `env.json`, `run.log`).

## Criteria
| Criterion | Status | Evidence (all MEASURED on this machine unless noted) |
|---|---|---|
| G1.1 buf lint/format/generate/breaking | PASS | `proto.json`: lint, format, regeneration diff-clean, `buf breaking` vs `HEAD` passes; **negative test**: a mutation (type change of `seq`, deleted `soc_pct`) is detected, an additive field is allowed |
| G1.2 telemetry contract | PASS | the problem statement's example event parses unchanged (strict, unknown fields rejected); 2,000-event binary + JSON round trips; **protobuf 197.5 B/event, protojson 545.6 B/event** over 5,000 random events (`wire_size.json`) |
| G1.3 migrations up/down/up | PASS | real PostgreSQL 16 + pgvector (Testcontainers); schema fingerprint identical after up/down/up; roles and tables removed by down |
| G1.4 schema lint | PASS | every table has a PK; tenant tables have `tenant_id NOT NULL`, RLS enabled **and forced**, an app-role policy; every FK has a supporting index; service roles are not superuser/BYPASSRLS/owners, hold no DDL/TRUNCATE; app role cannot write reference data |
| G1.5 RLS attack suite | PASS | 21 tenant tables: tenant A sees 0 of B's rows, cross-tenant INSERT/UPDATE/DELETE rejected or no-op with counts unchanged, unset/empty GUC returns nothing, malformed GUC errors, views and functions do not leak, `audit_log` append-only, service roles scoped; **real logins** as `voltsight_app`/`voltsight_gateway` (not just `SET ROLE`) |
| G1.5 mutation check | PASS | `mutation_rls.json`: replacing the policy with `USING (true)` makes the suite report **21 LEAKs**; migration restored byte-identical |
| G1.6 ACID | PASS | atomic rollback; alert idempotency key; composite FKs block cross-tenant references (5 cases); overlapping driver assignments rejected (exclusion constraint); check constraints; **100 rounds of racing plan approvals: exactly one winner each**; SQL `vin_valid` agrees with the Go implementation |
| G1.7 100K seed | PASS | exactly 100,000 vehicles, 0 invalid VINs in the database, counts and per-table digests match the manifest; same seed -> same manifest, other seed -> different; `--reset` reload repeatable; one-row tampering caught by digest alone; PII encrypted (AES-256-CBC, random IV, wrong key fails). **Load: 323,321 rows in 18.5 s (about 17.5K rows/s)**, `seed_run.json` |
| G1.8 Keycloak realm | PASS | realm auto-imported; all 9 demo users get tokens whose `sub` = `app_user.id`, `tenant_id` = seed tenant, role present; wrong password / client secret / unknown user / web-client password grant / bearer-only API login rejected; **PKCE S256 enforced** (missing and `plain` rejected), unregistered `redirect_uri` not honoured; **tenant-claim forgery attempt through the Account API** (attacker authorised, profile readable, forged attribute accepted at protocol level) leaves the next token's `tenant_id` unchanged; realm hardening read back via the admin API |
| G1.9 Vault PKI | PASS | connector certs: URI SAN identity, ClientAuth only, ECDSA; chain verifies; revoked serial appears in the CRL and is rejected, other certs unaffected; expired rejected; look-alike CA rejected; lifetime capped by the role; role refuses foreign URI SANs and common names |
| G1.10 ER + 3NF | PASS | `docs/er/` regenerated from the live schema and equal to the committed files; `docs/3nf.md` has a section for each of the 31 tables and none for non-existent ones |
| G1.11 unit coverage, static, SAST | PASS | **89 tests, 0 failed, 0 skipped**; coverage: vin 100.0, geo 100.0, seedgen 98.8, dotenv 94.4, jwtverify 93.2, realm 92.3, erdiagram 89.2, pki 82.0, dbtool 81.8 (threshold 80); gofmt/vet/staticcheck/gitleaks clean; Semgrep (golang, secrets, dockerfile) 0 findings with the waiver below |
| G1.12a regression | PASS | G0 health and functional probes re-run: exit 0 |
| G1.12b GitHub CI green | **PARTIAL (not yet confirmed green)** | CI run `36854511933` (commit `a95f618`): on the Linux runner **all G1 criteria passed** (89 tests, 0 failed/skipped; coverage, RLS mutation check, static, Semgrep) and only the G0 regression step failed, because G0.1-G0.3 describe the developer machine. Fixed in `dd56cea` (`--stack-only`: those criteria are recorded SKIPPED, never PASS). The CI run for `dd56cea` (`36855616757`) was still in progress when this was written; its result is **not recorded here and must be read from GitHub** before G1.12b is called PASS |

## Defects found by this gate and fixed (iteration history)
1. Charger primary-key collision at full size (depot names repeated across fleets) — found by the first 100K load; fixed, regression test `TestIdentitiesUniqueAtFullSize`.
2. Audit partition could not be created once rows had landed in the default partition — fixed by pre-creating 36 monthly partitions in the migration; test asserts the default partition stays empty.
3. Keycloak access tokens had no `sub` and no account audience (own `clientScopes` in the realm file suppressed Keycloak's built-ins) — mappers moved onto the clients.
4. The first tenant-forgery test passed vacuously (Account API returned 401) — rebuilt so the attacker is authorised and the precondition is asserted.
5. `OwnerDSN` corrupted passwords containing a space (query-style escaping in userinfo) — found by the new unit test; fixed.
6. staticcheck SA1019: ECDSA keys built from raw `X`/`Y` — replaced with `ecdsa.ParseUncompressedPublicKey` (also rejects off-curve points; regression test).
7. Gate-runner bugs (cp1252 decoding of UTF-8 test output; `buf breaking` invoked outside the module) — fixed; the gate had correctly failed instead of passing.
8. `dbtool` coverage was 74.6% (no tests for `OwnerDSN`, `BootstrapRoles`) — tests added, now 81.8%.

## Limitations and waivers (not hidden)
- **Tenant GUC is caller-controlled.** A database session that sets `app.tenant_id` itself sees that tenant's rows (asserted by `TestKnownLimitation_TenantGUCIsCallerControlled`). Isolation therefore depends on the API deriving the tenant from the verified JWT and using `SET LOCAL` per request; that is tested at gate G9, not here.
- Business rule "a vehicle's home depot belongs to its fleet" is enforced by the generator and its tests, not by a database constraint (documented in `docs/3nf.md`).
- Semgrep waiver: `math/rand/v2` in `internal/seedgen/world.go` is a seeded PRNG for deterministic synthetic data, not security-relevant.
- `voltsight-test` Keycloak client (direct password grant) exists for automated tests only and must be omitted from production realms.
- At-rest encryption of volumes is not part of G1 (gate G12); the PII column is encrypted in the database.
- `cmd/*` programs have no Go unit tests; they are exercised by the real `make bootstrap seed` run and by the gate. Coverage thresholds apply to the library packages listed above.
- Environment: on Docker Desktop for Windows the Testcontainers reaper (Ryuk) needs `TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE=//var/run/docker.sock`; Ryuk stays enabled. A native PostgreSQL occupies host port 5432, so the compose database is published on 55432.
- The seed load rate depends on this laptop (16 logical cores, Docker limited to 11.5 GB); it is a measurement, not a capacity claim.
