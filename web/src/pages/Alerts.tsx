import { useState } from "react";
import { api, type Me } from "../api";
import { usePoll, ago, fmtTime } from "../hooks";
import { ErrorBox, RouteMap, Sev } from "../components";

function Detail({ id, me, onChanged, onClose }: { id: string; me: Me; onChanged: () => void; onClose: () => void }) {
  const d = usePoll(() => api(`/v1/alerts/${id}`), null, [id]);
  const [err, setErr] = useState("");
  const a = d.data?.alert;
  const ev = a?.evidence ?? {};
  const act = async (what: "ack" | "resolve") => {
    try {
      await api(`/v1/alerts/${id}/${what}`, { method: "POST" });
      d.reload();
      onChanged();
    } catch (e: any) {
      setErr(e.message);
    }
  };
  return (
    <aside className="drawer">
      <button className="close" onClick={onClose}>✕</button>
      <ErrorBox error={d.error || err} />
      {a && (
        <>
          <h3><Sev s={a.severity} /> {a.rule.replace(/_/g, " ")}</h3>
          <p><b>{a.vin}</b> · {a.status} · detected {fmtTime(a.detected_at)}</p>
          <table>
            <tbody>
              <tr><td>State of charge</td><td>{ev.soc_pct}%</td></tr>
              <tr><td>Estimated range</td><td>{ev.estimated_range_km} km ({ev.estimator})</td></tr>
              <tr><td>Usable after {Math.round((ev.safety_margin ?? 0) * 100)}% safety margin</td><td>{ev.usable_range_km} km</td></tr>
              <tr><td>Nearest available charger</td><td>{ev.reachable_charger ? `${ev.distance_to_charger_km} km (route ${ev.route_km} km)` : <b>none reachable</b>}</td></tr>
              <tr><td>Chargers available in city</td><td>{ev.city_chargers_available} / {ev.city_chargers_total}</td></tr>
            </tbody>
          </table>
          {ev.route && <RouteMap route={ev.route} here={[ev.lat, ev.lon]} />}
          {ev.route && <div className="muted small">● vehicle (red) → charger (green square)</div>}
          {!ev.route && <p className="muted small">Route detail is hidden for your role.</p>}
          {me.permissions.includes("alerts.write") && (
            <div className="row">
              <button disabled={a.status !== "open"} onClick={() => act("ack")}>Acknowledge</button>
              <button disabled={a.status === "resolved"} onClick={() => act("resolve")}>Resolve</button>
            </div>
          )}
          <h4>History</h4>
          {d.data.events.length === 0 && <p className="muted small">No actions yet.</p>}
          {d.data.events.map((e: any, i: number) => <div key={i} className="small">{e.action} · {fmtTime(e.at)}</div>)}
        </>
      )}
    </aside>
  );
}

export function Alerts({ me }: { me: Me }) {
  const [status, setStatus] = useState("open");
  const [severity, setSeverity] = useState("");
  const [after, setAfter] = useState("");
  const [pages, setPages] = useState<any[]>([]);
  const [sel, setSel] = useState("");
  const list = usePoll(
    () => api(`/v1/alerts?limit=50&status=${status}&severity=${severity}${after ? `&after=${after}` : ""}`),
    after ? null : 5000,
    [status, severity, after],
  );
  const items = [...pages.flatMap((p) => p.items), ...(list.data?.items ?? [])];
  const reset = () => { setPages([]); setAfter(""); };
  return (
    <div className="split">
      <div>
        <h2>Alerts</h2>
        <div className="row">
          <select value={status} onChange={(e) => { reset(); setStatus(e.target.value); }}>
            <option value="open">open</option><option value="acknowledged">acknowledged</option><option value="resolved">resolved</option><option value="">all</option>
          </select>
          <select value={severity} onChange={(e) => { reset(); setSeverity(e.target.value); }}>
            <option value="">any severity</option><option>CRITICAL</option><option>WARNING</option><option>INFO</option>
          </select>
        </div>
        <ErrorBox error={list.error} />
        <table className="grid">
          <thead><tr><th>Severity</th><th>Vehicle</th><th>Rule</th><th>Status</th><th>Detected</th></tr></thead>
          <tbody>
            {items.map((a: any) => (
              <tr key={a.id} onClick={() => setSel(a.id)} className={sel === a.id ? "sel" : ""}>
                <td><Sev s={a.severity} /></td><td>{a.vin}</td><td>{a.rule.replace(/_/g, " ").toLowerCase()}</td><td>{a.status}</td><td>{ago(a.detected_at)}</td>
              </tr>
            ))}
          </tbody>
        </table>
        {items.length === 0 && !list.loading && <p className="muted">No alerts match.</p>}
        {list.data?.next && <button onClick={() => { setPages((p) => [...p, list.data]); setAfter(list.data.next); }}>Load more</button>}
      </div>
      {sel && <Detail id={sel} me={me} onChanged={() => list.reload()} onClose={() => setSel("")} />}
    </div>
  );
}
