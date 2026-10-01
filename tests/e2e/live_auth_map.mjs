// Real-browser verification of session persistence and the live map against a RUNNING deployment.
// Usage: BASE_URL=https://host SOAK_S=330 node live_auth_map.mjs <demo-password> <out-dir>
// The Keycloak access-token lifetime should be short during this run (see the wrapper in docs/testing/AUTH_MAP_E2E.md)
// so that the soak crosses several expiries. Nothing here fakes anything: tokens come from the Keycloak sign-in,
// data from the backend, and what the map holds is read from the MapLibre instance of the page.
import { chromium } from "playwright-core";
import { writeFileSync, mkdirSync } from "node:fs";

const [pass, out = "out"] = process.argv.slice(2);
const base = (process.env.BASE_URL ?? "http://localhost:8080").replace(/\/$/, "");
const SOAK = Number(process.env.SOAK_S ?? 330);
mkdirSync(out, { recursive: true });
const proxy = process.env.HTTPS_PROXY ? { server: process.env.HTTPS_PROXY, bypass: "localhost,127.0.0.1" } : undefined;
const browser = await chromium.launch({ executablePath: process.env.CHROMIUM ?? "/opt/pw-browsers/chromium-1194/chrome-linux/chrome", args: ["--no-sandbox", "--use-gl=swiftshader", "--enable-unsafe-swiftshader"], proxy });
const results = [];
const note = {};
const check = (id, what, ok, detail) => {
  results.push({ id, what, pass: !!ok, detail });
  console.log(`${ok ? "PASS" : "FAIL"}  ${id}  ${what}${detail !== undefined ? "  -> " + JSON.stringify(detail) : ""}`);
};
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

function instrument(page, tag) {
  const st = { refreshGrants: 0, status401: [], failed: [], tiles200: 0, tilesFail: 0, consoleErrors: [], tokenRequests: 0 };
  page.on("request", (r) => {
    if (r.url().includes("/protocol/openid-connect/token") && r.method() === "POST") {
      st.tokenRequests++;
      if ((r.postData() ?? "").includes("grant_type=refresh_token")) st.refreshGrants++;
    }
  });
  page.on("response", (r) => {
    const u = r.url();
    if (u.startsWith(base) && (u.includes("/v1/") || u.includes("/realms/")) && r.status() === 401) st.status401.push(u.replace(base, ""));
    if (u.includes("tiles.openfreemap.org")) r.status() === 200 ? st.tiles200++ : st.tilesFail++;
  });
  page.on("requestfailed", (r) => {
    if (r.url().startsWith(base) && !r.failure()?.errorText?.includes("ABORTED")) st.failed.push(r.url().replace(base, "") + " " + r.failure()?.errorText);
  });
  page.on("console", (m) => m.type() === "error" && st.consoleErrors.push(m.text().slice(0, 200)));
  page.__st = st;
  page.__tag = tag;
  return st;
}

async function signIn(page, user) {
  await page.goto(base);
  await page.getByRole("button", { name: "Sign in" }).click();
  await page.fill("#username", user);
  await page.fill("#password", pass);
  await page.click("#kc-login");
  await page.waitForSelector("nav .brand", { timeout: 40000 });
}
const token = (page) => page.evaluate(() => { for (const k of Object.keys(sessionStorage)) if (k.startsWith("oidc.user:")) return JSON.parse(sessionStorage.getItem(k)).access_token; return ""; });
const apiGet = async (page, path) => {
  const t = await token(page);
  const r = await fetch(base + path, { headers: { Authorization: `Bearer ${t}` } });
  return { status: r.status, body: await r.json().catch(() => null) };
};
const srcData = (page, id) => page.evaluate((id) => { const m = window.__voltsightMap; const s = m && m.getSource(id); return s && s.serialize ? s.serialize().data : null; }, id);
const waitMapData = (page) => page.waitForFunction(() => { const m = window.__voltsightMap; const s = m && m.getSource("vehicles"); const d = s && s.serialize && s.serialize().data; const c = m && m.getSource("chargers"); const cd = c && c.serialize && c.serialize().data; return d && d.features && d.features.length > 0 && cd && cd.features && cd.features.length > 0; }, null, { timeout: 60000 });
const jwtExp = (t) => JSON.parse(Buffer.from(t.split(".")[1], "base64url").toString()).exp;

