# CAP and PACELC for VoltSight

Scope: the single-region deployment described in `docs/architecture.md`. "Partition" means a network partition between a service and a store, or between the Kafka brokers. The local and Codespaces profiles run one broker and one instance of each store, so the **availability** column below describes the designed behaviour and what was tested (see `docs/testing/CHAOS_RESULTS.md`); replication exists only in the Helm/Terraform design and was **not** exercised.

PACELC reads: during a Partition choose Availability or Consistency; Else (normal operation) choose Latency or Consistency.

| Component / data | Choice (P→ / E→) | What that means here | Behaviour under failure (tested unless marked) |
|---|---|---|---|
| PostgreSQL: tenancy, users, plans, approvals, alert lifecycle, audit log, erasure requests | **PC / EC** | synchronous commit, row-level security, unique keys; a write either commits or fails, never half-applies | PostgreSQL down: API write endpoints and Copilot approvals return 5xx and nothing is queued behind the user's back; reads that need it fail; the stream path keeps running because it does not depend on PostgreSQL for decisions (alerts are retried by the alert service until PostgreSQL returns; offsets are committed only after the insert). Failover **not tested** |
| Kafka `telemetry.v1` (64 partitions, keyed by VIN) | **PA / EL** (producers `acks=all` at the gateway, one broker locally) | per-VIN order inside a partition; durability traded against latency by batching; replication factor 3 is design only | broker restart: gateway retries (469 duplicate records observed and collapsed downstream), no loss, workers resume after the consumer-group rebalance (`evidence/G13`) |
| Redis hot state (latest vehicle state, dedup windows, charger status mirror, SSE fan-out) | **PA / EL** | eventually consistent with Kafka; rebuildable from the log by replay | Redis restart: the worker's bounded retry rides over the `LOADING` window (defect found and fixed); dashboards show stale or empty state until the stream repopulates, nothing is lost because Redis is not the system of record |
| ClickHouse telemetry history (ReplacingMergeTree) | **PA / EL** | at-least-once inserts; duplicates collapse by key on merge; analytics tolerate seconds of lag | ClickHouse restart: the sink retries with capped back-off and commits offsets only after an acknowledged insert; zero loss (`evidence/G13/clickhouse-restart`); history endpoints return an error meanwhile while the live dashboard keeps working |
| Alert delivery to the browser (Redis pub/sub → SSE) | **PA / EL** | best-effort live push; the alerts table is authoritative | a dropped stream reconnects with backoff and a fresh token; the open-alerts list is re-read from PostgreSQL, so a missed push is never a missed alert |
| Keycloak (identity) | **PC / EC** | tokens are verified offline against cached JWKS | Keycloak down: existing access tokens keep working until they expire (refresh then fails and the SPA keeps the user on the page, showing a transient error, instead of signing out); new logins are impossible. Outage **not** fault-injected end to end |
| Vault (device PKI) | **PC / EC** | dev mode in the demo | Vault down: no new device certificates can be issued; running gateway/API keep serving with certificates already loaded. Restart loses the in-memory CA in dev mode (documented) |
| pgvector similarity search | **PC / EC** | same PostgreSQL | unavailable ⇒ the Copilot's `search_similar_incidents` tool fails and the answer says there is insufficient evidence; the risk path is unaffected |
| LLM provider (optional) | **PA** | not a store | outage ⇒ the Copilot degrades to the deterministic provider's tool-only answer or a clear error; consequential writes still require human approval (tested: `TestLLMOutageDegradesGracefully`) |

## Why these choices
* **Money-adjacent and safety-adjacent records (approvals, audit, erasure, alert lifecycle) are CP**: a lost or duplicated approval is worse than a refused request.
* **Telemetry is AP**: a few seconds of staleness is acceptable, losing the ability to ingest is not; per-VIN sequence numbers make late and duplicate events harmless and the log makes state rebuildable.
* **Latency vs consistency in normal operation (the "E")**: the hot path reads Redis (single-digit-millisecond API answers measured in `evidence/live-deployment/live_checks.json`) and accepts that a vehicle's marker can be up to the 3 s snapshot age behind Kafka. Strongly consistent reads are used only where a human acts on the answer (alert detail and lifecycle read PostgreSQL directly).

## What is not verified
Multi-broker or multi-AZ behaviour, PostgreSQL failover, Keycloak or Vault outage injection, and network partitions with a proxy such as toxiproxy were not run; the table states design intent for those rows.
