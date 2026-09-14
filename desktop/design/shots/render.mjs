#!/usr/bin/env node
// Renders the mockups to PNG over the Chrome DevTools protocol (no npm deps; Node >= 22).
// Deterministic where `--screenshot` is not: sets the viewport explicitly, waits for
// webfonts and the preset stylesheet, then captures.
//   node shots/render.mjs                       every page and state, dark, 1440x900 + 1120x720
//   node shots/render.mjs --theme nord --quick  one preset, 1440x900 only
//   node shots/render.mjs --pages "01-overview.html 02-wifi.html"
import { spawn, execSync } from "node:child_process";
import { mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const root = resolve(here, "..");
const args = process.argv.slice(2);
const opt = (k, d) => { const i = args.indexOf(k); return i >= 0 ? args[i + 1] : d; };
const theme = opt("--theme", "dark");
const quick = args.includes("--quick");
const sizes = quick ? [[1440, 900]] : [[1440, 900], [1120, 720]];
const pages = (opt("--pages", "") || `01-overview.html 02-wifi.html 02-wifi.html?state=empty 02-wifi.html?state=error
  03-vpn.html 03-vpn.html?state=needs-setup 04-quality.html 04-quality.html?state=learning 04-quality.html?state=degraded
  05-speed.html 05-speed.html?state=running 05-speed.html?state=result 06-secret-prompt.html 07-settings.html
  07-settings.html?state=palette 08-command-palette.html`).split(/\s+/).filter(Boolean);

const browser = process.env.BROWSER || ["google-chrome", "chromium", "chromium-browser"].find((b) => { try { execSync(`command -v ${b}`, { stdio: "ignore" }); return true; } catch { return false; } });
if (!browser) { console.error("no chrome/chromium on PATH; try: nix shell nixpkgs#chromium --command node shots/render.mjs"); process.exit(1); }

const profile = mkdtempSync(join(tmpdir(), "bnm-shots-"));
const port = 9222 + Math.floor(Math.random() * 1000);
const chrome = spawn(browser, [`--headless=new`, `--no-sandbox`, `--disable-gpu`, `--hide-scrollbars`, `--remote-debugging-port=${port}`, `--user-data-dir=${profile}`, `--window-size=1440,900`, "about:blank"], { stdio: "ignore" });
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
let ws, id = 0; const pending = new Map();
async function connect() {
  for (let i = 0; i < 50; i++) {
    try {
      const list = await (await fetch(`http://127.0.0.1:${port}/json`)).json();
      const page = list.find((t) => t.type === "page");
      if (page) { ws = new WebSocket(page.webSocketDebuggerUrl); await new Promise((r, j) => { ws.onopen = r; ws.onerror = j; }); break; }
    } catch { }
    await sleep(200);
  }
  if (!ws) throw new Error("could not connect to chrome");
  ws.onmessage = (m) => { const d = JSON.parse(m.data); if (d.id && pending.has(d.id)) { pending.get(d.id)(d); pending.delete(d.id); } };
}
const send = (method, params = {}) => new Promise((r) => { const i = ++id; pending.set(i, r); ws.send(JSON.stringify({ id: i, method, params })); });

await connect();
await send("Page.enable"); await send("Runtime.enable");
for (const [w, h] of sizes) {
  await send("Emulation.setDeviceMetricsOverride", { width: w, height: h, deviceScaleFactor: 1, mobile: false });
  for (const p of pages) {
    const [file, q] = p.split("?");
    const url = `file://${root}/${file}?${q ? q + "&" : ""}theme=${theme}&chrome=0&motion=0`;
    await send("Page.navigate", { url });
    // wait for load + fonts + the preset stylesheet, then two frames
    for (let i = 0; i < 100; i++) {
      const r = await send("Runtime.evaluate", { expression: `document.readyState === "complete" && document.fonts.status === "loaded" && !!getComputedStyle(document.documentElement).getPropertyValue("--accent").trim() && document.getElementById("preset").sheet !== null`, returnByValue: true });
      if (r.result?.result?.value) break; await sleep(50);
    }
    await send("Runtime.evaluate", { expression: "new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r)))", awaitPromise: true });
    await sleep(150);
    const shot = await send("Page.captureScreenshot", { format: "png", captureBeyondViewport: false });
    const state = q && q.includes("state=") ? "-" + q.replace(/.*state=([^&]+).*/, "$1") : "";
    const out = join(here, `${file.replace(".html", "")}${state}-${theme}-${w}.png`);
    writeFileSync(out, Buffer.from(shot.result.data, "base64"));
    console.log(out.replace(root + "/", ""));
  }
}
ws.close(); chrome.kill();
