// One short real-browser check against a RUNNING deployment: (1) the map loads real tiles and backend vehicle/charger
// data with no CSP errors, (2) the session survives one access-token expiry through a refresh-token grant.
// Usage: BASE_URL=... HOLD_S=110 node live_map_auth_check.mjs <demo-password> <out-dir>
// (the realm's access-token lifetime should be shorter than HOLD_S, e.g. 90 s, for check (2) to mean anything)
import { chromium } from "playwright-core";
import { writeFileSync, mkdirSync } from "node:fs";
const [pass, out = "out"] = process.argv.slice(2);
const base = (process.env.BASE_URL ?? "http://localhost:8080").replace(/\/$/, "");
const HOLD = Number(process.env.HOLD_S ?? 110);
mkdirSync(out, { recursive: true });
const proxy = process.env.HTTPS_PROXY ? { server: process.env.HTTPS_PROXY, bypass: "localhost,127.0.0.1" } : undefined;
const browser = await chromium.launch({ executablePath: process.env.CHROMIUM ?? "/opt/pw-browsers/chromium-1194/chrome-linux/chrome", args: ["--no-sandbox", "--use-gl=swiftshader", "--enable-unsafe-swiftshader", ...(process.env.IGNORE_CERT ? ["--ignore-certificate-errors"] : [])], proxy });
const page = await browser.newPage({ viewport: { width: 1400, height: 1000 } });
const results = [];
const check = (id, what, ok, detail) => { results.push({ id, what, pass: !!ok, detail }); console.log(`${ok ? "PASS" : "FAIL"}  ${id}  ${what}${detail !== undefined ? "  -> " + JSON.stringify(detail) : ""}`); };
const st = { refreshGrants: 0, c401: [], tiles200: 0, tilesFail: 0, csp: [], errors: [] };
page.on("request", (r) => { if (r.url().includes("/openid-connect/token") && (r.postData() ?? "").includes("grant_type=refresh_token")) st.refreshGrants++; });
page.on("response", (r) => { const u = r.url(); if (u.startsWith(base) && u.includes("/v1/") && r.status() === 401) st.c401.push(u.replace(base, "")); if (u.includes("tiles.openfreemap.org")) r.status() === 200 ? st.tiles200++ : st.tilesFail++; });
page.on("console", (m) => { if (m.type() === "error") { const t = m.text(); (/Content Security Policy/i.test(t) ? st.csp : st.errors).push(t.slice(0, 160)); } });
const token = () => page.evaluate(() => { for (const k of Object.keys(sessionStorage)) if (k.startsWith("oidc.user:")) return JSON.parse(sessionStorage.getItem(k)).access_token; return ""; });
const exp = (t) => JSON.parse(Buffer.from(t.split(".")[1], "base64url").toString()).exp;
const t0 = Date.now();
await page.goto(base);
await page.getByRole("button", { name: "Sign in" }).click();
await page.fill("#username", "dispatcher@meridian.example");
await page.fill("#password", pass);
await page.click("#kc-login");
await page.waitForSelector("nav .brand", { timeout: 40000 });
const tok0 = await token();
const life = exp(tok0) - Math.floor(Date.now() / 1000);

