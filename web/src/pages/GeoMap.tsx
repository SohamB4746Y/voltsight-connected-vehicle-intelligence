import { useEffect, useRef, useState } from "react";
import * as maplibregl from "maplibre-gl";
import "maplibre-gl/dist/maplibre-gl.css";
// Serve MapLibre's web worker as a same-origin file built by Vite (works under `worker-src \'self\'`), instead of the
// inline blob worker that failed to start in the production bundle ("Worker failed to load").
import workerUrl from "maplibre-gl/dist/maplibre-gl-worker.mjs?worker&url";

maplibregl.setWorkerUrl(workerUrl);
import { api } from "../api";
import { usePoll, fmtTime, ago } from "../hooks";
import { Sev } from "../components";

// Real geographic map. Basemap: OpenFreeMap "liberty" (OpenStreetMap data, no API key). Everything drawn on top comes
// from the backend: vehicle positions/risk from GET /v1/map/vehicles, charger sites/status from GET /v1/map/chargers,
// the route of a risky vehicle from that alert's evidence (the A* road route the risk engine computed).
const STYLE = "https://tiles.openfreemap.org/styles/liberty";
export const CITY_VIEW: Record<string, { center: [number, number]; zoom: number }> = {
  "All cities": { center: [78.0, 17.5], zoom: 4.6 },
  Chennai: { center: [80.2707, 13.0827], zoom: 10.5 },
  Bengaluru: { center: [77.5946, 12.9716], zoom: 10.5 },
  Surat: { center: [72.8311, 21.1702], zoom: 10.5 },
};
const BLANK_STYLE = {
  version: 8,
  name: "basemap-unavailable",
  sources: {},
  glyphs: "https://tiles.openfreemap.org/fonts/{fontstack}/{range}.pbf",
  layers: [{ id: "background", type: "background", paint: { "background-color": "#e5e7eb" } }],
} as maplibregl.StyleSpecification;
const RISK_COLOR = ["match", ["get", "risk"], "critical", "#dc2626", "low", "#f59e0b", "#16a34a"] as any;
const empty = { type: "FeatureCollection", features: [] } as any;
const na = (v: unknown, unit = "") => (v === undefined || v === null || v === "" ? "not available" : `${typeof v === "number" ? Math.round(v * 10) / 10 : v}${unit}`);

const vehiclesGeo = (vs: any[]) => ({
  type: "FeatureCollection",
  features: vs.map((v) => ({ type: "Feature", geometry: { type: "Point", coordinates: [v.lon, v.lat] }, properties: v })),
});
const chargersGeo = (cs: any[]) => ({
  type: "FeatureCollection",
  features: cs.map((c) => ({ type: "Feature", geometry: { type: "Point", coordinates: [c.lon, c.lat] }, properties: c })),
});

export interface Selection {
  vin?: string;
  alertId?: string;
}

