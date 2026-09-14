// The shell contract (desktop/CONTRACT.md): every OS-touching thing goes through
// `invoke` commands and `bnm://*` events. The frontend never opens the socket.
import type { Change, Event, SpeedProgress, SpeedResult } from "@/api/types";
import type { DesktopConfig } from "@/config/types";

export interface ApiResponse {
  status: number;
  /** Raw JSON text from bnmd; "" for 204. */
  body: string;
}

export interface StreamState {
  connected: boolean;
  error?: string;
  attempt?: number;
}

export type SpeedEvent =
  | { phase: "progress"; data: SpeedProgress }
  | { phase: "result"; data: SpeedResult }
  | { phase: "error"; error: string };

export interface DaemonStatus {
  running: boolean;
  socket: string;
  pid?: number;
  version?: string;
  unit_installed: boolean;
  unit_active: boolean;
}

export interface PickedFile {
  name: string;
  content: string;
}

export interface ShellEvents {
  "bnm://change": Change;
  "bnm://event": Event;
  "bnm://stream": StreamState;
  "bnm://speed": SpeedEvent;
  "bnm://config": DesktopConfig;
}

export type ShellEventName = keyof ShellEvents;
export type Unlisten = () => void;

export interface Shell {
  readonly kind: "tauri" | "http" | "mock";
  invoke<T = void>(cmd: string, args?: Record<string, unknown>): Promise<T>;
  listen<K extends ShellEventName>(event: K, handler: (payload: ShellEvents[K]) => void): Promise<Unlisten>;
}

/** Transport failures reject with this prefix (CONTRACT.md). */
export const DAEMON_UNREACHABLE = "daemon-unreachable";
export const API_MISMATCH = "api-mismatch";

export function isUnreachable(err: unknown): boolean {
  const s = typeof err === "string" ? err : err instanceof Error ? err.message : "";
  return s.startsWith(DAEMON_UNREACHABLE) || s.startsWith(API_MISMATCH);
}

/** Small synchronous pub/sub the non-Tauri shells use to fan events out. */
export class Emitter {
  private handlers = new Map<string, Set<(p: unknown) => void>>();
  on<K extends ShellEventName>(event: K, handler: (payload: ShellEvents[K]) => void): Unlisten {
    let set = this.handlers.get(event);
    if (!set) this.handlers.set(event, (set = new Set()));
    const h = handler as (p: unknown) => void;
    set.add(h);
    return () => {
      set?.delete(h);
    };
  }
  emit<K extends ShellEventName>(event: K, payload: ShellEvents[K]): void {
    this.handlers.get(event)?.forEach((h) => {
      try {
        h(payload);
      } catch (e) {
        console.error(`bnm: handler for ${event} threw`, e);
      }
    });
  }
}
