// Startup wiring (CONTRACT.md "Frontend routing of events"): subscribe once to
// bnm://change, bnm://event, bnm://stream, bnm://config, bnm://speed; then start
// the stream and load the desktop config.
import type { QueryClient } from "@tanstack/react-query";
import { api, describeError } from "@/api/client";
import { invalidateFor, qk } from "@/api/queries";
import type { SecretRequest } from "@/api/types";
import { withDefaults } from "@/config/types";
import { getShell, shell, type Unlisten } from "@/shell";
import { ui, useUI } from "@/state/ui";
import { applyConfig } from "@/theme/apply";

let lastWarning = "";
let firstConfig = true;

export function applyAndStore(raw: unknown): void {
  const cfg = withDefaults(raw);
  const st = useUI.getState();
  st.setConfig(cfg);
  // The configured start section applies once, unless the URL picked one.
  if (firstConfig) {
    firstConfig = false;
    if (!new URLSearchParams(location.search).get("section")) st.setSection(cfg.start_section);
  }
  const res = applyConfig(cfg);
  if (res.warning && res.warning !== lastWarning) {
    lastWarning = res.warning;
    ui.toast({ tone: "warn", title: res.warning, detail: "Pick another theme in Settings." });
  }
  if (cfg.errors?.length) ui.toast({ tone: "warn", title: "Some desktop config keys were ignored", detail: cfg.errors.join("; ") });
}

async function openSecret(id: string): Promise<void> {
  try {
    await shell.windowShow();
  } catch {
    /* browser mode */
  }
  try {
    const req = await api.get<SecretRequest>(`/v1/secrets/${id}`);
    useUI.getState().setSecret(req);
  } catch (e) {
    const d = describeError(e);
    ui.error(`Password prompt: ${d.title}`, d.detail);
  }
}

export async function startEventRouting(qc: QueryClient): Promise<Unlisten> {
  const s = getShell();
  const st = useUI.getState();
  const offs: Unlisten[] = [];

  offs.push(
    await s.listen("bnm://change", (c) => {
      invalidateFor(qc, c.kind);
    }),
  );
  offs.push(
    await s.listen("bnm://event", (ev) => {
      useUI.getState().pushEvent(ev);
      void qc.invalidateQueries({ queryKey: qk.events });
      if (ev.type === "secret-needed" && ev.data?.request_id) void openSecret(ev.data.request_id);
      if (ev.type === "secret-resolved") {
        const cur = useUI.getState().secret;
        if (cur && cur.id === ev.data?.request_id) useUI.getState().setSecret(null);
        void qc.invalidateQueries({ queryKey: qk.secrets });
      }
    }),
  );
  offs.push(
    await s.listen("bnm://stream", (state) => {
      const prev = useUI.getState().stream;
      useUI.getState().setStream(state);
      // Back after an outage: everything we show may be stale.
      if (state.connected && !prev.connected) void qc.invalidateQueries();
    }),
  );
  offs.push(await s.listen("bnm://config", (cfg) => applyAndStore(cfg)));
  offs.push(
    await s.listen("bnm://speed", (ev) => {
      const u = useUI.getState();
      if (ev.phase === "progress") u.speedProgress(ev.data);
      else if (ev.phase === "result") {
        u.speedResult(ev.data);
        void qc.invalidateQueries({ queryKey: qk.speedHistory });
      } else u.speedError(ev.error);
    }),
  );

  try {
    applyAndStore(await shell.configGet());
  } catch (e) {
    const d = describeError(e);
    ui.error(`Desktop config: ${d.title}`, d.detail);
  }
  try {
    await shell.streamStart();
  } catch (e) {
    st.setStream({ connected: false, error: String(e) });
  }
  // A prompt may already be waiting from before the window opened.
  try {
    const pending = await api.get<SecretRequest[]>("/v1/secrets");
    const first = pending[0];
    if (first && !useUI.getState().secret) useUI.getState().setSecret(first);
  } catch {
    /* 501 without a secret agent, or unreachable: the banner covers it */
  }

  return () => offs.forEach((off) => off());
}
