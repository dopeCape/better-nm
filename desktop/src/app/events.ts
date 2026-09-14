// Startup wiring (CONTRACT.md "Frontend routing of events"): subscribe once to
// bnm://change, bnm://event, bnm://stream, bnm://config, bnm://speed; then start
// the stream and load the desktop config.
import type { QueryClient } from "@tanstack/react-query";
import { api, describeError } from "@/api/client";
import { invalidateFor, qk } from "@/api/queries";
import type { SecretRequest } from "@/api/types";
import { withDefaults } from "@/config/types";
import { getShell, shell, type ShellEventName, type ShellEvents, type Unlisten } from "@/shell";
import { ui, useUI } from "@/state/ui";
import { applyConfig } from "@/theme/apply";

let lastWarning = "";
let lastErrors = "";
let firstConfig = true;

/**
 * Applies a config to the document and the store. Idempotent: the shell sends
 * every config twice (config_set emits, then the watcher emits again), so the
 * toasts fire only when the warning or the error list actually changes.
 */
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
  const errors = (cfg.errors ?? []).join("; ");
  if (errors && errors !== lastErrors) ui.toast({ tone: "warn", title: "Some desktop config keys were ignored", detail: errors });
  lastErrors = errors;
  // `tray` may have changed; the shell knows whether an icon exists now.
  void refreshTrayPresent();
}

/** Tests reset the once-only bits between runs. */
export function resetEventRouting(): void {
  lastWarning = "";
  lastErrors = "";
  firstConfig = true;
}

async function refreshTrayPresent(): Promise<void> {
  try {
    useUI.getState().setTrayPresent(!!(await shell.trayPresent()));
  } catch {
    useUI.getState().setTrayPresent(false);
  }
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

/**
 * Subscribes to the shell's events and kicks off the initial fetches. Returns
 * synchronously with the unsubscribe, so a caller that cleans up before the
 * listeners are registered (React StrictMode mounts effects twice) never leaves
 * a duplicate handler behind: each listener is dropped the moment it resolves
 * if the routing was already stopped.
 */
export function startEventRouting(qc: QueryClient): Unlisten {
  const s = getShell();
  const offs: Unlisten[] = [];
  let stopped = false;

  const on = <K extends ShellEventName>(event: K, handler: (payload: ShellEvents[K]) => void) => {
    void s.listen(event, handler).then((off) => {
      if (stopped) off();
      else offs.push(off);
    });
  };

  on("bnm://change", (c) => {
    invalidateFor(qc, c.kind);
  });
  on("bnm://event", (ev) => {
    const u = useUI.getState();
    u.pushEvent(ev);
    void qc.invalidateQueries({ queryKey: qk.events });
    if (ev.type === "secret-needed" && ev.data?.request_id) void openSecret(ev.data.request_id);
    if (ev.type === "secret-resolved") {
      // Whether it is the one on screen or queued behind it, it is gone.
      if (ev.data?.request_id) u.removeSecret(ev.data.request_id);
      void qc.invalidateQueries({ queryKey: qk.secrets });
    }
  });
  on("bnm://stream", (state) => {
    const prev = useUI.getState().stream;
    useUI.getState().setStream(state);
    // Back after an outage: everything we show may be stale.
    if (state.connected && !prev.connected) void qc.invalidateQueries();
  });
  on("bnm://config", (cfg) => applyAndStore(cfg));
  on("bnm://speed", (ev) => {
    const u = useUI.getState();
    // The shell may still emit a progress frame or the "cancelled" error after
    // the view reset the run (speed_cancel resolves before the event lands);
    // only a running test takes events.
    if (u.speed.state !== "running") return;
    if (ev.phase === "progress") u.speedProgress(ev.data);
    else if (ev.phase === "result") {
      u.speedResult(ev.data);
      void qc.invalidateQueries({ queryKey: qk.speedHistory });
    } else if (ev.error === "cancelled") u.speedReset();
    else u.speedError(ev.error);
  });

  void (async () => {
    try {
      const cfg = await shell.configGet();
      if (!stopped) applyAndStore(cfg);
    } catch (e) {
      const d = describeError(e);
      ui.error(`Desktop config: ${d.title}`, d.detail);
    }
    if (stopped) return;
    try {
      await shell.streamStart();
    } catch (e) {
      useUI.getState().setStream({ connected: false, error: String(e) });
    }
    // Prompts may already be waiting from before the window opened.
    try {
      const pending = await api.get<SecretRequest[]>("/v1/secrets");
      if (stopped) return;
      for (const r of pending ?? []) useUI.getState().setSecret(r);
    } catch {
      /* 501 without a secret agent, or unreachable: the banner covers it */
    }
  })();

  return () => {
    stopped = true;
    offs.splice(0).forEach((off) => off());
  };
}
