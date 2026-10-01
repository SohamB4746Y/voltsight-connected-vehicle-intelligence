# Demo (≤ 5 minutes, real system, real data)
Prerequisites: Docker, Go ≥ 1.27, Node 22. `make up-lowulimit` (or `make up`), `make seed`, `make build`, `make web`, `./bin/vsincidents` (loads the incident corpus).
Start everything: `tools/live/demo.sh 100000 3000` (gateway, worker, sink, alert service, API + console, and a 100,000-vehicle simulation in real time with a regional charger outage at +90 s). Open <http://localhost:8081>, sign in as `dispatcher@meridian.example` (password: `DEMO_USER_PASSWORD` in `.env`).

| Time | Show | What to say |
|---|---|---|
| 0:00 | Dashboard | 40,000 of the 100,000 vehicles belong to this tenant; moving/charging counts, average SoC and the live map are computed server-side from the real-time state in Redis (the browser never receives 100K points) |
| 0:45 | Chargers → take a depot charger out of service | the status event flows through Kafka into every worker's road-graph overlay; each affected vehicle's distance to the nearest available charger changes within milliseconds |
| 1:15 | Dashboard live alert feed / Alerts | alerts arrive over SSE the moment the alert service persists them; open one: SoC, estimated vs usable range (10% safety margin), distance to the nearest available charger, the A\* route, chargers available in the city |
| 2:00 | Acknowledge → History | status change and event commit in one transaction; the audit trail records it |
| 2:20 | Vehicles → a vehicle | live state, 30-minute SoC/speed history from ClickHouse, alerts |
| 2:50 | Copilot: "Why is the most critical vehicle at risk?" | it calls tools (list → evidence → chargers) and cites alert/vehicle ids; "Find similar past incidents" uses pgvector; the answer says *insufficient evidence* when a tool returns nothing |
| 3:40 | Sign in as `energy_manager@meridian.example` → Charge plans → Propose, then approve | dynamic programming over time-of-use tariffs; plan cost vs charge-on-plug-in; nothing is scheduled until a person approves |
| 4:20 | Sign in as `viewer@…` | same data, coordinates coarsened, no route detail; the API returns 403 for writes |
| 4:40 | `bin/vsseal verify` | audit hash chain verifies |
Reset: `make nuke && make up-lowulimit && make seed`.
