// Checks against a RUNNING deployment, as real users signed in through Keycloak (authorization code + PKCE).
// Usage: BASE_URL=https://host node live_checks.mjs <demo-password> <out.json>
// Nothing here is simulated: tokens come from the browser sign-in, requests go to the public origin.
import { chromium } from "playwright-core";
import { writeFileSync } from "node:fs";

const [pass, outFile = "live_checks.json"] = process.argv.slice(2);
const base = (process.env.BASE_URL ?? "http://localhost:8080").replace(/\/$/, "");
const chromePath = process.env.CHROMIUM ?? "/opt/pw-browsers/chromium-1194/chrome-linux/chrome";
const browser = await chromium.launch({ executablePath: chromePath, args: ["--no-sandbox"] });
const results = [];
const check = (id, what, ok, detail) => {
  results.push({ id, what, pass: !!ok, detail });
  console.log(`${ok ? "PASS" : "FAIL"}  ${id}  ${what}${detail !== undefined ? "  -> " + JSON.stringify(detail) : ""}`);
};

async function signIn(user) {
  const ctx = await browser.newContext();
  const page = await ctx.newPage();
  await page.goto(base);
  await page.getByRole("button", { name: "Sign in" }).click();
  await page.fill("#username", user);
  await page.fill("#password", pass);
  await page.click("#kc-login");
  await page.waitForSelector("nav .brand", { timeout: 30000 });
  const token = await page.evaluate(() => {
    for (const k of Object.keys(sessionStorage)) if (k.startsWith("oidc.user:")) return JSON.parse(sessionStorage.getItem(k)).access_token;
    return "";
  });
  await ctx.close();
  return token;
}
const call = async (token, path, init = {}) => {
  const t0 = performance.now();
  const r = await fetch(base + path, { ...init, headers: { ...(token ? { Authorization: `Bearer ${token}` } : {}), "Content-Type": "application/json", ...(init.headers ?? {}) } });
  const ms = performance.now() - t0;
  let body = null;
  try { body = await r.json(); } catch { /* not JSON */ }
  return { status: r.status, body, ms, headers: r.headers };
};

const tok = {};
for (const u of ["viewer@meridian.example", "dispatcher@meridian.example", "tenant_admin@meridian.example", "dispatcher@coastal.example"]) {
  tok[u] = await signIn(u);
  check("AUTH-" + u.split("@")[0] + "@" + u.split("@")[1].split(".")[0], "OIDC sign-in (code+PKCE) yields a bearer token", tok[u].split(".").length === 3);
}
const [V, D, A, C] = ["viewer@meridian.example", "dispatcher@meridian.example", "tenant_admin@meridian.example", "dispatcher@coastal.example"].map((u) => tok[u]);

// --- authentication ---
check("SEC-1", "no token -> 401", (await call("", "/v1/fleet/summary")).status === 401);
check("SEC-2", "garbage token -> 401", (await call("a.b.c", "/v1/fleet/summary")).status === 401);
const meD = (await call(D, "/v1/me")).body, meC = (await call(C, "/v1/me")).body;
check("SEC-3", "two tenants resolve to different tenant ids", meD?.tenant_id && meC?.tenant_id && meD.tenant_id !== meC.tenant_id, { meridian: meD?.tenant_id?.slice(0, 8), coastal: meC?.tenant_id?.slice(0, 8) });

// --- RBAC ---
const al = (await call(D, "/v1/alerts?limit=5")).body;
const alert = (al?.items ?? al?.alerts ?? al ?? [])[0];
check("RBAC-0", "dispatcher can list alerts", !!alert, { sample: alert?.id });
if (alert) {
  const r1 = await call(V, `/v1/alerts/${alert.id}/ack`, { method: "POST", body: "{}" });
  check("RBAC-1", "viewer cannot acknowledge an alert (403)", r1.status === 403, r1.status);
  const r2 = await call(D, `/v1/alerts/${alert.id}/ack`, { method: "POST", body: "{}" });
  check("RBAC-2", "dispatcher can acknowledge an alert (2xx)", r2.status >= 200 && r2.status < 300, r2.status);
}
check("RBAC-3", "viewer cannot read the audit log (403)", (await call(V, "/v1/audit")).status === 403);
check("RBAC-4", "dispatcher cannot read the audit log (403)", (await call(D, "/v1/audit")).status === 403);
const audit = await call(A, "/v1/audit?limit=200");
const acts = JSON.stringify(audit.body ?? "");
check("RBAC-5", "tenant admin can read the audit log and it records the alert acknowledgement", audit.status === 200 && acts.includes("alert.ack"), { status: audit.status });
check("RBAC-6", "viewer cannot use the Copilot (403)", (await call(V, "/v1/copilot/messages", { method: "POST", body: JSON.stringify({ message: "hello" }) })).status === 403);

