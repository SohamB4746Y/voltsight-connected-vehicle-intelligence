import { api } from "../api";
import { usePoll, fmtTime } from "../hooks";
import { ErrorBox } from "../components";

export function Audit() {
  const list = usePoll(() => api("/v1/audit?limit=100"), 8000);
  return (
    <div>
      <h2>Audit trail</h2>
      <p className="muted">Every read and write of tenant data, and every Copilot action, is recorded here.</p>
      <ErrorBox error={list.error} />
      <table className="grid">
        <thead><tr><th>Time</th><th>Actor</th><th>Action</th><th>Resource</th><th>Status</th></tr></thead>
        <tbody>
          {list.data?.items.map((a: any) => (
            <tr key={a.id}><td>{fmtTime(a.ts)}</td><td>{a.actor_type} {a.actor.slice(0, 8)}</td><td>{a.action}</td><td>{a.resource_type} {a.resource_id ?? ""}</td><td>{a.details?.status}</td></tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