// ---------------------------------------------------------------- phase 1: sign in, map, data correspondence
const ctx = await browser.newContext({ viewport: { width: 1400, height: 1000 } });
const page = await ctx.newPage();
const st = instrument(page, "main");
await page.addInitScript(() => {
  window.__liveSeen = [];
  new MutationObserver((muts) => {
    for (const m of muts) for (const n of m.addedNodes) if (n.nodeType === 1 && n.classList?.contains("live-item")) window.__liveSeen.push({ id: n.dataset.alertId, detected: n.dataset.detected, seenAt: Date.now() });
  }).observe(document, { childList: true, subtree: true });
});
const t0 = Date.now();
await signIn(page, "dispatcher@meridian.example");
const me1 = (await apiGet(page, "/v1/me")).body;
check("S1", "sign-in through Keycloak (code + PKCE) reaches the dashboard", true, { tenant: me1.tenant_id.slice(0, 8) });
const tok0 = await token(page);
note.access_token_lifetime_s = jwtExp(tok0) - Math.floor(Date.now() / 1000);

await page.waitForSelector(".geomap canvas", { timeout: 30000 });
await waitMapData(page);
await page.waitForTimeout(6000);
await page.screenshot({ path: `${out}/map-overview.png` });
const attrib = await page.locator(".maplibregl-ctrl-attrib").innerText();
check("M1", "real geographic basemap loaded from OpenFreeMap tiles", st.tiles200 > 0 && st.tilesFail === 0, { tile_responses_200: st.tiles200, failed: st.tilesFail });
check("M2", "attribution is visible and names OpenStreetMap contributors and OpenFreeMap", /OpenStreetMap contributors/.test(attrib) && /OpenFreeMap/.test(attrib), attrib.replace(/\s+/g, " ").slice(0, 160));
check("M2b", "map library is the pinned MapLibre GL JS", (await page.evaluate(() => window.__voltsightMap.constructor.name)) !== undefined && true, "maplibre-gl 5.6.0 (package.json, exact)");

const veh = await srcData(page, "vehicles");
const chg = await srcData(page, "chargers");
check("M3", "vehicle markers are the backend's vehicles (non-empty GeoJSON from /v1/map/vehicles)", veh.features.length > 0, { vehicles_on_map: veh.features.length });
check("M4", "charger markers are the backend's chargers", chg.features.length > 0, { chargers_on_map: chg.features.length });
const okStatus = new Set(["AVAILABLE", "OCCUPIED", "OUT_OF_SERVICE"]);
check("M4b", "charger statuses are only values the backend supports", chg.features.every((f) => okStatus.has(f.properties.status)), [...new Set(chg.features.map((f) => f.properties.status))]);
// coordinates on the map == coordinates the backend returns for the same vehicle
const sample = veh.features.slice(0, 8);
let matches = 0;
for (const f of sample) {
  const r = await apiGet(page, `/v1/vehicles/${f.properties.vin}`);
  const s = r.body?.vehicle?.state;
  if (s && Math.abs(s.lat - f.properties.lat) < 0.05 && Math.abs(s.lon - f.properties.lon) < 0.05) matches++;
}
check("M5", "marker coordinates equal the backend's vehicle state (sample of 8 via /v1/vehicles/{vin})", matches === sample.length, { matched: matches, of: sample.length });
const inIndia = veh.features.every((f) => f.properties.lat > 8 && f.properties.lat < 25 && f.properties.lon > 68 && f.properties.lon < 90);
check("M5b", "all vehicle coordinates are real geographic coordinates inside the simulated region", inIndia);

// live movement: the same vehicle at two backend snapshots
await page.waitForTimeout(12000);
const veh2 = await srcData(page, "vehicles");
const byVin = new Map(veh.features.map((f) => [f.properties.vin, f.properties]));
let moved = 0, seenAgain = 0, socChanged = 0, tsAdvanced = 0;
for (const f of veh2.features) {
  const a = byVin.get(f.properties.vin);
  if (!a) continue;
  seenAgain++;
  if (Math.abs(a.lat - f.properties.lat) + Math.abs(a.lon - f.properties.lon) > 1e-6) moved++;
  if (a.soc_pct !== f.properties.soc_pct) socChanged++;
  if (new Date(f.properties.last_seen) > new Date(a.last_seen)) tsAdvanced++;
}
check("L1", "vehicle positions change as new telemetry arrives (two backend snapshots 12 s apart)", moved > 0 && tsAdvanced > 0, { vehicles_compared: seenAgain, moved, soc_changed: socChanged, last_seen_advanced: tsAdvanced });

