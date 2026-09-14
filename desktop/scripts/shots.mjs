#!/usr/bin/env node
// Screenshots the live app (pnpm dev, real daemon) over the Chrome DevTools
// protocol, the way desktop/design/shots/render.mjs shoots the mockups.
//   node scripts/shots.mjs                          dark, 1440x900, every section
//   node scripts/shots.mjs --theme catppuccin-latte --sizes 1440x900,1120x720,900x600
//   node scripts/shots.mjs --pages overview,wifi,palette
// Read-only against the system: it never connects, forgets or toggles anything.
import { spawn, execSync } from "node:child_process";
import { mkdirSync, mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const outDir = resolve(here, "..", "shots");
mkdirSync(outDir, { recursive: true });
const args = process.argv.slice(2);
const opt = (k, d) => {
  const i = args.indexOf(k);
  return i >= 0 ? args[i + 1] : d;
};
const theme = opt("--theme", "dark");
const base = opt("--url", "http://127.0.0.1:5173");
const sizes = opt("--sizes", "1440x900")
  .split(",")
  .map((s) => s.split("x").map(Number));
const density = opt("--density", "default");
const pages = opt("--pages", "overview,wifi,vpn,quality,speed,devices,settings,settings-palette,palette,secret,unreachable").split(",");

const browser = process.env.BROWSER || ["google-chrome", "chromium", "chromium-browser", "google-chrome-stable"].find((b) => {
  try {
    execSync(`command -v ${b}`, { stdio: "ignore" });
    return true;
  } catch {
    return false;
  }
});
if (!browser) {
  console.error("no chrome/chromium on PATH; try: nix shell nixpkgs#chromium --command node scripts/shots.mjs");
  process.exit(1);
}

const profile = mkdtempSync(join(tmpdir(), "bnm-app-shots-"));
const port = 9222 + Math.floor(Math.random() * 1000);
const chrome = spawn(browser, [`--headless=new`, `--no-sandbox`, `--disable-gpu`, `--hide-scrollbars`, `--remote-debugging-port=${port}`, `--user-data-dir=${profile}`, `--window-size=1440,900`, "about:blank"], { stdio: "ignore" });
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
let ws,
  id = 0;
const pending = new Map();
async function connect() {
  for (let i = 0; i < 50; i++) {
    try {
      const list = await (await fetch(`http://127.0.0.1:${port}/json`)).json();
      const page = list.find((t) => t.type === "page");
      if (page) {
        ws = new WebSocket(page.webSocketDebuggerUrl);
        await new Promise((r, j) => {
          ws.onopen = r;
          ws.onerror = j;
        });
        break;
      }
    } catch {
      /* not up yet */
    }
    await sleep(200);
  }
  if (!ws) throw new Error("could not connect to chrome");
  ws.onmessage = (m) => {
    const d = JSON.parse(m.data);
    if (d.id && pending.has(d.id)) {
      pending.get(d.id)(d);
      pending.delete(d.id);
    }
  };
}
const send = (method, params = {}) =>
  new Promise((r) => {
    const i = ++id;
    pending.set(i, r);
    ws.send(JSON.stringify({ id: i, method, params }));
  });
const evaluate = async (expression, awaitPromise = false) => (await send("Runtime.evaluate", { expression, returnByValue: true, awaitPromise })).result?.result?.value;

await connect();
await send("Page.enable");
await send("Runtime.enable");
// Desktop config lives in localStorage in browser mode: seed the theme before the app boots.
const cfg = JSON.stringify({ theme, density, reduced_motion: true });
await send("Page.addScriptToEvaluateOnNewDocument", { source: `try { localStorage.setItem("bnmdesktop.config", ${JSON.stringify(cfg)}); } catch {}` });

async function settle() {
  for (let i = 0; i < 120; i++) {
    const ok = await evaluate(`document.readyState === "complete" && document.fonts.status === "loaded" && !!document.querySelector(".app") && !document.querySelector("[aria-busy='true'], .skel")`);
    if (ok) break;
    await sleep(100);
  }
  await evaluate("new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r)))", true);
  await sleep(250);
}

async function key(keyName, text, mods = 0) {
  const code = { k: "KeyK", Escape: "Escape" }[keyName] ?? keyName;
  await send("Input.dispatchKeyEvent", { type: "keyDown", key: keyName, code, modifiers: mods, text, windowsVirtualKeyCode: keyName === "k" ? 75 : keyName === "Escape" ? 27 : 0 });
  await send("Input.dispatchKeyEvent", { type: "keyUp", key: keyName, code, modifiers: mods, windowsVirtualKeyCode: keyName === "k" ? 75 : keyName === "Escape" ? 27 : 0 });
}

for (const [w, h] of sizes) {
  await send("Emulation.setDeviceMetricsOverride", { width: w, height: h, deviceScaleFactor: 1, mobile: false });
  for (const p of pages) {
    const section = { "settings-palette": "settings", palette: "overview", secret: "wifi", unreachable: "overview" }[p] ?? p;
    await send("Page.navigate", { url: `${base}/?section=${section}&motion=0` });
    await settle();
    if (p === "settings-palette") {
      await evaluate(`document.querySelector("details summary")?.click()`);
      await settle();
    } else if (p === "palette") {
      await key("k", undefined, 2); // Ctrl K
      await sleep(200);
      await send("Input.insertText", { text: "ta" });
      await settle();
    } else if (p === "secret") {
      await evaluate(`window.__bnm.getState().setSecret({ id: "shot", connection_uuid: "x", connection_name: "ALHN-F832-5", ssid: "ALHN-F832-5", vpn: false, setting_name: "802-11-wireless-security", fields: [{ key: "psk", label: "Wi-Fi password", secret: true }], request_new: true, user_requested: true, created_at: new Date().toISOString(), expires_at: new Date(Date.now() + 102000).toISOString() })`);
      await settle();
      await send("Input.insertText", { text: "correct-horse" });
      await sleep(200);
    } else if (p === "unreachable") {
      await evaluate(`window.__bnm.getState().setStream({ connected: false, error: "connect: no such file or directory", attempt: 3 })`);
      await sleep(200);
    }
    const shot = await send("Page.captureScreenshot", { format: "png", captureBeyondViewport: false });
    const out = join(outDir, `${p}-${theme}${density !== "default" ? "-" + density : ""}-${w}.png`);
    writeFileSync(out, Buffer.from(shot.result.data, "base64"));
    const overflow = await evaluate(`(() => { const c = document.querySelector(".content"); return c ? c.scrollWidth - c.clientWidth : 0; })()`);
    console.log(`${out.replace(resolve(here, "..") + "/", "")}${overflow > 0 ? `  (horizontal overflow ${overflow}px)` : ""}`);
  }
}
ws.close();
chrome.kill();