export function GeoMap({ city, refreshKey, selection, onSelect, live }: { city: string; refreshKey: number; selection: Selection; onSelect: (s: Selection) => void; live: string }) {
  const box = useRef<HTMLDivElement>(null);
  const mapRef = useRef<maplibregl.Map | null>(null);
  const [ready, setReady] = useState(false);
  const [charger, setCharger] = useState<any | null>(null);
  const [basemap, setBasemap] = useState(true);
  const cityQ = city === "All cities" ? "" : `?city=${encodeURIComponent(city)}`;

  // one aggregated endpoint, not one request per vehicle
  const vehicles = usePoll(() => api(`/v1/map/vehicles${cityQ}`), 4000, [city, refreshKey]);
  const chargers = usePoll(() => api(`/v1/map/chargers${cityQ}`), 15000, [city]);
  const vin = selection.vin;
  const detail = usePoll(() => (vin ? api(`/v1/vehicles/${vin}`) : Promise.resolve(null)), vin ? 5000 : null, [vin]);
  const alertId = selection.alertId;
  const alert = usePoll(() => (alertId ? api(`/v1/alerts/${alertId}`) : Promise.resolve(null)), null, [alertId]);

  useEffect(() => {
    if (!box.current) return;
    const map = new maplibregl.Map({ container: box.current, style: STYLE, center: CITY_VIEW["All cities"].center, zoom: CITY_VIEW["All cities"].zoom, attributionControl: false });
    map.addControl(
      new maplibregl.AttributionControl({
        compact: false,
        customAttribution: '© <a href="https://www.openstreetmap.org/copyright" target="_blank" rel="noopener">OpenStreetMap contributors</a>',
      }),
      "bottom-right",
    );
    map.addControl(new maplibregl.NavigationControl({ showCompass: false }), "top-right");
    const addLayers = () => {
      if (map.getSource("vehicles")) return;
      map.addSource("chargers", { type: "geojson", data: empty });
      map.addSource("route", { type: "geojson", data: empty });
      map.addSource("vehicles", {
        type: "geojson", data: empty, cluster: true, clusterRadius: 42, clusterMaxZoom: 12,
        clusterProperties: {
          crit: ["+", ["case", ["==", ["get", "risk"], "critical"], 1, 0]],
          low: ["+", ["case", ["==", ["get", "risk"], "low"], 1, 0]],
        },
      });
      map.addLayer({ id: "route-casing", type: "line", source: "route", paint: { "line-color": "#ffffff", "line-width": 8 } });
      map.addLayer({ id: "route", type: "line", source: "route", paint: { "line-color": "#2563eb", "line-width": 4 } });
      map.addLayer({
        id: "chargers", type: "circle", source: "chargers",
        paint: {
          "circle-radius": ["interpolate", ["linear"], ["zoom"], 4, 2, 10, 5, 14, 8],
          "circle-color": ["match", ["get", "status"], "AVAILABLE", "#2563eb", "OCCUPIED", "#f97316", "OUT_OF_SERVICE", "#b91c1c", "#6b7280"],
          "circle-stroke-color": "#0f172a", "circle-stroke-width": 1.5,
        },
      });
      map.addLayer({
        id: "veh-clusters", type: "circle", source: "vehicles", filter: ["has", "point_count"],
        paint: {
          "circle-color": ["case", [">", ["get", "crit"], 0], "#dc2626", [">", ["get", "low"], 0], "#f59e0b", "#16a34a"],
          "circle-radius": ["step", ["get", "point_count"], 14, 50, 18, 200, 24], "circle-stroke-color": "#fff", "circle-stroke-width": 2, "circle-opacity": 0.9,
        },
      });
      map.addLayer({
        id: "veh-cluster-count", type: "symbol", source: "vehicles", filter: ["has", "point_count"],
        layout: { "text-field": ["get", "point_count_abbreviated"], "text-font": ["Noto Sans Regular"], "text-size": 12 }, paint: { "text-color": "#fff" },
      });
      map.addLayer({
        id: "veh-points", type: "circle", source: "vehicles", filter: ["!", ["has", "point_count"]],
        paint: {
          "circle-color": RISK_COLOR, "circle-radius": ["match", ["get", "risk"], "critical", 9, "low", 7, 5],
          "circle-stroke-color": "#fff", "circle-stroke-width": 1.5,
        },
      });
      map.addLayer({ id: "veh-selected", type: "circle", source: "vehicles", filter: ["==", ["get", "vin"], ""], paint: { "circle-radius": 15, "circle-color": "rgba(0,0,0,0)", "circle-stroke-color": "#2563eb", "circle-stroke-width": 3 } });

      setReady(true);
    };
    // layer-delegated handlers work for layers added later, so they are registered once, outside addLayers
    map.on("click", "veh-clusters", async (e: maplibregl.MapLayerMouseEvent) => {
      const f = e.features?.[0];
      if (!f) return;
      const z = await (map.getSource("vehicles") as maplibregl.GeoJSONSource).getClusterExpansionZoom(f.properties!.cluster_id);
      map.easeTo({ center: (f.geometry as any).coordinates, zoom: z + 0.5 });
    });
    map.on("click", "veh-points", (e: maplibregl.MapLayerMouseEvent) => {
      const p = e.features?.[0]?.properties as any;
      if (p) {
        setCharger(null);
        onSelect({ vin: p.vin, alertId: p.alert_id || undefined });
      }
    });
    map.on("click", "chargers", (e: maplibregl.MapLayerMouseEvent) => {
      const p = e.features?.[0]?.properties as any;
      if (p) setCharger(p);
    });
    for (const l of ["veh-clusters", "veh-points", "chargers"]) {
      map.on("mouseenter", l, () => (map.getCanvas().style.cursor = "pointer"));
      map.on("mouseleave", l, () => (map.getCanvas().style.cursor = ""));
    }

    // The basemap is a third-party service: if its style cannot be fetched (blocked network, outage) the markers must
    // still render, so fall back to a plain background style. Backend data never depends on the tile host.
    let styleOk = false, fellBack = false;
    const fallback = () => {
      if (styleOk || fellBack) return;
      fellBack = true;
      setBasemap(false);
      map.setStyle(BLANK_STYLE);
    };
    map.on("style.load", () => {
      styleOk = true;
      addLayers();
    });
    map.on("error", fallback);
    window.setTimeout(fallback, 8000);
    mapRef.current = map;
    (window as unknown as { __voltsightMap?: maplibregl.Map }).__voltsightMap = map; // read-only handle for the browser E2E tests
    return () => map.remove();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useEffect(() => {
    if (ready && vehicles.data) (mapRef.current!.getSource("vehicles") as maplibregl.GeoJSONSource).setData(vehiclesGeo(vehicles.data.vehicles) as any);
  }, [ready, vehicles.data]);
  useEffect(() => {
    if (ready && chargers.data) (mapRef.current!.getSource("chargers") as maplibregl.GeoJSONSource).setData(chargersGeo(chargers.data.chargers) as any);
  }, [ready, chargers.data]);
  useEffect(() => {
    if (ready) mapRef.current!.flyTo({ ...CITY_VIEW[city], duration: 1200 });
  }, [ready, city]);
  useEffect(() => {
    if (ready) mapRef.current!.setFilter("veh-selected", ["==", ["get", "vin"], vin ?? ""]);
  }, [ready, vin]);

  // alert evidence: the route is the A* road route the risk engine computed; draw it only if the alert has one
  const ev = alert.data?.alert?.evidence;
  const route: number[][] | undefined = ev?.route;
  useEffect(() => {
    if (!ready) return;
    const src = mapRef.current!.getSource("route") as maplibregl.GeoJSONSource;
    src.setData(route && route.length > 1 ? ({ type: "Feature", geometry: { type: "LineString", coordinates: route.map((p) => [p[1], p[0]]) }, properties: {} } as any) : empty);
    if (route && route.length > 1) {
      const b = new maplibregl.LngLatBounds();
      route.forEach((p) => b.extend([p[1], p[0]]));
      mapRef.current!.fitBounds(b, { padding: 70, maxZoom: 14, duration: 900 });
    } else if (ev?.lat && ev?.lon) mapRef.current!.flyTo({ center: [ev.lon, ev.lat], zoom: 12, duration: 900 });
  }, [ready, alert.data]);

  const v = detail.data?.vehicle;
  const st = v?.state;
  const row = vehicles.data?.vehicles?.find((x: any) => x.vin === vin);
  const margin = ev && ev.usable_range_km != null && ev.distance_to_charger_km != null ? ev.usable_range_km - ev.distance_to_charger_km : undefined;
  return (
    <div>
      <div className="geo-head">
        <span className={`conn ${live}`}>{live === "live" ? "LIVE ●" : live === "connecting" ? "CONNECTING…" : "RECONNECTING…"}</span>
        {!basemap && <span className="muted small">basemap unavailable (tile host unreachable): markers only</span>}
        <span className="muted small">
          {vehicles.data ? `${vehicles.data.count} vehicles` : "…"} · {chargers.data ? `${chargers.data.count} chargers` : "…"} · updated every 4 s from the backend
        </span>
      </div>
      <div ref={box} className="geomap" />
      <div className="legend small">
        <span><i style={{ background: "#16a34a" }} /> normal</span>
        <span><i style={{ background: "#f59e0b" }} /> low range (open warning)</span>
        <span><i style={{ background: "#dc2626" }} /> critical (open alert)</span>
        <span><i className="sq" style={{ background: "#2563eb" }} /> charger available</span>
        <span><i className="sq" style={{ background: "#f97316" }} /> occupied</span>
        <span><i className="sq" style={{ background: "#b91c1c" }} /> out of service</span>
        {route && <span><i className="ln" /> road route to the recommended charger (risk engine, A*)</span>}
      </div>
      {charger && (
        <div className="panel">
          <b>Charger</b> <button onClick={() => setCharger(null)}>×</button>
          <table className="kv"><tbody>
            <tr><td>ID</td><td>{charger.id}</td></tr><tr><td>Name</td><td>{na(charger.name)}</td></tr>
            <tr><td>Status</td><td>{charger.status}</td></tr><tr><td>Network</td><td>{na(charger.network)}</td></tr>
            <tr><td>Power</td><td>{na(Number(charger.power_kw), " kW")}</td></tr><tr><td>City</td><td>{na(charger.city)}</td></tr>
            <tr><td>Location</td><td>{Number(charger.lat).toFixed(4)}, {Number(charger.lon).toFixed(4)}</td></tr>
          </tbody></table>
        </div>
      )}
      {vin && (
        <div className="panel">
          <b>Vehicle {vin}</b> <button onClick={() => onSelect({})}>×</button>
          {!st && !row && <p className="muted">loading…</p>}
          <table className="kv"><tbody>
            <tr><td>Status</td><td>{row ? (row.risk === "critical" ? <Sev s="CRITICAL" /> : row.risk === "low" ? <Sev s="WARNING" /> : "normal") : "not available"}</td></tr>
            <tr><td>State of charge</td><td>{na(st?.soc_pct ?? row?.soc_pct, " %")}</td></tr>
            <tr><td>Usable range</td><td>{na(row?.usable_range_km, " km")}{row?.usable_range_km == null && " (computed by the risk engine only while an alert is open)"}</td></tr>
            <tr><td>Speed</td><td>{na(st?.speed_kmh ?? row?.speed_kmh, " km/h")}</td></tr>
            <tr><td>Last telemetry</td><td>{st?.last_seen ?? row?.last_seen ? `${fmtTime(st?.last_seen ?? row?.last_seen)} (${ago(st?.last_seen ?? row?.last_seen)})` : "not available"}</td></tr>
            <tr><td>City</td><td>{na(row?.city)}</td></tr>
            <tr><td>Nearest reachable charger</td><td>{na(row?.nearest_charger_id)}</td></tr>
            <tr><td>Charger distance</td><td>{na(row?.charger_distance_km, " km")}</td></tr>
            <tr><td>Range margin</td><td>{na(row?.range_margin_km, " km")}</td></tr>
            <tr><td>Model</td><td>{na(v?.model)}</td></tr>
          </tbody></table>
          <div className="small"><b>Recent alerts</b></div>
          {(detail.data?.alerts ?? []).length === 0 && <div className="muted small">none</div>}
          {(detail.data?.alerts ?? []).slice(0, 5).map((a: any) => (
            <div key={a.id} className="feed small"><Sev s={a.severity} /> <a href="#dashboard" onClick={() => onSelect({ vin, alertId: a.id })}>{a.rule.replace(/_/g, " ").toLowerCase()}</a> · {a.status} · {ago(a.detected_at)}</div>
          ))}
        </div>
      )}
      {alertId && ev && (
        <div className="panel">
          <b>Alert evidence</b> <button onClick={() => onSelect({ vin })}>×</button>
          <table className="kv"><tbody>
            <tr><td>Alert ID</td><td>{alertId}</td></tr><tr><td>VIN</td><td>{alert.data.alert.vin}</td></tr>
            <tr><td>Severity / rule</td><td><Sev s={alert.data.alert.severity} /> {alert.data.alert.rule}</td></tr>
            <tr><td>Created</td><td>{fmtTime(alert.data.alert.detected_at)} ({alert.data.alert.status})</td></tr>
            <tr><td>State of charge</td><td>{na(ev.soc_pct, " %")}</td></tr>
            <tr><td>Estimated range ({na(ev.estimator)})</td><td>{na(ev.estimated_range_km, " km")}</td></tr>
            <tr><td>Usable range (after {na(ev.safety_margin !== undefined ? Math.round(ev.safety_margin * 100) : undefined, " %")} safety margin)</td><td>{na(ev.usable_range_km, " km")}</td></tr>
            <tr><td>Nearest available charger</td><td>{ev.reachable_charger ? `${ev.nearest_charger_id} at ${na(ev.distance_to_charger_km, " km")}` : "none reachable"}</td></tr>
            <tr><td>Range margin (usable − distance)</td><td>{na(margin, " km")}</td></tr>
            <tr><td>Road route</td><td>{ev.route_km != null ? `${ev.route_km} km (A*, drawn on the map)` : "not available"}</td></tr>
            <tr><td>Vehicle position</td><td>{ev.lat != null ? `${Number(ev.lat).toFixed(4)}, ${Number(ev.lon).toFixed(4)}` : "not available"}</td></tr>
            <tr><td>Speed / ambient</td><td>{na(ev.speed_kmh, " km/h")} / {na(ev.ambient_c, " °C")}</td></tr>
            <tr><td>Chargers in city</td><td>{na(ev.city_chargers_available)} available of {na(ev.city_chargers_total)}</td></tr>
          </tbody></table>
        </div>
      )}
    </div>
  );
}
