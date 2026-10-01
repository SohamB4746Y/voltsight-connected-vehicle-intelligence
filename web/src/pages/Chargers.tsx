import { useState } from "react";
import { api, type Me } from "../api";
import { usePoll } from "../hooks";
import { ErrorBox } from "../components";

export function Chargers({ me }: { me: Me }) {
  const [after, setAfter] = useState("");
  const [pages, setPages] = useState<any[]>([]);
  const [err, setErr] = useState("");
  const list = usePoll(() => api(`/v1/chargers?limit=100${after ? `&after=${after}` : ""}`), after ? null : 5000, [after]);
  const items = [...pages.flatMap((p) => p.items), ...(list.data?.items ?? [])];
  const canWrite = me.permissions.includes("chargers.write");
  const toggle = async (c: any) => {
    try {
      await api(`/v1/chargers/${c.id}/status`, { method: "POST", body: JSON.stringify({ status: c.status === "OUT_OF_SERVICE" ? "AVAILABLE" : "OUT_OF_SERVICE" }) });
      setErr("");
      setTimeout(() => list.reload(), 300);
    } catch (e: any) {
      setErr(e.message);
    }
  };
  return (
    <div>
      <h2>Chargers</h2>
      <p className="muted">Taking a depot charger out of service changes every vehicle's range-risk decision within seconds (status event → road-graph overlay → alerts).</p>
      <ErrorBox error={list.error || err} />
      <table className="grid">
        <thead><tr><th>Name</th><th>Network</th><th>Power</th><th>Status</th>{canWrite && <th />}</tr></thead>
        <tbody>
          {items.map((c: any) => (
            <tr key={c.id}>
              <td>{c.name}</td><td>{c.public ? "public" : "depot"}</td><td>{c.power_kw} kW</td>
              <td><span className={`pill ${c.status === "OUT_OF_SERVICE" ? "critical" : "info"}`}>{c.status.toLowerCase().replace(/_/g, " ")}</span></td>
              {canWrite && <td>{!c.public && <button onClick={() => toggle(c)}>{c.status === "OUT_OF_SERVICE" ? "Restore" : "Take out of service"}</button>}</td>}
            </tr>
          ))}
        </tbody>
      </table>
      {list.data?.next && <button onClick={() => { setPages((p) => [...p, list.data]); setAfter(list.data.next); }}>Load more</button>}
    </div>
  );
}
