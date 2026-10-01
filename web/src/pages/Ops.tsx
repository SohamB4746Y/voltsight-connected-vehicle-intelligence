import { api } from "../api";
import { usePoll } from "../hooks";
import { Card, ErrorBox } from "../components";

export function Ops() {
  const p = usePoll(() => api("/v1/ops/pipeline"), 5000);
  return (
    <div>
      <h2>Platform health</h2>
      <ErrorBox error={p.error} />
      <div className="cards">
        <Card title="Live vehicle states (Redis keys)" value={p.data?.redis_keys ?? "–"} />
        <Card title="Telemetry rows, last minute" value={p.data?.telemetry_rows_last_minute ?? "–"} />
        <Card title="Chargers out of service" value={p.data?.chargers_out_of_service ?? "–"} sub={`${p.data?.chargers_with_status ?? 0} with a reported status`} />
      </div>
    </div>
  );
}