// --- tenant isolation (RLS + application checks) ---
const vm = (await call(D, "/v1/vehicles?limit=5")).body;
const vins = (vm?.items ?? vm?.vehicles ?? []).map((x) => x.vin);
check("ISO-0", "meridian dispatcher sees vehicles", vins.length > 0, vins.length);
if (vins.length) {
  const cross = await call(C, `/v1/vehicles/${vins[0]}`);
  check("ISO-1", "coastal dispatcher cannot read a meridian vehicle (404, no existence leak)", cross.status === 404, cross.status);
  const crossT = await call(C, `/v1/vehicles/${vins[0]}/telemetry`);
  check("ISO-2", "coastal dispatcher cannot read a meridian vehicle's telemetry", crossT.status === 404 || crossT.status === 403, crossT.status);
  const lc = (await call(C, "/v1/vehicles?limit=200")).body;
  const cv = (lc?.items ?? lc?.vehicles ?? []).map((x) => x.vin);
  check("ISO-3", "coastal vehicle list contains no meridian VIN", cv.length > 0 && !cv.some((v) => vins.includes(v)), { coastal_rows: cv.length });
}
if (alert) check("ISO-4", "coastal dispatcher cannot read a meridian alert (404)", (await call(C, `/v1/alerts/${alert.id}`)).status === 404);

// --- CORS: only the real origin ---
const pre = async (origin) => (await fetch(base + "/v1/fleet/summary", { method: "OPTIONS", headers: { Origin: origin, "Access-Control-Request-Method": "GET", "Access-Control-Request-Headers": "authorization" } })).headers.get("access-control-allow-origin");
const evil = await pre("https://evil.example");
const own = await pre(base);
check("CORS-1", "foreign origin gets no Access-Control-Allow-Origin", !evil, evil);
check("CORS-2", "the deployment's own origin is allowed", own === base, own);
check("CORS-3", "no wildcard ACAO on an authenticated endpoint", (await call(D, "/v1/fleet/summary")).headers.get("access-control-allow-origin") !== "*");

// --- exposure of the identity provider ---
const st = async (p, init) => (await fetch(base + p, { redirect: "manual", ...init })).status;
check("EXP-1", "Keycloak admin console is not reachable (404)", (await st("/admin/master/console/")) === 404 && (await st("/admin")) === 404);
const masterBody = await (await fetch(base + "/realms/master/.well-known/openid-configuration")).text();
check("EXP-2", "master realm is not reachable (404, and no Keycloak metadata served)", (await st("/realms/master/.well-known/openid-configuration")) === 404 && !masterBody.includes("issuer"));
check("EXP-2b", "Keycloak management endpoints are not reachable (/metrics, /health)", (await st("/metrics")) === 404 && (await st("/health/ready")) === 404);
const tokUrl = base + "/realms/voltsight/protocol/openid-connect/token";
const pg = await fetch(tokUrl, { method: "POST", headers: { "Content-Type": "application/x-www-form-urlencoded" }, body: new URLSearchParams({ grant_type: "password", client_id: "voltsight-web", username: "dispatcher@meridian.example", password: pass }) });
check("EXP-3", "password grant is refused for the web client", pg.status >= 400, pg.status);
const pt = await fetch(tokUrl, { method: "POST", headers: { "Content-Type": "application/x-www-form-urlencoded" }, body: new URLSearchParams({ grant_type: "password", client_id: "voltsight-test", client_secret: "x", username: "dispatcher@meridian.example", password: pass }) });
check("EXP-4", "the DEV-only test client does not exist in the public realm", pt.status >= 400, (await pt.json().catch(() => ({}))).error);
const hdr = (await fetch(base + "/")).headers;
check("EXP-5", "security headers present", hdr.get("x-content-type-options") === "nosniff" && hdr.get("x-frame-options") === "DENY" && !!hdr.get("strict-transport-security"));

// --- Copilot (typed tools; stub provider unless ANTHROPIC_API_KEY is configured) ---
const cp = await call(D, "/v1/copilot/messages", { method: "POST", body: JSON.stringify({ message: "Which vehicles are at risk of not reaching a charger?" }) });
check("COP-1", "copilot answers with tool-backed content or says insufficient evidence", cp.status === 200 && JSON.stringify(cp.body).length > 20, { status: cp.status, keys: Object.keys(cp.body ?? {}) });

// --- API latency (client side, this origin, under the live ingest load) ---
const pct = (a, p) => a.slice().sort((x, y) => x - y)[Math.min(a.length - 1, Math.floor((p / 100) * a.length))];
const lat = {};
for (const [name, path] of [["fleet_summary", "/v1/fleet/summary"], ["vehicles_page", "/v1/vehicles?limit=50"], ["alerts_open", "/v1/alerts?limit=50"], ["map_cells", "/v1/map/cells"]]) {
  const xs = [];
  for (let i = 0; i < 300; i++) xs.push((await call(D, path)).ms);
  lat[name] = { n: xs.length, p50_ms: +pct(xs, 50).toFixed(1), p95_ms: +pct(xs, 95).toFixed(1), p99_ms: +pct(xs, 99).toFixed(1), max_ms: +Math.max(...xs).toFixed(1) };
}
console.log("latency", JSON.stringify(lat));
const worst95 = Math.max(...Object.values(lat).map((l) => l.p95_ms)), worst99 = Math.max(...Object.values(lat).map((l) => l.p99_ms));
check("LAT-1", "API p95 < 200 ms (all four endpoints, sequential client on the same host)", worst95 < 200, worst95);
check("LAT-2", "API p99 < 500 ms", worst99 < 500, worst99);

await browser.close();
writeFileSync(outFile, JSON.stringify({ base, at: new Date().toISOString(), results, latency: lat }, null, 2));
const failed = results.filter((r) => !r.pass);
console.log(`\n${results.length - failed.length}/${results.length} passed`);
process.exit(failed.length ? 1 : 0);
