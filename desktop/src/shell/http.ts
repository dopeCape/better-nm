// Browser dev shell: implements the CONTRACT.md commands against the Vite dev
// server, which proxies /api/* to bnmd's Unix socket (vite.config.ts). Desktop
// config lives in localStorage with the same defaults as the YAML file. Used only
// when `import.meta.env.DEV && !window.__TAURI_INTERNALS__`.
import type { SpeedOptions, SpeedProgress, SpeedResult, Status } from "@/api/types";
import { DEFAULT_CONFIG, withDefaults, type DesktopConfig, type DesktopConfigPatch } from "@/config/types";
import { DAEMON_UNREACHABLE, Emitter, type ApiResponse, type DaemonStatus, type PickedFile, type Shell, type ShellEventName, type ShellEvents } from "./types";

const STORAGE_KEY = "bnmdesktop.config";
const API_BASE = "/api";

function readConfig(): DesktopConfig {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    return { ...withDefaults(raw ? JSON.parse(raw) : {}), source_path: `localStorage:${STORAGE_KEY}`, errors: [] };
  } catch {
    return { ...DEFAULT_CONFIG, source_path: `localStorage:${STORAGE_KEY}`, errors: ["localStorage unreadable"] };
  }
}

function writeConfig(cfg: DesktopConfig): void {
  const { source_path: _s, errors: _e, ...file } = cfg;
  localStorage.setItem(STORAGE_KEY, JSON.stringify(file));
}

/** Parses a text/event-stream body chunk by chunk; calls onEvent(name, data). */
async function readSSE(body: ReadableStream<Uint8Array>, onEvent: (event: string, data: string) => void, signal?: AbortSignal): Promise<void> {
  const reader = body.getReader();
  const dec = new TextDecoder();
  let buf = "";
  for (;;) {
    if (signal?.aborted) break;
    const { value, done } = await reader.read();
    if (done) break;
    buf += dec.decode(value, { stream: true });
    let i: number;
    while ((i = buf.indexOf("\n\n")) >= 0) {
      const block = buf.slice(0, i);
      buf = buf.slice(i + 2);
      let name = "message";
      const data: string[] = [];
      for (const line of block.split("\n")) {
        if (line.startsWith(":")) continue;
        if (line.startsWith("event:")) name = line.slice(6).trim();
        else if (line.startsWith("data:")) data.push(line.slice(5).trimStart());
      }
      if (data.length) onEvent(name, data.join("\n"));
    }
  }
}

