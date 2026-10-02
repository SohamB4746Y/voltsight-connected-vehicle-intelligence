// Records the real VoltSight console for the explainer video. Browser segments run back to back; marks.json holds each segment's offset.
import { chromium } from "playwright-core";
import { readFileSync, writeFileSync, mkdirSync } from "node:fs";
const segs = JSON.parse(readFileSync("./submission_narration.json"));
const pass = process.env.DEMO_PASSWORD; const base = "http://localhost:8080";
mkdirSync("/tmp/vid/rec", { recursive: true });
const browser = await chromium.launch({ executablePath: "/opt/pw-browsers/chromium-1194/chrome-linux/chrome", args: ["--no-sandbox", "--use-gl=swiftshader", "--enable-unsafe-swiftshader"], proxy: process.env.HTTPS_PROXY ? { server: process.env.HTTPS_PROXY, bypass: "localhost,127.0.0.1" } : undefined });
const ctx = await browser.newContext({ viewport: { width: 1600, height: 900 }, recordVideo: { dir: "/tmp/vid/rec", size: { width: 1600, height: 900 } } });
await ctx.addInitScript(() => {
  const mk = () => {
    if (document.getElementById("__cur")) return;
    const c = document.createElement("div"); c.id = "__cur";
    c.style.cssText = "position:fixed;z-index:2147483647;width:18px;height:18px;border-radius:50%;background:rgba(255,196,0,.85);border:2px solid #fff;box-shadow:0 0 6px #000;pointer-events:none;left:-30px;top:-30px;transform:translate(-50%,-50%)";
    document.documentElement.appendChild(c);
    addEventListener("mousemove", (e) => { c.style.left = e.clientX + "px"; c.style.top = e.clientY + "px"; }, true);
    addEventListener("mousedown", () => { c.style.background = "rgba(255,80,0,.9)"; }, true); addEventListener("mouseup", () => { c.style.background = "rgba(255,196,0,.85)"; }, true);
  };
  if (document.readyState !== "loading") mk(); else addEventListener("DOMContentLoaded", mk);
  new MutationObserver(mk).observe(document, { childList: true, subtree: true });
});
const page = await ctx.newPage();
const T0 = Date.now();
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const bar = (t) => page.evaluate((x) => { let e = document.getElementById("__bar"); if (!e) { e = document.createElement("div"); e.id = "__bar"; e.style.cssText = "position:fixed;left:0;right:0;top:0;z-index:2147483646;padding:7px 16px;background:rgba(15,27,45,.93);color:#fff;font:600 15px Helvetica,Arial,sans-serif;display:flex;justify-content:space-between"; document.documentElement.appendChild(e); const st = document.createElement('style'); st.textContent = '#root{padding-top:34px;box-sizing:border-box}'; document.head.appendChild(st); } e.innerHTML = "<span>" + x + "</span><span style='color:#35c493'>⚡ VoltSight · live recording of the running application</span>"; }, t).catch(() => {});
const move = async (x, y, steps = 25) => { await page.mouse.move(x, y, { steps }); };
const center = async (loc) => { const b = await loc.boundingBox(); return b ? [b.x + b.width / 2, b.y + b.height / 2] : null; };
const clickLoc = async (loc) => { const c = await center(loc); if (c) { await move(c[0], c[1], 30); await sleep(250); await page.mouse.down(); await sleep(90); await page.mouse.up(); } };
// login (trimmed away: it is before the first mark)
await page.goto(base); await page.getByRole("button", { name: "Sign in" }).click();
await page.fill("#username", "dispatcher@meridian.example"); await page.fill("#password", pass); await page.click("#kc-login");
await page.waitForSelector("nav .brand", { timeout: 40000 });
await sleep(9000);
await page.getByRole("button", { name: "Chennai", exact: true }).click(); await sleep(7000);
const marks = {};
const seg = async (id, fn) => { const s = segs.find((x) => x.id === id); const t = Date.now(); marks[id] = { start: (t - T0) / 1000, dur: s.dur + 0.5 }; await bar(s.title); await fn(); const left = (s.dur + 0.5) * 1000 - (Date.now() - t); if (left > 0) await sleep(left); console.log(id, "ran", ((Date.now() - t) / 1000).toFixed(1), "of", (s.dur + 0.5).toFixed(1)); };
const cards = () => page.locator(".card");
await seg("s1", async () => { await page.evaluate(() => { location.hash = "dashboard"; }); await move(800, 450, 40); await sleep(4000); for (let i = 0; i < 5; i++) { const c = await center(cards().nth(i)); if (c) { await move(c[0], c[1] + 10, 30); await sleep(1700); } } await move(1200, 480, 40); });
await seg("s4", async () => { for (const i of [0, 1, 2, 3, 4]) { const c = await center(cards().nth(i)); if (c) { await move(c[0], c[1] + 10, 30); await sleep(2800); } } const l = page.locator("[data-alert-id], a").filter({ hasText: "ZA" }).first(); await move(1200, 380, 40); await sleep(3000); await move(1200, 520, 40); });
await seg("s5", async () => {
  await move(700, 560, 40); await sleep(2500);
  for (const c of ["Bengaluru", "Surat", "All cities", "Chennai"]) { await clickLoc(page.getByRole("button", { name: c, exact: true })); await sleep(3300); }
  await move(620, 600, 30); await page.mouse.wheel(0, -300); await sleep(1800); await page.mouse.wheel(0, 300); await sleep(1500);
});
await seg("s6", async () => {
  await page.evaluate(() => { location.hash = "dashboard"; }); await sleep(1500);
  const l = page.locator("a", { hasText: /^ZA|^ZR|^ZN|^ZZ/ }).first(); if (await l.count()) { await clickLoc(l); await sleep(5500); }
  await move(640, 560, 30); await sleep(2500);
  await page.evaluate(() => { location.hash = "alerts"; }); await sleep(2500);
  const row = page.locator("table tbody tr").first(); if (await row.count()) { await clickLoc(row); await sleep(6000); }
});
await seg("s7", async () => {
  await page.evaluate(() => { location.hash = "copilot"; }); await sleep(2500);
  const b = page.getByRole("button", { name: "Which vehicles are at risk right now?" }); await clickLoc(b); await sleep(11000);
  await move(640, 300, 30); await page.mouse.wheel(0, 200); await sleep(3000);
});
await seg("s10", async () => { await page.evaluate(() => { location.hash = "dashboard"; }); await move(640, 380, 40); await sleep(6000); await clickLoc(page.getByRole("button", { name: "Chennai", exact: true })); });
writeFileSync("/tmp/vid/marks.json", JSON.stringify(marks, null, 1));
const v = page.video(); await ctx.close(); console.log("video", await v.path()); await browser.close();