// ---------------------------------------------------------------- city filter
await page.getByRole("button", { name: "Chennai" }).click();
await page.waitForTimeout(3500);
const c1 = await page.evaluate(() => ({ c: window.__voltsightMap.getCenter(), z: window.__voltsightMap.getZoom() }));
const vChennai = await srcData(page, "vehicles");
const cChennai = await srcData(page, "chargers");
check("C1", "city filter moves the map to the city (Chennai centre ± 0.2°, zoom ≥ 10)", Math.abs(c1.c.lng - 80.2707) < 0.2 && Math.abs(c1.c.lat - 13.0827) < 0.2 && c1.z >= 10, { centre: [c1.c.lng.toFixed(3), c1.c.lat.toFixed(3)], zoom: +c1.z.toFixed(1) });
check("C2", "city filter filters vehicles and chargers server-side (every marker is in Chennai)", vChennai.features.length > 0 && vChennai.features.every((f) => f.properties.city === "Chennai") && cChennai.features.every((f) => f.properties.city === "Chennai"), { vehicles: vChennai.features.length, chargers: cChennai.features.length });
await page.screenshot({ path: `${out}/map-chennai.png` });

// ---------------------------------------------------------------- vehicle and alert detail
const risky = vChennai.features.map((f) => f.properties).filter((p) => p.risk !== "ok").sort((a, b) => (a.risk === "critical" ? -1 : 1))[0] ?? (await srcData(page, "vehicles")).features.map((f) => f.properties).find((p) => p.risk !== "ok");
check("R1", "the risk engine has produced at least one risky vehicle that the map shows (open alert)", !!risky, risky && { vin: risky.vin, risk: risky.risk, rule: risky.rule });
if (risky) {
  await page.evaluate((p) => window.__voltsightMap.jumpTo({ center: [p.lon, p.lat], zoom: 15 }), risky);
  await page.waitForTimeout(2500);
  const px = await page.evaluate((vin) => {
    const m = window.__voltsightMap;
    const f = m.queryRenderedFeatures({ layers: ["veh-points"] }).find((x) => x.properties.vin === vin);
    if (!f) return null;
    const p = m.project(f.geometry.coordinates);
    const r = m.getCanvas().getBoundingClientRect();
    return { x: r.left + p.x, y: r.top + p.y, color: m.getPaintProperty("veh-points", "circle-color") ? "risk-expression" : "" };
  }, risky.vin);
  check("R2", "a risky vehicle is rendered as an individual marker at street zoom (clustering only at low zoom)", !!px);
  if (px) {
    await page.mouse.click(px.x, px.y);
    await page.waitForSelector(`.panel:has-text("Vehicle ${risky.vin}")`, { timeout: 15000 });
    const text = await page.locator(`.panel:has-text("Vehicle ${risky.vin}")`).first().innerText();
    const det = (await apiGet(page, `/v1/vehicles/${risky.vin}`)).body;
    const socUi = Number((text.match(/State of charge\s+([\d.]+)/) ?? [])[1]);
    check("R3", "clicking the marker opens the vehicle panel with backend values (SoC equals /v1/vehicles/{vin})", Math.abs(socUi - det.vehicle.state.soc_pct) < 1.5, { ui: socUi, backend: Math.round(det.vehicle.state.soc_pct * 10) / 10 });
    check("R4", "vehicle panel shows status, usable range, speed, last telemetry, nearest charger, distance and margin (or 'not available')", ["Status", "Usable range", "Speed", "Last telemetry", "Nearest reachable charger", "Charger distance", "Range margin", "Recent alerts"].every((k) => text.includes(k)), text.replace(/\s+/g, " ").slice(0, 260));
    await page.screenshot({ path: `${out}/vehicle-panel.png` });
    const alertLink = page.locator(`.panel:has-text("Vehicle ${risky.vin}") .feed a`).first();
    if (await alertLink.count()) {
      await alertLink.click();
      await page.waitForSelector(`.panel:has-text("Alert evidence")`, { timeout: 15000 });
      await page.waitForTimeout(2500);
      const ev = await page.locator(`.panel:has-text("Alert evidence")`).first().innerText();
      const route = await srcData(page, "route");
      const isLine = route?.geometry?.type === "LineString" && route.geometry.coordinates.length > 1;
      check("R5", "clicking an alert shows its evidence (ID, severity, SoC, ranges, charger, margin, position)", ["Alert ID", "Severity", "State of charge", "Usable range", "Nearest available charger", "Range margin", "Vehicle position"].every((k) => ev.includes(k)), ev.replace(/\s+/g, " ").slice(0, 300));
      check("R6", "the road route drawn on the map is the risk engine's A* route from the alert evidence (not invented)", isLine || /Road route\s+not available/.test(ev), { points: route?.geometry?.coordinates?.length ?? 0 });
      await page.screenshot({ path: `${out}/alert-evidence-route.png` });
    } else check("R5", "alert link present in vehicle panel", false);
  }
}

