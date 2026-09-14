import { invoke as tauriInvoke } from "@tauri-apps/api/core";
import { listen as tauriListen } from "@tauri-apps/api/event";
import type { Shell, ShellEventName, ShellEvents } from "./types";

/** The real thing: a Tauri 2 window talking to desktop/src-tauri. */
export function createTauriShell(): Shell {
  return {
    kind: "tauri",
    invoke<T>(cmd: string, args?: Record<string, unknown>): Promise<T> {
      return tauriInvoke<T>(cmd, args);
    },
    async listen<K extends ShellEventName>(event: K, handler: (payload: ShellEvents[K]) => void) {
      return tauriListen<ShellEvents[K]>(event, (e) => handler(e.payload));
    },
  };
}
