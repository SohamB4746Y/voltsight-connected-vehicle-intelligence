# Compliance mapping (awareness level — this is not a certification)
| Regulation / standard | Requirement | Control | Evidence |
|---|---|---|---|
| GDPR Art. 5(1)(c),(e) / DPDP Act s.8 | minimisation, storage limitation | telemetry carries VIN only (no driver identity); ClickHouse TTL 30 days; Kafka retention 72 h | `internal/sink` DDL; `internal/kafkautil` |
| GDPR Art. 17 / DPDP s.12 | erasure | driver PII in one encrypted column; erasure worker + verification record | `TestErasureFlowEndToEnd` |
| GDPR Art. 25, 32 / DPDP s.8(5) | privacy by design, security | RLS, role masking of location, TLS 1.3, audit chain | tests in `docs/owasp-map.md` |
| GDPR Art. 30 | records of processing | audit log of every access with request id | `TestAuditCompleteness` |
| UNECE R155 (CSMS) | threat analysis, secure comms, monitoring | STRIDE model, mTLS device identity, revocation, anomaly alerts | `docs/threat-model.md`, G3 |
| UNECE R156 (software updates) | update traceability | model/schema versioning (`schema_ver`, `model_version` table, hashed artefact); OTA schema v2 roll-out tolerated | G3 schema-v2 test; `evidence/G8` |
**Not covered:** data-subject *export*, consent management, cross-border transfer controls, retention of PostgreSQL audit rows (no purge job: the hash chain would need a documented anchor), erasure of cold Parquet (not built).
