import { useEffect, useRef, useState } from "react";
import { api, streamAlerts, type Me } from "../api";
import { usePoll, ago } from "../hooks";
import { Card, ErrorBox, Sev } from "../components";

interface Cell {
  lat: number;
  lon: number;
  count: number;
  avg_soc_pct: number;
  low_soc: number;
}

const CITIES: Record<string, [number, number]> = {
  "All cities": [0, 0],
  Chennai: [13.08, 80.27],
  Bengaluru: [12.97, 77.59],
  Surat: [21.17, 72.83],
};

function FleetMap({ cells, size }: { cells: Cell[]; size: number }) {
  const ref = useRef<HTMLCanvasElement>(null);
  useEffect(() => {
    const c = ref.current;
    if (!c) return;
    const ctx = c.getContext("2d")!;
    const W = (c.width = c.clientWidth * devicePixelRatio),
      H = (c.height = c.clientHeight * devicePixelRatio);
    ctx.fillStyle = "#0f172a";
    ctx.fillRect(0, 0, W, H);
    if (!cells.length) return;
    const lats = cells.map((x) => x.lat),
      lons = cells.map((x) => x.lon);
    const [a, b, l, r] = [Math.min(...lats) - size, Math.max(...lats) + size, Math.min(...lons) - size, Math.max(...lons) + size];
    const sx = (lon: number) => ((lon - l) / (r - l || 1)) * W,
      sy = (lat: number) => H - ((lat - a) / (b - a || 1)) * H;
    const maxCount = Math.max(...cells.map((x) => x.count));
    for (const cell of cells) {
      const risk = cell.low_soc / cell.count;
      const hue = 130 - Math.min(1, risk * 2) * 130; // green -> red
      const rad = (3 + 9 * Math.sqrt(cell.count / maxCount)) * devicePixelRatio;
      ctx.beginPath();
      ctx.fillStyle = `hsla(${hue}, 85%, 55%, 0.75)`;
      ctx.arc(sx(cell.lon), sy(cell.lat), rad, 0, Math.PI * 2);
      ctx.fill();
    }
  }, [cells, size]);
  return <canvas ref={ref} className="map" />;
}

export function Dashboard({ me }: { me: Me }) {
  const summary = usePoll(() => api("/v1/fleet/summary"), 5000);
  const map = usePoll(() => api("/v1/map/cells?cell=0.01"), 5000);
  const open = usePoll(() => api("/v1/alerts?status=open&limit=8"), 10000);
  const [city, setCity] = useState("All cities");
  const [live, setLive] = useState<any[]>([]);

  useEffect(() => {
    const ac = new AbortController();
    streamAlerts((a) => {
      setLive((x) => [a, ...x].slice(0, 20));
      open.reload();
    }, ac.signal);
    return () => ac.abort();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const s = summary.data;
  const crit = s ? Object.entries(s.alerts_unresolved as Record<string, number>).filter(([k]) => k.startsWith("CRITICAL")).reduce((n, [, v]) => n + v, 0) : 0;
  const warn = s ? Object.entries(s.alerts_unresolved as Record<string, number>).filter(([k]) => k.startsWith("WARNING")).reduce((n, [, v]) => n + v, 0) : 0;
  return (
    <div>
      <h2>Fleet overview</h2>
      <ErrorBox error={summary.error || map.error} />
      <div className="cards">
        <Card title="Vehicles" value={s?.vehicles ?? "–"} sub={`${s?.live.reporting ?? 0} reporting live`} />
        <Card title="Moving / charging" value={s ? `${s.live.moving} / ${s.live.charging}` : "–"} />
        <Card title="Average SoC" value={s ? `${s.live.avg_soc_pct}%` : "–"} sub={`${s?.live.low_soc ?? 0} below 20%`} tone={s && s.live.low_soc > 0 ? "warn" : undefined} />
        <Card title="Critical alerts" value={crit} tone={crit ? "bad" : "ok"} sub={`${warn} warnings`} />
        <Card title="Chargers" value={s?.chargers_visible ?? "–"} sub={`${s?.chargers_out_of_service_global ?? 0} out of service (network)`} />
      </div>
      <div className="two">
        <section>
          <h3>Live map {map.data && <span className="muted small">· {map.data.vehicles} vehicles in {map.data.cells.length} cells{me.permissions.includes("geo.precise") ? "" : " (coarse)"}</span>}</h3>
          <div className="row">
            {Object.keys(CITIES).map((c) => (
              <button key={c} className={c === city ? "primary" : ""} onClick={() => setCity(c)}>{c}</button>
            ))}
          </div>
          <FleetMap
            cells={(map.data?.cells ?? []).filter((c: Cell) => city === "All cities" || (Math.abs(c.lat - CITIES[city][0]) < 0.4 && Math.abs(c.lon - CITIES[city][1]) < 0.4))}
            size={map.data?.cell_degrees ?? 0.01}
          />
          <div className="muted small">circle size = vehicles in the cell · colour = share below 20% SoC (green → red)</div>
        </section>
        <section>
          <h3>Live alert feed</h3>
          {live.length === 0 && <p className="muted">Waiting for alerts… (streamed over SSE the moment the alert service persists them)</p>}
          {live.map((a, i) => (
            <div key={i} className="feed">
              <Sev s={a.severity} /> <b>{a.vin}</b> {a.rule.replace(/_/g, " ").toLowerCase()} · SoC {a.soc_pct}% · margin {a.margin_km?.toFixed?.(1)} km
              <span className="muted small"> · {ago(a.detected_at)}</span>
            </div>
          ))}
          <h3>Open alerts</h3>
          {open.data?.items.length === 0 && <p className="muted">None.</p>}
          {open.data?.items.map((a: any) => (
            <div key={a.id} className="feed">
              <Sev s={a.severity} /> <a href={`#alerts`}>{a.vin}</a> {a.rule.replace(/_/g, " ").toLowerCase()} <span className="muted small">· {ago(a.detected_at)}</span>
            </div>
          ))}
        </section>
      </div>
    </div>
  );
}
