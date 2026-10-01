import { api } from "../api";
import { usePoll } from "../hooks";
import { Card, ErrorBox } from "../components";

export function Reports() {
  const cost = usePoll(() => api("/v1/reports/cost"), 15000);
  const soh = usePoll(() => api("/v1/reports/soh"), 15000);
  const energy = usePoll(() => api("/v1/reports/energy"), 30000);
  return (
    <div>
      <h2>Reports</h2>
      <ErrorBox error={cost.error || soh.error} />
      <h3>Charging cost (approved plans)</h3>
      <div className="cards">
        <Card title="Planned cost" value={`₹${(cost.data?.total_cost_inr ?? 0).toFixed(0)}`} />
        <Card title="Baseline (charge on plug-in)" value={`₹${(cost.data?.total_baseline_inr ?? 0).toFixed(0)}`} />
        <Card title="Saved" value={`₹${(cost.data?.total_saved_inr ?? 0).toFixed(0)}`} tone="ok" />
      </div>
      <h3>Fleet energy by day (trip rollup)</h3>
      {energy.data?.days.length ? (
        <table className="grid">
          <thead><tr><th>Day</th><th>Trips</th><th>Distance</th><th>Energy</th><th>kWh / 100 km</th></tr></thead>
          <tbody>{energy.data.days.map((d: any) => <tr key={d.day}><td>{d.day.slice(0, 10)}</td><td>{d.trips}</td><td>{d.km.toFixed(0)} km</td><td>{d.kwh.toFixed(0)} kWh</td><td>{d.km > 0 ? ((d.kwh / d.km) * 100).toFixed(1) : "–"}</td></tr>)}</tbody>
        </table>
      ) : <p className="muted">No trips analysed yet (the batch job fills this).</p>}
      <h3>Battery state of health</h3>
      <div className="cards">
        <Card title="Vehicles estimated" value={soh.data?.vehicles_estimated ?? 0} />
        <Card title="Mean SoH" value={`${soh.data?.mean_soh_pct ?? 0}%`} />
        {soh.data && Object.entries(soh.data.distribution).map(([k, v]) => <Card key={k} title={`SoH ${k}%`} value={v as number} />)}
      </div>
      {soh.data?.lowest.length > 0 && (
        <table className="grid">
          <thead><tr><th>Lowest SoH</th><th>Capacity</th><th>Nominal</th><th>SoH</th><th>Method</th></tr></thead>
          <tbody>{soh.data.lowest.map((v: any) => <tr key={v.vin}><td>{v.vin}</td><td>{v.capacity_kwh} kWh</td><td>{v.nominal_kwh} kWh</td><td>{v.soh_pct}%</td><td>{v.method}</td></tr>)}</tbody>
        </table>
      )}
    </div>
  );
}
