import { useState } from "react";
import { api, type Me } from "../api";
import { usePoll, fmtTime } from "../hooks";
import { ErrorBox } from "../components";

export function Plans({ me }: { me: Me }) {
  const list = usePoll(() => api("/v1/plans"), 8000);
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  const [last, setLast] = useState<any>(null);
  const canWrite = me.permissions.includes("plans.write");
  const propose = async () => {
    setBusy(true);
    try {
      setLast(await api("/v1/plans:propose", { method: "POST", body: JSON.stringify({ target_soc_pct: 90, limit: 200 }) }));
      setErr("");
      list.reload();
    } catch (e: any) {
      setErr(e.message);
    } finally {
      setBusy(false);
    }
  };
  const decide = async (id: string, what: "approve" | "reject") => {
    try {
      await api(`/v1/plans/${id}/${what}`, { method: "POST" });
      list.reload();
    } catch (e: any) {
      setErr(e.message);
    }
  };
  return (
    <div>
      <h2>Charge plans</h2>
      <p className="muted">Minimum-cost schedules under time-of-use tariffs (dynamic programming over time slot × state of charge), compared with charging as soon as plugged in. Nothing is scheduled until a person approves it.</p>
      {canWrite && <button className="primary" disabled={busy} onClick={propose}>{busy ? "Optimising…" : "Propose plan for parked vehicles below target"}</button>}
      <ErrorBox error={err || list.error} />
      {last && <div className="box ok">New plan: {last.vehicles_planned} vehicles, {last.items} charging blocks, ₹{last.cost_estimate_inr} vs ₹{last.baseline_cost_estimate_inr} baseline ({last.savings_pct}% saved){last.infeasible_vins.length ? `, ${last.infeasible_vins.length} cannot reach the target in time` : ""}</div>}
      <table className="grid">
        <thead><tr><th>Created</th><th>Status</th><th>Blocks</th><th>Cost</th><th>Baseline</th><th>Saved</th>{canWrite && <th />}</tr></thead>
        <tbody>
          {list.data?.items.map((p: any) => (
            <tr key={p.id}>
              <td>{fmtTime(p.created_at)}</td><td>{p.status}</td><td>{p.items}</td>
              <td>₹{p.cost_estimate_inr}</td><td>₹{p.baseline_cost_estimate_inr}</td><td>{p.savings_pct}%</td>
              {canWrite && <td>{p.status === "proposed" && <><button onClick={() => decide(p.id, "approve")}>Approve</button> <button onClick={() => decide(p.id, "reject")}>Reject</button></>}</td>}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
