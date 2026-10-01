"""Renders evidence/G14/status.md and evidence/G6/live_alert_latency/latency.json from the raw run outputs."""
import json, os, statistics
def pct(v, p): v = sorted(v); return v[int(p * (len(v) - 1))]
out = ["# Gate G14 status (load, capacity) — PARTIAL", "",
       "All runs: 4-vCPU / 15.7 GB Linux sandbox, **every component on one host** (simulator, gateway, worker, sink, alert service, Kafka, ClickHouse, Redis, PostgreSQL), so the load generator competes with the system under test. These are honest single-host measurements, not a capacity claim.", ""]
# burst
b = "evidence/G14/burst_3x"
if os.path.exists(f"{b}/sim.json"):
    s = json.load(open(f"{b}/sim.json")); g = s["gateway"]
    rec = json.load(open(f"{b}/recon.json")) if os.path.exists(f"{b}/recon.json") else {}
    out += ["## 3× telemetry burst (40 s at 3 samples/vehicle/s inside a 100 s real-time run, 100,000 vehicles)",
            f"- events emitted {s['events_emitted']:,}; accepted by the gateway {g['accepted']:,}; to DLQ {g['rejected']:,}; sender gave up on {g['failed']:,}; 429 retries {g['retried_429']:,}; 503 retries {g['retried_503']:,}; achieved {s['events_per_wall_second']:,.0f} events/s over {s['wall_seconds']:.0f} s wall",
            f"- reconciliation: balanced = **{rec.get('balanced')}**; Kafka records {rec.get('kafka_telemetry_records'):,}; ClickHouse distinct {rec.get('clickhouse_rows_after_dedup'):,} = worker distinct {rec.get('worker_distinct_events'):,}; lags {rec.get('rt_group_lag')}/{rec.get('sink_group_lag')}" if rec else "- reconciliation file missing", ""]
else:
    out += ["## 3× burst: **NOT MEASURED** (run did not complete)", ""]
# latency
l = "evidence/G6/live_alert_latency/alert_latency.ndjson"
if os.path.exists(l):
    rows = [json.loads(x) for x in open(l) if x.strip()]
    if rows:
        rp = [r["recv_to_persist_ms"] / 1000 for r in rows]; rd = [r["recv_to_decision_ms"] / 1000 for r in rows]
        res = {"alerts": len(rows), "gateway_receive_to_decision_s": {"p50": pct(rd, .5), "p95": pct(rd, .95), "p99": pct(rd, .99)},
               "gateway_receive_to_persisted_s": {"p50": pct(rp, .5), "p95": pct(rp, .95), "p99": pct(rp, .99), "max": max(rp)}}
        json.dump(res, open("evidence/G6/live_alert_latency/latency.json", "w"), indent=2)
        out += ["## Alert latency (G6.10 / N4), live pipeline at ~100K events/s, stress scenario",
                f"- {len(rows)} alerts; gateway-receive → alert **persisted in PostgreSQL**: p50 {pct(rp,.5):.2f} s, p95 {pct(rp,.95):.2f} s, p99 {pct(rp,.99):.2f} s, max {max(rp):.2f} s (target p99 < 5 s: **{'met' if pct(rp,.99) < 5 else 'NOT met'}** on this single oversubscribed host)",
                f"- gateway-receive → decision in the worker: p50 {pct(rd,.5):.2f} s, p95 {pct(rd,.95):.2f} s, p99 {pct(rd,.99):.2f} s", ""]
    else:
        out += ["## Alert latency: **NOT MEASURED** (no alerts were produced in the run)", ""]
else:
    out += ["## Alert latency: **NOT MEASURED** (run did not complete)", ""]
# stores
if os.path.exists("evidence/G14/pg_vs_clickhouse.txt"):
    out += ["## PostgreSQL vs ClickHouse insert (SC5)", "```", open("evidence/G14/pg_vs_clickhouse.txt").read().strip(), "```", ""]
if os.path.exists("evidence/G14/ch_bench.json"):
    c = json.load(open("evidence/G14/ch_bench.json"))
    out += ["## ClickHouse history benchmark (SYNTHETIC rows generated inside ClickHouse, production schema/ordering/codecs)",
            f"- {c['synthetic_rows']:,} rows ({c['vehicles']:,} vehicles × {c['samples_per_vehicle']} samples), {c['bytes_per_row_on_disk']} B/row on disk, compression {c['compression_ratio']}×"]
    out += [f"- `{q['name']}`: {q['wall_ms']} ms wall, {q['rows_read']:,} rows read" for q in c["queries"]]
    out += ["- smooth synthetic signals compress better than real telemetry; this is **not** a billion-row run (disk budget) and not real data", ""]
out += ["## Not measured", "- ≥ 100K events/s *sustained* beyond the 60 s run in `evidence/G4`; 5-minute 3× burst; soak (heap/RSS/lag trend); scale-out 1→2→4→8 workers; adding Kafka brokers; per-hop ceilings under isolation; billion-row batch scan; CPU/memory plots."]
open("evidence/G14/status.md", "w").write("\n".join(out) + "\n"); print("\n".join(out))
