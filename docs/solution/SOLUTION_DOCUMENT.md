# VoltSight — Solution Document
*The hackathon's Solution Document template was not available to the author, so this document follows the structure the brief asks for. It is a summary; every claim links to the evidence that backs it. Honest gaps are in [`../requirements-status.md`](../requirements-status.md).*

## 1. Problem and users
EV fleets strand vehicles because range estimates ignore battery ageing, load, route and charger outages; they overpay for charging (peak tariffs, depot congestion); and they cannot see degradation. VoltSight answers, per vehicle and within seconds: **can it still reach an available charger?** — and plans charging at the lowest time-of-use cost. Users: dispatchers (real time), energy managers (plans, SoH, cost), tenant administrators (access, audit, privacy), platform engineers (health). Problem space chosen after scoring eight candidates (EV charging & battery health scored highest because every algorithm family of the brief has a genuine job in it and the simulator provides checkable ground truth).

## 2. Solution overview
Simulator (100,000 vehicles) → mTLS gateway → Kafka → stream workers (dedup, state, **graph-based range risk**) → alerts → PostgreSQL/Redis → API + web console; history in ClickHouse feeding batch analytics (trips, battery SoH), a consumption model, a charge-plan optimiser and an audited Copilot. Architecture, stores and flows: [`../architecture.md`](../architecture.md).

## 3. Key design decisions (ADRs)
Streaming engine (001), polyglot storage (002), consistency and delivery semantics (003), device transport (004), agent guardrails (005), tenant isolation (006), model serving (007): [`../adr/`](../adr/).

## 4. Algorithms and data engineering
Complexity analysis with measurements: [`../algorithms.md`](../algorithms.md). 3NF schema with documented denormalisation: [`../3nf.md`](../3nf.md), ER diagram [`../er`](../er/README.md). SQL tuning with real plans: [`../sql-optimisation.md`](../sql-optimisation.md). Telemetry volume: [`../capacity.md`](../capacity.md).

## 5. Security and privacy
STRIDE model, OWASP mapping, compliance mapping (GDPR/DPDP/UNECE R155-156 at awareness level), scan results: [`../threat-model.md`](../threat-model.md), [`../owasp-map.md`](../owasp-map.md), [`../compliance.md`](../compliance.md), [`../../evidence/G12/status.md`](../../evidence/G12/status.md).

## 6. Quality evidence
Real-time path 98.3K ev/s for 60 s with reconciliation (G4); detection quality vs simulator truth (G6); API tests (G9); Copilot scenarios (G10); ML vs baselines (G8); chaos (G13); load/capacity (G14). Index: [`../../evidence`](../../evidence/).

## 7. Deployment
Compose (verified), Helm (linted, 21 resources schema-valid), Terraform AWS/GCP (validated, never applied): [`../deployment.md`](../deployment.md). Demo: [`../demo.md`](../demo.md).

## 8. Limitations (summary)
Targets not reached or not measured: ≥100K ev/s *sustained*, 3× burst for 5 minutes, soak, scale-out, alert latency percentiles, billion-row benchmark; not built: Parquet cold archive, MQTT, Go GBM evaluator, Pact/BDD suites; not run: DAST, image build/scan, Kubernetes install, cloud apply; the simulator is the only data source, so absolute accuracy figures do not transfer to real fleets.