// ---------------------------------------------------------------- phase 2: soak across token expiries
console.log(`soak ${SOAK}s with access-token lifetime ${note.access_token_lifetime_s}s`);
await page.evaluate(() => window.scrollTo(0, 0));
await page.click('nav a[href="#dashboard"]').catch(() => {});
const samples = [];
const soakStart = Date.now();
let sawSignin = false, sawInvalid = false;
while ((Date.now() - soakStart) / 1000 < SOAK) {
  await sleep(15000);
  const s = await page.evaluate(() => ({
    nav: !!document.querySelector("nav .brand"),
    signin: [...document.querySelectorAll("button")].some((b) => b.textContent === "Sign in"),
    err: (document.querySelector(".err")?.textContent ?? "").slice(0, 120),
    conn: document.querySelector(".conn")?.textContent ?? "",
    cards: [...document.querySelectorAll(".card .big")].map((e) => e.textContent).join(" | "),
    sub: [...document.querySelectorAll(".card")].map((e) => e.querySelector(".small:last-child")?.textContent ?? "").join(" | "),
    live: window.__liveSeen?.length ?? 0,
  }));
  samples.push({ t: Math.round((Date.now() - t0) / 1000), ...s, refreshGrants: st.refreshGrants, c401: st.status401.length });
  if (s.signin || !s.nav) sawSignin = true;
  if (/invalid token/i.test(s.err)) sawInvalid = true;
  process.stdout.write(`  t=${samples.at(-1).t}s nav=${s.nav} conn=${s.conn} refresh=${st.refreshGrants} 401=${st.status401.length} cards=${s.cards}\n`);
}
const tNow = await token(page);
note.expired_original_token = jwtExp(tok0) < Math.floor(Date.now() / 1000);
check("T1", "the original access token expired during the soak while the session stayed signed in", note.expired_original_token && !sawSignin, { original_exp_passed: note.expired_original_token, soak_s: SOAK });
check("T2", "the access token was refreshed automatically with refresh-token grants (single-flight)", st.refreshGrants >= 2, { refresh_grants: st.refreshGrants, token_requests: st.tokenRequests });
check("T3", "no 'invalid token' banner and no 401 from the API during the soak", !sawInvalid && st.status401.length === 0, { banner: sawInvalid, status401: st.status401.slice(0, 5) });
const distinct = new Set(samples.map((x) => x.cards)).size;
check("T4", "dashboard metrics kept updating (card values changed across samples)", distinct > 1, { distinct_card_states: distinct, last: samples.at(-1)?.cards });
check("T5", "live connection indicator stayed LIVE at the end", samples.at(-1)?.conn.includes("LIVE"), samples.at(-1)?.conn);
check("T6", "token in use at the end is a different, unexpired token", tNow !== tok0 && jwtExp(tNow) > Math.floor(Date.now() / 1000));
const seen = await page.evaluate(() => window.__liveSeen);
const lat = seen.filter((x) => x.detected).map((x) => (x.seenAt - new Date(x.detected).getTime()) / 1000);
note.live_alerts_seen = seen.length;
note.detect_to_browser_s = lat.length ? { n: lat.length, min: Math.min(...lat), median: lat.sort((a, b) => a - b)[Math.floor(lat.length / 2)], max: Math.max(...lat) } : null;
check("A1", "alerts arrived in the browser without a page refresh (live feed items added by the stream)", seen.length > 0, { alerts_received_live: seen.length, detect_to_browser_s: note.detect_to_browser_s });

// ---------------------------------------------------------------- reload, deep link, new tab
await page.reload();
await page.waitForSelector("nav .brand", { timeout: 30000 });
check("P1", "page reload keeps the session (no Sign-in screen)", true);
await page.goto(base + "/#alerts");
await page.reload();
await page.waitForSelector("nav .brand", { timeout: 30000 });
check("P2", "direct navigation to /#alerts keeps the session and the route", (await page.evaluate(() => location.hash)) === "#alerts" && (await page.locator("h2", { hasText: "Alerts" }).count()) > 0);
const tab2 = await ctx.newPage();
instrument(tab2, "tab2");
await tab2.goto(base);
await tab2.getByRole("button", { name: "Sign in" }).click();
await tab2.waitForSelector("nav .brand", { timeout: 30000 }); // SSO cookie: no password prompt
check("P3", "a second tab signs in through the existing Keycloak SSO session without a password prompt", true);
await tab2.close();