export function createHttpShell(): Shell {
  const em = new Emitter();
  let stream: EventSource | null = null;
  let streamAttempt = 0;
  let speedAbort: AbortController | null = null;

  const commands: Record<string, (args: Record<string, unknown>) => Promise<unknown>> = {
    async api_request(args): Promise<ApiResponse> {
      const method = String(args.method ?? "GET");
      const path = String(args.path);
      let res: Response;
      try {
        res = await fetch(API_BASE + path, {
          method,
          headers: args.body !== undefined ? { "content-type": "application/json" } : {},
          body: args.body !== undefined ? JSON.stringify(args.body) : undefined,
        });
      } catch (e) {
        throw `${DAEMON_UNREACHABLE}: ${e instanceof Error ? e.message : String(e)}`;
      }
      const body = await res.text();
      if (res.status === 503 && body.includes(`"${DAEMON_UNREACHABLE}`)) {
        try {
          throw String((JSON.parse(body) as { error: string }).error);
        } catch (e) {
          if (typeof e === "string") throw e;
        }
      }
      return { status: res.status, body };
    },

    async stream_start() {
      if (stream) return;
      const es = new EventSource(`${API_BASE}/v1/events/stream`);
      stream = es;
      es.onopen = () => {
        streamAttempt = 0;
        em.emit("bnm://stream", { connected: true });
      };
      es.onerror = () => {
        streamAttempt += 1;
        em.emit("bnm://stream", { connected: false, error: "event stream closed", attempt: streamAttempt });
      };
      es.addEventListener("change", (e) => em.emit("bnm://change", JSON.parse((e as MessageEvent).data)));
      es.addEventListener("event", (e) => em.emit("bnm://event", JSON.parse((e as MessageEvent).data)));
    },

    async stream_stop() {
      stream?.close();
      stream = null;
    },

    async speed_start(args) {
      if (speedAbort) throw "speed-running";
      const opts = (args.opts ?? {}) as SpeedOptions;
      const ac = new AbortController();
      speedAbort = ac;
      (async () => {
        try {
          const res = await fetch(`${API_BASE}/v1/speed`, {
            method: "POST",
            headers: { "content-type": "application/json", accept: "text/event-stream" },
            body: JSON.stringify(opts),
            signal: ac.signal,
          });
          if (!res.ok || !res.body) {
            const text = await res.text();
            let msg = text || `HTTP ${res.status}`;
            try {
              msg = (JSON.parse(text) as { error?: string }).error ?? msg;
            } catch {
              /* plain text */
            }
            em.emit("bnm://speed", { phase: "error", error: msg });
            return;
          }
          await readSSE(
            res.body,
            (name, data) => {
              if (name === "progress") em.emit("bnm://speed", { phase: "progress", data: JSON.parse(data) as SpeedProgress });
              else if (name === "result") em.emit("bnm://speed", { phase: "result", data: JSON.parse(data) as SpeedResult });
              else if (name === "error") {
                let msg = data;
                try {
                  msg = (JSON.parse(data) as { error?: string }).error ?? data;
                } catch {
                  /* plain text */
                }
                em.emit("bnm://speed", { phase: "error", error: msg });
              }
            },
            ac.signal,
          );
        } catch (e) {
          if (!ac.signal.aborted) em.emit("bnm://speed", { phase: "error", error: e instanceof Error ? e.message : String(e) });
        } finally {
          if (speedAbort === ac) speedAbort = null;
        }
      })();
    },

    async speed_cancel() {
      speedAbort?.abort();
      speedAbort = null;
    },

    async config_get(): Promise<DesktopConfig> {
      const cfg = readConfig();
      queueMicrotask(() => em.emit("bnm://config", cfg));
      return cfg;
    },

    async config_set(args) {
      const patch = (args.patch ?? {}) as DesktopConfigPatch;
      const cur = readConfig();
      const next: DesktopConfig = { ...cur, ...patch, custom: patch.custom ? { ...patch.custom } : cur.custom };
      writeConfig(next);
      queueMicrotask(() => em.emit("bnm://config", readConfig()));
    },

    async config_path() {
      return `localStorage:${STORAGE_KEY}`;
    },

    async pick_vpn_file(): Promise<PickedFile | null> {
      return new Promise((resolve) => {
        const input = document.createElement("input");
        input.type = "file";
        input.accept = ".conf,.ovpn";
        input.style.display = "none";
        document.body.appendChild(input);
        const done = (v: PickedFile | null) => {
          input.remove();
          resolve(v);
        };
        input.addEventListener("change", async () => {
          const f = input.files?.[0];
          if (!f) return done(null);
          if (f.size > 1024 * 1024) return done(null);
          done({ name: f.name, content: await f.text() });
        });
        input.addEventListener("cancel", () => done(null));
        input.click();
      });
    },

    async open_url(args) {
      window.open(String(args.url), "_blank", "noopener");
    },

    async daemon_status(): Promise<DaemonStatus> {
      try {
        const r = (await commands.api_request!({ method: "GET", path: "/v1/status" })) as ApiResponse;
        if (r.status !== 200) return { running: false, socket: "(vite proxy)", unit_installed: false, unit_active: false };
        const s = JSON.parse(r.body) as Status;
        return { running: true, socket: "(vite proxy)", version: s.version, unit_installed: false, unit_active: false };
      } catch {
        return { running: false, socket: "(vite proxy)", unit_installed: false, unit_active: false };
      }
    },

    async daemon_install() {
      throw "not available in the browser; run: bnm daemon install";
    },

    async daemon_restart() {
      throw "not available in the browser; run: bnm daemon restart";
    },

    async window_show() {
      window.focus();
    },

    async notify(args) {
      console.info("bnm notify:", args.title, args.body);
    },
  };

  return {
    kind: "http",
    async invoke<T>(cmd: string, args: Record<string, unknown> = {}): Promise<T> {
      const fn = commands[cmd];
      if (!fn) throw `unknown command: ${cmd}`;
      return (await fn(args)) as T;
    },
    async listen<K extends ShellEventName>(event: K, handler: (payload: ShellEvents[K]) => void) {
      return em.on(event, handler);
    },
  };
}
