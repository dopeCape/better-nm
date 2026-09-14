// A Shell for tests: canned routes, a call log, and `emit` to fire bnm://* events.
import { QueryClient } from "@tanstack/react-query";
import { DEFAULT_CONFIG, type DesktopConfig } from "@/config/types";
import { setShell, type Shell, type ShellEventName, type ShellEvents } from "@/shell";
import { Emitter, type ApiResponse } from "@/shell/types";
import { useUI } from "@/state/ui";

export type Route = { status?: number; body?: unknown } | ((body: unknown) => { status?: number; body?: unknown });

export interface MockShell extends Shell {
  calls: { cmd: string; args: Record<string, unknown> }[];
  requests: { method: string; path: string; body?: unknown }[];
  routes: Record<string, Route>;
  emit<K extends ShellEventName>(event: K, payload: ShellEvents[K]): void;
  config: DesktopConfig;
  /** What `tray_present` answers. */
  trayPresent: boolean;
}

export function createMockShell(routes: Record<string, Route> = {}): MockShell {
  const em = new Emitter();
  const shell: MockShell = {
    kind: "mock",
    calls: [],
    requests: [],
    routes,
    config: { ...DEFAULT_CONFIG },
    trayPresent: false,
    emit: (event, payload) => em.emit(event, payload),
    async invoke<T>(cmd: string, args: Record<string, unknown> = {}): Promise<T> {
      shell.calls.push({ cmd, args });
      switch (cmd) {
        case "api_request": {
          const method = String(args.method);
          const path = String(args.path);
          shell.requests.push({ method, path, body: args.body });
          const key = `${method} ${path}`;
          const r = shell.routes[key] ?? shell.routes[`${method} ${path.split("?")[0]}`];
          if (!r) return { status: 404, body: JSON.stringify({ error: `no mock for ${key}`, code: "not-found" }) } as T;
          const out = typeof r === "function" ? r(args.body) : r;
          const status = out.status ?? 200;
          return { status, body: out.body === undefined ? (status === 204 ? "" : JSON.stringify({ ok: true })) : JSON.stringify(out.body) } as unknown as T;
        }
        case "config_get":
          return shell.config as T;
        case "config_set":
          shell.config = { ...shell.config, ...(args.patch as Partial<DesktopConfig>) };
          queueMicrotask(() => em.emit("bnm://config", shell.config));
          return undefined as T;
        case "config_path":
          return "/home/test/.config/bnmdesktop/config.yaml" as T;
        case "daemon_status":
          return { running: true, socket: "/run/user/1000/bnm/bnmd.sock", unit_installed: false, unit_active: false } as T;
        case "pick_vpn_file":
          return null as T;
        case "tray_present":
          return shell.trayPresent as T;
        default:
          return undefined as T;
      }
    },
    async listen(event, handler) {
      return em.on(event, handler);
    },
  };
  setShell(shell);
  return shell;
}

export function testQueryClient(): QueryClient {
  return new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0, staleTime: 0 }, mutations: { retry: false } } });
}

export function resetUI(): void {
  useUI.setState({ section: "overview", paletteOpen: false, toasts: [], stream: { connected: true }, secret: null, secretQueue: [], trayPresent: false, liveEvents: [], wifiSelected: null, wifiLastScan: null, config: { ...DEFAULT_CONFIG } });
  useUI.getState().speedReset();
}

/** The daemon's 4xx shape. */
export function apiError(status: number, error: string, hint?: string, code?: string): Route {
  return { status, body: { error, ...(hint ? { hint } : {}), ...(code ? { code } : {}) } };
}

export type { ApiResponse };
