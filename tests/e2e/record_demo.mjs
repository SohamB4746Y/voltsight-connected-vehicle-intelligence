// Records a silent screen recording of the demo journey against the running stack (Playwright video, no narration).
// usage: node record_demo.mjs <password> <outDir>
import { chromium } from "playwright-core";
const [pass, out = "demo"] = process.argv.slice(2);
const base = process.env.BASE_URL ?? "http://localhost:8081";
const browser = await chromium.launch({ executablePath: process.env.CHROMIUM ?? "/opt/pw-browsers/chromium-1194/chrome-linux/chrome", args: ["--no-sandbox"] });
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

async function session(user, fn) {
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 760 }, recordVideo: { dir: out, size: { width: 1280, height: 760 } } });
  const page = await ctx.newPage();
  const caption = async (text) =>
    page.evaluate((t) => {
      let el = document.getElementById("__cap");
      if (!el) {
        el = document.createElement("div");
        el.id = "__cap";
        el.style.cssText = "position:fixed;left:0;right:0;bottom:0;padding:10px 18px;background:rgba(15,23,42,.92);color:#fff;font:600 16px system-ui;z-index:99999";
        document.body.appendChild(el);
      }
      el.textContent = t;
    }, text);
  await page.goto(base);
  await page.getByRole("button", { name: "Sign in" }).click();
  await page.fill("#username", user);
  await page.fill("#password", pass);
  await page.click("#kc-login");
  await page.waitForSelector("nav .brand", { timeout: 30000 });
  await fn(page, caption);
  await ctx.close();
}

await session("dispatcher@meridian.example", async (page, cap) => {
  await cap("Dispatcher: live fleet overview - 100K-vehicle simulation, real-time state from Redis");
  await sleep(7000);
  await cap("Server-aggregated live map (colour = share of vehicles below 20% SoC)");
  await page.click("text=Chennai"); await sleep(5000);
  await page.click('nav a[href="#alerts"]');
  await cap("Range-risk alerts arrive over SSE; open one for the evidence");
  await page.waitForSelector("table.grid tbody tr", { timeout: 15000 }).catch(() => {});
  await sleep(2500);
  const rows = await page.$$("table.grid tbody tr");
  if (rows.length) { await rows[0].click(); await cap("Estimated vs usable range, nearest available charger, A* route"); await sleep(6000);
    const ack = page.getByRole("button", { name: "Acknowledge" }); if (await ack.isEnabled().catch(() => false)) { await ack.click(); await cap("Acknowledged - the action and the audit record commit together"); await sleep(3000); } }
  await page.click('nav a[href="#chargers"]');
  await cap("Chargers: taking a depot charger out of service re-routes every decision within seconds");
  await sleep(4000);
  await page.click('nav a[href="#vehicles"]');
  await page.waitForSelector("table.grid tbody tr", { timeout: 15000 }); await page.click("table.grid tbody tr");
  await cap("Vehicle detail: live state and 30-minute history from ClickHouse"); await sleep(6000);
  await page.click('nav a[href="#copilot"]');
  await cap("Charge Ops Copilot: tool calls on your tenant's data, cited answers");
  await page.click("text=Which vehicles are at risk right now?"); await sleep(6000);
  await page.fill("input[placeholder^='Ask']", "Find similar past incidents where chargers failed and a van was stranded");
  await page.keyboard.press("Enter"); await sleep(6000);
});

await session("energy_manager@meridian.example", async (page, cap) => {
  await page.click('nav a[href="#plans"]');
  await cap("Energy manager: minimum-cost charging plan (dynamic programming over time-of-use tariffs)");
  await sleep(2500);
  await page.getByRole("button", { name: /Propose plan/ }).click();
  await page.waitForSelector(".box.ok, .box.err", { timeout: 60000 }).catch(() => {});
  await sleep(4000);
  await cap("Nothing is scheduled until a person approves");
  const approve = page.getByRole("button", { name: "Approve" }).first();
  if (await approve.isVisible().catch(() => false)) { await approve.click(); await cap("Approved"); await sleep(3000); }
  await page.click('nav a[href="#reports"]'); await cap("Reports: battery state of health and trip energy rollups"); await sleep(5000);
});

await session("viewer@meridian.example", async (page, cap) => {
  await cap("Viewer: same data, coarse locations, no route detail, no write actions");
  await sleep(6000);
  await page.click('nav a[href="#alerts"]'); await sleep(5000);
});
await browser.close();
console.log("videos written to", out);
