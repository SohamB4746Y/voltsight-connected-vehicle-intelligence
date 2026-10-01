import { useEffect, useState } from "react";
import { api, streamAlerts, type Me, type StreamStatus } from "../api";
import { GeoMap, CITY_VIEW, type Selection } from "./GeoMap";
import { usePoll, ago } from "../hooks";
import { Card, ErrorBox, Sev } from "../components";

export function Dashboard({}: { me: Me }) {
  // dashboard statistics: one aggregated endpoint served from the 3 s per-tenant hot-state snapshot
  const summary = usePoll(() => api("/v1/fleet/summary"), 3000);
  const open = usePoll(() => api("/v1/alerts?status=open&limit=8"), 5000);
  const [city, setCity] = useState("All cities");
  const [live, setLive] = useState<any[]>([]);
  const [conn, setConn] = useState<StreamStatus>("connecting");
  const [bump, setBump] = useState(0); // a new alert refreshes the map straight away
  const [sel, setSel] = useState<Selection>({});

  useEffect(() => {
    const ac = new AbortController();
    streamAlerts((a) => {
      setLive((x) => [a, ...x].slice(0, 20));
      open.reload();
      summary.reload();
      setBump((n) => n + 1);
    }, ac.signal, setConn);
    return () => ac.abort();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const s = summary.data;
  const crit = s ? Object.entries(s.alerts_unresolved as Record<string, number>).filter(([k]) => k.startsWith("CRITICAL")).reduce((n, [, v]) => n + v, 0) : 0;
  const warn = s ? Object.entries(s.alerts_unresolved as Record<string, number>).filter(([k]) => k.startsWith("WARNING")).reduce((n, [, v]) => n + v, 0) : 0;
  return (
    <div>
      <h2>Fleet overview</h2>
      <ErrorBox error={summary.error} />
      <div className="cards">
        <Card title="Vehicles" value={s?.vehicles ?? "–"} sub={`${s?.live.reporting ?? 0} reporting live`} />
        <Card title="Moving / charging" value={s ? `${s.live.moving} / ${s.live.charging}` : "–"} />
        <Card title="Average SoC" value={s ? `${s.live.avg_soc_pct}%` : "–"} sub={`${s?.live.low_soc ?? 0} below 20%`} tone={s && s.live.low_soc > 0 ? "warn" : undefined} />
        <Card title="Critical alerts" value={crit} tone={crit ? "bad" : "ok"} sub={`${warn} warnings`} />
        <Card title="Chargers" value={s?.chargers_visible ?? "–"} sub={`${s?.chargers_out_of_service_global ?? 0} out of service (network)`} />
      </div>
      <div className="two">
        <section>
          <h3>Live map</h3>
          <div className="row">
            {Object.keys(CITY_VIEW).map((c) => (
              <button key={c} className={c === city ? "primary" : ""} onClick={() => setCity(c)}>{c}</button>
            ))}
          </div>
          <GeoMap city={city} refreshKey={bump} selection={sel} onSelect={setSel} live={conn} />
        </section>
        <section>
          <h3>Live alert feed</h3>
          {live.length === 0 && <p className="muted">Waiting for alerts… (streamed over SSE the moment the alert service persists them)</p>}
          {live.map((a, i) => (
            <div key={i} className="feed live-item" data-alert-id={a.id} data-detected={a.detected_at}>
              <Sev s={a.severity} /> <a href="#dashboard" onClick={() => setSel({ vin: a.vin, alertId: a.id })}><b>{a.vin}</b></a> {a.rule.replace(/_/g, " ").toLowerCase()} · SoC {a.soc_pct}% · margin {a.margin_km?.toFixed?.(1)} km
              <span className="muted small"> · {ago(a.detected_at)}</span>
            </div>
          ))}
          <h3>Open alerts</h3>
          {open.data?.items.length === 0 && <p className="muted">None.</p>}
          {open.data?.items.map((a: any) => (
            <div key={a.id} className="feed">
              <Sev s={a.severity} /> <a href="#dashboard" onClick={() => setSel({ vin: a.vin, alertId: a.id })}>{a.vin}</a> {a.rule.replace(/_/g, " ").toLowerCase()} <span className="muted small">· {ago(a.detected_at)}</span>
            </div>
          ))}
        </section>
      </div>
    </div>
  );
}
