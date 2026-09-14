import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
// Design system, lifted as-is from desktop/design (scripts/sync-design.mjs).
import "./design/tokens.css";
import "./design/presets/dark.css";
import "./design/base.css";
import "./design/sprite.js";
import "./styles/app.css";
import { App } from "./app/App";
import { DEFAULT_CONFIG, isSection } from "./config/types";
import { initShell } from "./shell";
import { useUI } from "./state/ui";
import { applyConfig } from "./theme/apply";

// Paint the default theme before the first render so nothing flashes.
applyConfig(DEFAULT_CONFIG);

async function boot() {
  await initShell();
  const q = new URLSearchParams(location.search);
  const start = q.get("section");
  if (isSection(start)) useUI.getState().setSection(start);
  if (q.get("motion") === "0") document.documentElement.dataset.motion = "0";
  // Dev only: lets the screenshot driver and a browser console poke the UI store.
  if (import.meta.env.DEV) (window as unknown as { __bnm: typeof useUI }).__bnm = useUI;
  createRoot(document.getElementById("root")!).render(
    <StrictMode>
      <App />
    </StrictMode>,
  );
}

void boot();