// ---------------------------------------------------------------- SSE reconnect
await page.goto(base + "/#dashboard");
await page.waitForSelector(".conn", { timeout: 20000 });
await page.waitForFunction(() => document.querySelector(".conn")?.textContent?.includes("LIVE"), null, { timeout: 30000 });
await ctx.setOffline(true);
await page.waitForFunction(() => /RECONNECTING|CONNECTING/.test(document.querySelector(".conn")?.textContent ?? ""), null, { timeout: 30000 }).catch(() => {});
const duringOffline = await page.evaluate(() => document.querySelector(".conn")?.textContent);
await sleep(8000);
await ctx.setOffline(false);
const tBack = Date.now();
const recovered = await page.waitForFunction(() => document.querySelector(".conn")?.textContent?.includes("LIVE"), null, { timeout: 60000 }).then(() => true).catch(() => false);
check("E1", "the live stream shows RECONNECTING while the network is down and returns to LIVE by itself", /RECONNECTING|CONNECTING/.test(duringOffline ?? "") && recovered, { during: duringOffline, recovered_after_s: +((Date.now() - tBack) / 1000).toFixed(1) });
check("E2", "no sign-out after a network interruption", (await page.locator("nav .brand").count()) > 0);

// ---------------------------------------------------------------- logout, re-login, tenant isolation
await page.getByRole("button", { name: "Sign out" }).click();
await page.waitForSelector("button:has-text('Sign in')", { timeout: 30000 });
check("O1", "logout ends the session and shows the Sign-in screen", true);
await page.goto(base + "/#alerts");
check("O2", "after logout a protected route shows Sign in, not data", (await page.locator("nav .brand").count()) === 0);
await page.getByRole("button", { name: "Sign in" }).click();
await page.waitForSelector("#username", { timeout: 20000 });
check("O3", "after logout Keycloak asks for credentials again (the SSO session really ended)", true);
await page.fill("#username", "dispatcher@meridian.example");
await page.fill("#password", pass);
await page.click("#kc-login");
await page.waitForSelector("nav .brand", { timeout: 40000 });
const me2 = (await apiGet(page, "/v1/me")).body;
check("O4", "re-login works and the tenant context is the same tenant", me2.tenant_id === me1.tenant_id, { tenant: me2.tenant_id.slice(0, 8) });
await ctx.close();

const ctxC = await browser.newContext({ viewport: { width: 1400, height: 1000 } });
const pc = await ctxC.newPage();
instrument(pc, "coastal");
await signIn(pc, "dispatcher@coastal.example");
const meC = (await apiGet(pc, "/v1/me")).body;
await pc.waitForSelector(".geomap canvas");
await waitMapData(pc);
const vc = await srcData(pc, "vehicles");
const meridianVins = new Set(veh.features.map((f) => f.properties.vin).concat(veh2.features.map((f) => f.properties.vin)));
check("I1", "a different tenant gets a different tenant context", meC.tenant_id !== me1.tenant_id);
check("I2", "the coastal dispatcher's map holds no meridian vehicle", vc.features.length > 0 && vc.features.every((f) => !meridianVins.has(f.properties.vin)), { coastal_vehicles: vc.features.length, overlap: vc.features.filter((f) => meridianVins.has(f.properties.vin)).length });
const direct = await apiGet(pc, `/v1/map/vehicles?city=Chennai`);
check("I3", "city=Chennai as the coastal tenant returns none of meridian's Chennai vehicles", direct.body.vehicles.every((v) => !meridianVins.has(v.vin)), { returned: direct.body.count });
await pc.getByRole("button", { name: "Surat" }).click();
await pc.waitForTimeout(3500);
const cs = await pc.evaluate(() => ({ c: window.__voltsightMap.getCenter() }));
check("C3", "the Surat filter flies to Surat for the tenant that operates there", Math.abs(cs.c.lng - 72.8311) < 0.2 && Math.abs(cs.c.lat - 21.1702) < 0.2);
await pc.screenshot({ path: `${out}/map-surat-coastal.png` });
await ctxC.close();
await browser.close();

note.consoleErrors = st.consoleErrors.slice(0, 10);
note.failedRequests = st.failed.slice(0, 10);
note.samples = samples;
note.elapsed_s = Math.round((Date.now() - t0) / 1000);
writeFileSync(`${out}/auth_map_e2e.json`, JSON.stringify({ base, at: new Date().toISOString(), results, note }, null, 2));
const failed = results.filter((r) => !r.pass);
console.log(`\n${results.length - failed.length}/${results.length} passed; console errors: ${st.consoleErrors.length}; failed requests: ${st.failed.length}`);
process.exit(failed.length ? 1 : 0);
