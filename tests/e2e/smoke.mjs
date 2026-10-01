// Real browser journey against the running stack: Keycloak sign-in (auth code + PKCE) -> dashboard -> vehicles
// -> alerts -> ack. Usage: node smoke.mjs <user> <password> [outDir]
import { chromium } from "playwright-core";
const [user, pass, out = "shots"] = process.argv.slice(2);
const base = process.env.BASE_URL ?? "http://localhost:8081";
const browser = await chromium.launch({ executablePath: process.env.CHROMIUM ?? "/opt/pw-browsers/chromium-1194/chrome-linux/chrome", args: ["--no-sandbox"] });
const page = await browser.newPage({ viewport: { width: 1360, height: 860 } });
const log = (m) => console.log(m);
const errors = [];
page.on("pageerror", (e) => errors.push(String(e)));
page.on("console", (m) => m.type() === "error" && errors.push(m.text()));
await page.goto(base);
await page.getByRole("button", { name: "Sign in" }).click();
await page.fill("#username", user);
await page.fill("#password", pass);
await page.click("#kc-login");
await page.waitForSelector("nav .brand", { timeout: 20000 });
log("signed in via Keycloak: " + (await page.textContent(".who")));
await page.waitForTimeout(6000);
await page.screenshot({ path: `${out}/1-dashboard.png` });
log("dashboard cards: " + (await page.$$eval(".card .big", (els) => els.map((e) => e.textContent).join(" | "))));
await page.click('nav a[href="#vehicles"]');
await page.waitForSelector("table.grid tbody tr", { timeout: 15000 });
await page.click("table.grid tbody tr");
await page.waitForSelector(".drawer h3");
await page.waitForTimeout(1500);
await page.screenshot({ path: `${out}/2-vehicle.png` });
log("vehicle rows: " + (await page.$$eval("table.grid tbody tr", (r) => r.length)));
await page.click('nav a[href="#alerts"]');
await page.waitForTimeout(2500);
const rows = await page.$$("table.grid tbody tr");
log("alert rows: " + rows.length);
if (rows.length) {
  await rows[0].click();
  await page.waitForSelector(".drawer h3");
  await page.waitForTimeout(800);
  await page.screenshot({ path: `${out}/3-alert.png` });
}
log("console/page errors: " + JSON.stringify(errors));
await browser.close();