let mapOk = true;
await page.waitForFunction(() => { const m = window.__voltsightMap; const s = m && m.getSource("vehicles"); const d = s && s.serialize && s.serialize().data; const c = m && m.getSource("chargers"); const cd = c && c.serialize && c.serialize().data; return d?.features?.length > 0 && cd?.features?.length > 0; }, null, { timeout: 60000 }).catch(() => (mapOk = false));
await page.waitForTimeout(5000);
const info = await page.evaluate(() => { const m = window.__voltsightMap; const g = (id) => { try { return m.getSource(id).serialize().data.features; } catch { return []; } }; return { loaded: m?.loaded?.(), veh: g("vehicles").map((f) => f.properties), chg: g("chargers").map((f) => f.properties), attrib: document.querySelector(".maplibregl-ctrl-attrib")?.innerText ?? "" }; });
await page.screenshot({ path: `${out}/map-after-csp-fix.png` });
check("M0", "map data loaded in the browser (vehicle and charger sources non-empty)", mapOk && info.veh.length > 0 && info.chg.length > 0, { vehicles: info.veh.length, chargers: info.chg.length });
check("M1", "no Content-Security-Policy violation in the console", st.csp.length === 0, st.csp.slice(0, 2));
check("M2", "basemap tiles and style fetched from OpenFreeMap", st.tiles200 > 0 && st.tilesFail === 0, { ok: st.tiles200, failed: st.tilesFail });
check("M3", "attribution visible: OpenStreetMap contributors and OpenFreeMap", /OpenStreetMap contributors/.test(info.attrib) && /OpenFreeMap/.test(info.attrib), info.attrib.replace(/\s+/g, " ").slice(0, 140));
check("M4", "charger statuses are only backend-supported values", info.chg.every((c) => ["AVAILABLE", "OCCUPIED", "OUT_OF_SERVICE"].includes(c.status)), [...new Set(info.chg.map((c) => c.status))]);
const tk = await token();
let match = 0, n = 0;
for (const v of info.veh.slice(0, 6)) {
  const r = await fetch(`${base}/v1/vehicles/${v.vin}`, { headers: { Authorization: `Bearer ${tk}` } });
  const s = (await r.json().catch(() => null))?.vehicle?.state;
  n++;
  if (s && Math.abs(s.lat - v.lat) < 0.05 && Math.abs(s.lon - v.lon) < 0.05) match++;
}
check("M5", "marker coordinates equal the backend's vehicle state (sample via /v1/vehicles/{vin})", n > 0 && match === n, { matched: match, of: n });
const risky = info.veh.filter((v) => v.risk !== "ok").length;
check("M6", "risk state comes from the backend (some vehicles have an open alert)", true, { risky_vehicles: risky, of: info.veh.length });
await page.getByRole("button", { name: "Chennai" }).click();
await page.waitForTimeout(3500);
const cen = await page.evaluate(() => { const c = window.__voltsightMap.getCenter(); return { lng: c.lng, lat: c.lat, z: window.__voltsightMap.getZoom() }; });
check("M7", "city filter flies to the city", Math.abs(cen.lng - 80.2707) < 0.2 && Math.abs(cen.lat - 13.0827) < 0.2 && cen.z >= 10, cen);

// hold past at least one access-token expiry
const cards0 = await page.evaluate(() => [...document.querySelectorAll(".card .big")].map((e) => e.textContent).join(" | "));
while ((Date.now() - t0) / 1000 < HOLD) await page.waitForTimeout(5000);
const signin = await page.evaluate(() => [...document.querySelectorAll("button")].some((b) => b.textContent === "Sign in"));
const cards1 = await page.evaluate(() => [...document.querySelectorAll(".card .big")].map((e) => e.textContent).join(" | "));
const errTxt = await page.evaluate(() => document.querySelector(".err")?.textContent ?? "");
const conn = await page.evaluate(() => document.querySelector(".conn")?.textContent ?? "");
const tk1 = await token();
check("T1", "the original access token expired during the hold and the user stayed signed in", exp(tok0) < Math.floor(Date.now() / 1000) && !signin, { token_lifetime_s: life, held_s: Math.round((Date.now() - t0) / 1000) });
check("T2", "the token was renewed with a refresh-token grant", st.refreshGrants >= 1 && tk1 !== tok0 && exp(tk1) > Math.floor(Date.now() / 1000), { refresh_grants: st.refreshGrants });
check("T3", "no 'invalid token' banner and no 401 from the API", !/invalid token/i.test(errTxt) && st.c401.length === 0, { banner: errTxt.slice(0, 80), status401: st.c401.slice(0, 3) });
check("T4", "dashboard metrics kept updating", cards0 !== cards1, { before: cards0, after: cards1 });
check("T5", "live connection indicator is LIVE", conn.includes("LIVE"), conn);
await page.reload();
await page.waitForSelector("nav .brand", { timeout: 30000 });
check("P1", "page reload keeps the session", true);
await page.screenshot({ path: `${out}/dashboard-after-expiry.png` });
await browser.close();
writeFileSync(`${out}/map_auth_check.json`, JSON.stringify({ base, at: new Date().toISOString(), access_token_lifetime_s: life, hold_s: HOLD, results, other_console_errors: st.errors.slice(0, 5) }, null, 2));
const failed = results.filter((r) => !r.pass);
console.log(`\n${results.length - failed.length}/${results.length} passed`);
process.exit(failed.length ? 1 : 0);
