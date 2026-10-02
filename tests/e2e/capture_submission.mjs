// Captures real screenshots of a RUNNING VoltSight deployment for the submission package.
// Usage: BASE_URL=http://localhost:8080 DEMO_PASSWORD=... node capture_submission.mjs <outDir>   (password is read from the environment, never printed)
import { chromium } from "playwright-core";
import { mkdirSync, writeFileSync } from "node:fs";
const out = process.argv[2] ?? "shots";
const base = (process.env.BASE_URL ?? "http://localhost:8080").replace(/\/$/, "");
const pass = process.env.DEMO_PASSWORD;
mkdirSync(out, { recursive: true });
const browser = await chromium.launch({ executablePath: process.env.CHROMIUM ?? "/opt/pw-browsers/chromium-1194/chrome-linux/chrome", args: ["--no-sandbox", "--use-gl=swiftshader", "--enable-unsafe-swiftshader"], proxy: process.env.HTTPS_PROXY ? { server: process.env.HTTPS_PROXY, bypass: "localhost,127.0.0.1" } : undefined });
const log = [];
const note = (k, v) => { log.push({ k, v }); console.log(k, JSON.stringify(v)); };
async function login(user) {
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 }, deviceScaleFactor: 1 });
  const page = await ctx.newPage();
  await page.goto(base);
  if (user === "__landing") { await page.waitForTimeout(1500); await page.screenshot({ path: `${out}/01-login-landing.png` }); await page.getByRole("button", { name: "Sign in" }).click(); await page.waitForSelector("#username"); await page.screenshot({ path: `${out}/01b-login-keycloak.png` }); user = "dispatcher@meridian.example"; }
  else await page.getByRole("button", { name: "Sign in" }).click();
  await page.fill("#username", user); await page.fill("#password", pass); await page.click("#kc-login");
  await page.waitForSelector("nav .brand", { timeout: 40000 });
  return { ctx, page };
}
{
  const { ctx, page } = await login("__landing");
  await page.waitForTimeout(14000);
  await page.screenshot({ path: `${out}/02-dashboard.png` });
  note("dashboard_cards", await page.evaluate(() => [...document.querySelectorAll(".card")].map((e) => e.innerText.replace(/\s+/g, " ").trim()).slice(0, 8)));
  const info = await page.evaluate(() => { const m = window.__voltsightMap; const g = (id) => { try { return m.getSource(id).serialize().data.features.length; } catch { return -1; } }; return { vehicles: g("vehicles"), chargers: g("chargers") }; });
  note("map_sources", info);
  // risk -> evidence from the live alert feed
  const link = page.locator("[data-alert-id]").first();
  if (await link.count()) { await link.click(); await page.waitForTimeout(3500); await page.screenshot({ path: `${out}/03-map-risk-selected-alert.png` }); }
  await page.getByRole("button", { name: "Chennai" }).click().catch(() => {});
  await page.waitForTimeout(3500);
  await page.screenshot({ path: `${out}/04-map-city-chennai.png` });
  for (const [id, f] of [["alerts", "05-alerts"], ["vehicles", "06-vehicles"], ["chargers", "07-chargers"], ["plans", "08-charge-plans"]]) {
    await page.evaluate((h) => { location.hash = h; }, id); await page.waitForTimeout(3000); await page.screenshot({ path: `${out}/${f}.png` });
  }
  // alert detail
  await page.evaluate(() => { location.hash = "alerts"; }); await page.waitForTimeout(2500);
  const row = page.locator("table tbody tr").first();
  if (await row.count()) { await row.click(); await page.waitForTimeout(2500); await page.screenshot({ path: `${out}/09-alert-evidence-route.png` }); }
  await page.evaluate(() => { location.hash = "copilot"; }); await page.waitForTimeout(1500);
  await page.getByRole("button", { name: "Which vehicles are at risk right now?" }).click();
  await page.waitForTimeout(9000);
  await page.screenshot({ path: `${out}/10-copilot-at-risk.png` });
  await ctx.close();
}
{
  const { ctx, page } = await login("tenant_admin@meridian.example");
  await page.evaluate(() => { location.hash = "audit"; }); await page.waitForTimeout(3000);
  await page.screenshot({ path: `${out}/11-audit-trail.png` });
  await page.evaluate(() => { location.hash = "ops"; }); await page.waitForTimeout(3000);
  await page.screenshot({ path: `${out}/12-platform-ops.png` });
  await ctx.close();
}
writeFileSync(`${out}/capture_notes.json`, JSON.stringify({ base, at: new Date().toISOString(), notes: log }, null, 2));
await browser.close();
