# ADR-003 Consistency (CAP/PACELC) and delivery semantics
| Data | Choice | Reason |
|---|---|---|
| tenancy, subscriptions, plans, approvals, audit, erasure | CP (PostgreSQL, synchronous commit) | never lose or duplicate an approval/audit row |
| raw telemetry, latest state | AP, eventual | latency/availability over linearisability; per-VIN seq guards give monotonicity; state rebuildable by replay |
| alerts | at-least-once delivery + unique key | duplicates collapse (PostgreSQL unique `(vin, rule, window_start)`) |
**Decision:** at-least-once with idempotent sinks, not Kafka exactly-once transactions (cost/latency on the hot path). **Evidence:** replay rebuilds identical Redis state; mid-stream kill converges to the uninterrupted run; ClickHouse restart loses nothing; a defect found by this method (consumer groups starting at the log end skipping early events while reporting lag 0) is documented in `evidence/G4/status.md`.
