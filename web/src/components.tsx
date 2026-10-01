import { type ReactNode } from "react";

export function Card({ title, value, sub, tone }: { title: string; value: ReactNode; sub?: ReactNode; tone?: "bad" | "warn" | "ok" }) {
  return (
    <div className={`card ${tone ?? ""}`}>
      <div className="muted small">{title}</div>
      <div className="big">{value}</div>
      {sub && <div className="muted small">{sub}</div>}
    </div>
  );
}

export function Sev({ s }: { s: string }) {
  return <span className={`pill ${s.toLowerCase()}`}>{s}</span>;
}

export function ErrorBox({ error }: { error: string }) {
  return error ? <div className="err box">⚠ {error}</div> : null;
}

/** A tiny dependency-free line chart. */
export function Spark({ values, color = "#2563eb", height = 70, min, max }: { values: number[]; color?: string; height?: number; min?: number; max?: number }) {
  if (values.length < 2) return <div className="muted small">not enough data</div>;
  const lo = min ?? Math.min(...values),
    hi = max ?? Math.max(...values);
  const w = 320;
  const pts = values
    .map((v, i) => `${(i / (values.length - 1)) * w},${height - ((v - lo) / (hi - lo || 1)) * (height - 4) - 2}`)
    .join(" ");
  return (
    <svg viewBox={`0 0 ${w} ${height}`} className="spark" preserveAspectRatio="none">
      <polyline fill="none" stroke={color} strokeWidth="2" points={pts} />
    </svg>
  );
}

/** Route polyline of an alert (evidence.route = [[lat, lon], ...]). */
export function RouteMap({ route, here }: { route: number[][]; here?: [number, number] }) {
  if (!route || route.length < 2) return null;
  const lats = route.map((p) => p[0]),
    lons = route.map((p) => p[1]);
  const [a, b, c, d] = [Math.min(...lats), Math.max(...lats), Math.min(...lons), Math.max(...lons)];
  const W = 320,
    H = 180,
    pad = 12;
  const sx = (lon: number) => pad + ((lon - c) / (d - c || 1e-6)) * (W - 2 * pad);
  const sy = (lat: number) => H - pad - ((lat - a) / (b - a || 1e-6)) * (H - 2 * pad);
  return (
    <svg viewBox={`0 0 ${W} ${H}`} className="routemap">
      <polyline fill="none" stroke="#2563eb" strokeWidth="3" points={route.map((p) => `${sx(p[1])},${sy(p[0])}`).join(" ")} />
      <circle cx={sx(route[0][1])} cy={sy(route[0][0])} r="6" fill="#dc2626" />
      <rect x={sx(route[route.length - 1][1]) - 6} y={sy(route[route.length - 1][0]) - 6} width="12" height="12" fill="#16a34a" />
      {here && <circle cx={sx(here[1])} cy={sy(here[0])} r="3" fill="#000" />}
    </svg>
  );
}
