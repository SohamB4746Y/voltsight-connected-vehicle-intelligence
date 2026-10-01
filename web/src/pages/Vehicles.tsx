import { useState } from "react";
import { api, type Me } from "../api";
import { usePoll, ago, fmtTime } from "../hooks";
import { ErrorBox, Sev, Spark } from "../components";

function Detail({ vin, onClose }: { vin: string; onClose: () => void }) {
  const d = usePoll(() => api(`/v1/vehicles/${vin}`), 5000, [vin]);
  const t = usePoll(() => api(`/v1/vehicles/${vin}/telemetry?step=10`), 10000, [vin]);
  const v = d.data?.vehicle;
  const pts = t.data?.points ?? [];
  return (
    <aside className="drawer">
      <button className="close" onClick={onClose}>
        ✕
      </button>
      <h3>{vin}</h3>
      <ErrorBox error={d.error} />
      {v && (
        <>
          <p>
            {v.oem} {v.model} · {v.fleet} · since {v.commissioned_at}
          </p>
          {v.state ? (
            <table>
              <tbody>
                <tr><td>State of charge</td><td><b>{v.state.soc_pct.toFixed(1)}%</b></td></tr>
                <tr><td>Speed</td><td>{v.state.speed_kmh.toFixed(0)} km/h</td></tr>
                <tr><td>Charging</td><td>{v.state.charge_state}</td></tr>
                <tr><td>Pack temperature</td><td>{v.state.pack_temp_c.toFixed(1)} °C</td></tr>
                <tr><td>Odometer</td><td>{v.state.odo_km.toFixed(0)} km</td></tr>
                <tr><td>Position</td><td>{v.state.lat.toFixed(4)}, {v.state.lon.toFixed(4)}</td></tr>
                <tr><td>Last seen</td><td>{ago(v.state.last_seen)}</td></tr>
                {v.state.dtc?.length ? <tr><td>Fault codes</td><td>{v.state.dtc.join(", ")}</td></tr> : null}
              </tbody>
            </table>
          ) : (
            <p className="muted">No live telemetry for this vehicle.</p>
          )}
        </>
      )}
      <h4>Last 30 minutes (history store)</h4>
      {t.error && <p className="muted small">{t.error}</p>}
      <div className="muted small">SoC %</div>
      <Spark values={pts.map((p: any) => p.soc_pct)} color="#16a34a" min={0} max={100} />
      <div className="muted small">Speed km/h</div>
      <Spark values={pts.map((p: any) => p.speed_kmh)} color="#2563eb" />
      <h4>Alerts</h4>
      {d.data?.alerts.length === 0 && <p className="muted">No alerts.</p>}
      {d.data?.alerts.map((a: any) => (
        <div key={a.id} className="feed">
          <Sev s={a.severity} /> {a.rule.replace(/_/g, " ").toLowerCase()} · {a.status} <span className="muted small">{fmtTime(a.detected_at)}</span>
        </div>
      ))}
    </aside>
  );
}

export function Vehicles({ me }: { me: Me }) {
  const [q, setQ] = useState("");
  const [atRisk, setAtRisk] = useState(false);
  const [pages, setPages] = useState<any[]>([]);
  const [after, setAfter] = useState("");
  const [sel, setSel] = useState("");
  const list = usePoll(
    async () => {
      const r = await api(`/v1/vehicles?limit=50&q=${encodeURIComponent(q)}&at_risk=${atRisk}${after ? `&after=${after}` : ""}`);
      return r;
    },
    after ? null : 8000,
    [q, atRisk, after],
  );
  const items = [...pages.flatMap((p) => p.items), ...(list.data?.items ?? [])];
  const next = list.data?.next;
  void me;
  return (
    <div className="split">
      <div>
        <h2>Vehicles</h2>
        <div className="row">
          <input placeholder="VIN prefix" value={q} onChange={(e) => { setPages([]); setAfter(""); setQ(e.target.value.toUpperCase()); }} />
          <label>
            <input type="checkbox" checked={atRisk} onChange={(e) => { setPages([]); setAfter(""); setAtRisk(e.target.checked); }} /> only vehicles with open alerts
          </label>
        </div>
        <ErrorBox error={list.error} />
        <table className="grid">
          <thead>
            <tr><th>VIN</th><th>Model</th><th>Fleet</th><th>SoC</th><th>State</th><th>Alerts</th></tr>
          </thead>
          <tbody>
            {items.map((v: any) => (
              <tr key={v.vin} onClick={() => setSel(v.vin)} className={sel === v.vin ? "sel" : ""}>
                <td>{v.vin}</td>
                <td>{v.model}</td>
                <td>{v.fleet}</td>
                <td>{v.state ? `${v.state.soc_pct.toFixed(0)}%` : "–"}</td>
                <td>{v.state ? (v.state.charge_state !== "NONE" ? "⚡ " + v.state.charge_state.toLowerCase() : v.state.speed_kmh > 3 ? "driving" : "parked") : <span className="muted">no data</span>}</td>
                <td>{v.open_alerts > 0 ? <span className="pill critical">{v.open_alerts}</span> : ""}</td>
              </tr>
            ))}
          </tbody>
        </table>
        {next && (
          <button onClick={() => { setPages((p) => [...p, list.data]); setAfter(next); }}>Load more</button>
        )}
      </div>
      {sel && <Detail vin={sel} onClose={() => setSel("")} />}
    </div>
  );
}
