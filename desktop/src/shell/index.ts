// One `Shell` for the whole app. Tauri when running inside the desktop window,
// the HTTP dev shell in a plain browser during `pnpm dev`, a mock in tests.
import type { SpeedOptions } from "@/api/types";
import type { DesktopConfig, DesktopConfigPatch } from "@/config/types";
import type { ApiResponse, DaemonStatus, PickedFile, Shell } from "./types";

export type { Shell, ShellEvents, ShellEventName, ApiResponse, DaemonStatus, PickedFile, SpeedEvent, StreamState, Unlisten } from "./types";
export { isUnreachable, DAEMON_UNREACHABLE, API_MISMATCH } from "./types";

declare global {
  interface Window {
    __TAURI_INTERNALS__?: unknown;
  }
}

let current: Shell | null = null;

export function hasTauri(): boolean {
  return typeof window !== "undefined" && window.__TAURI_INTERNALS__ !== undefined;
}

export function getShell(): Shell {
  if (current) return current;
  throw new Error("bnm: shell not initialised; call initShell() or setShell() first");
}

/** Tests inject a mock; main.tsx picks the real one. */
export function setShell(shell: Shell): void {
  current = shell;
}

export async function initShell(): Promise<Shell> {
  if (current) return current;
  if (!hasTauri() && import.meta.env.DEV) {
    const { createHttpShell } = await import("./http");
    current = createHttpShell();
  } else {
    const { createTauriShell } = await import("./tauri");
    current = createTauriShell();
  }
  return current;
}

// ---- Typed command helpers (CONTRACT.md) ----

export const shell = {
  apiRequest: (method: "GET" | "POST" | "PUT" | "DELETE", path: string, body?: unknown) =>
    getShell().invoke<ApiResponse>("api_request", body === undefined ? { method, path } : { method, path, body }),
  streamStart: () => getShell().invoke("stream_start"),
  streamStop: () => getShell().invoke("stream_stop"),
  speedStart: (opts: SpeedOptions) => getShell().invoke("speed_start", { opts }),
  speedCancel: () => getShell().invoke("speed_cancel"),
  configGet: () => getShell().invoke<DesktopConfig>("config_get"),
  configSet: (patch: DesktopConfigPatch) => getShell().invoke("config_set", { patch }),
  configPath: () => getShell().invoke<string>("config_path"),
  pickVpnFile: () => getShell().invoke<PickedFile | null>("pick_vpn_file"),
  openUrl: (url: string) => getShell().invoke("open_url", { url }),
  daemonStatus: () => getShell().invoke<DaemonStatus>("daemon_status"),
  daemonInstall: () => getShell().invoke("daemon_install"),
  daemonRestart: () => getShell().invoke("daemon_restart"),
  windowShow: () => getShell().invoke("window_show"),
  windowHide: () => getShell().invoke("window_hide"),
  windowClose: () => getShell().invoke("window_close"),
  trayPresent: () => getShell().invoke<boolean>("tray_present"),
  configReveal: () => getShell().invoke("config_reveal"),
  notify: (title: string, body: string) => getShell().invoke("notify", { title, body }),
};
