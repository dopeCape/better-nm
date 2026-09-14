// UI state that outlives a view: the section, overlays, toasts, stream health,
// the pending secret prompt, the speed test and the desktop config.
import { create } from "zustand";
import type { Event, SecretRequest, SpeedProgress, SpeedResult } from "@/api/types";
import { DEFAULT_CONFIG, type DesktopConfig, type Section } from "@/config/types";
import type { StreamState } from "@/shell";

export type ToastTone = "ok" | "warn" | "error" | "accent" | "plain";

export interface Toast {
  id: number;
  tone: ToastTone;
  title: string;
  detail?: string;
}

export type SpeedPhase = "latency" | "download" | "upload" | "done";

export interface SpeedRun {
  state: "idle" | "running" | "result" | "error";
  phase: SpeedPhase;
  mbps: number;
  percent: number;
  latencyMs?: number;
  result?: SpeedResult;
  error?: string;
  quick: boolean;
}

interface UIState {
  section: Section;
  setSection: (s: Section) => void;

  paletteOpen: boolean;
  setPaletteOpen: (open: boolean) => void;

  toasts: Toast[];
  toast: (t: Omit<Toast, "id">) => number;
  dismissToast: (id: number) => void;

  stream: StreamState;
  setStream: (s: StreamState) => void;

  secret: SecretRequest | null;
  setSecret: (r: SecretRequest | null) => void;

  liveEvents: Event[];
  pushEvent: (e: Event) => void;

  speed: SpeedRun;
  speedStart: (quick: boolean) => void;
  speedProgress: (p: SpeedProgress) => void;
  speedResult: (r: SpeedResult) => void;
  speedError: (err: string) => void;
  speedReset: () => void;

  config: DesktopConfig;
  setConfig: (c: DesktopConfig) => void;

  wifiSelected: string | null;
  setWifiSelected: (ssid: string | null) => void;
  wifiLastScan: number | null;
  setWifiLastScan: (t: number) => void;
}

let toastSeq = 0;

export const useUI = create<UIState>((set) => ({
  section: "overview",
  setSection: (section) => set({ section }),

  paletteOpen: false,
  setPaletteOpen: (paletteOpen) => set({ paletteOpen }),

  toasts: [],
  toast: (t) => {
    const id = ++toastSeq;
    set((s) => ({ toasts: [...s.toasts.slice(-3), { ...t, id }] }));
    return id;
  },
  dismissToast: (id) => set((s) => ({ toasts: s.toasts.filter((t) => t.id !== id) })),

  stream: { connected: true },
  setStream: (stream) => set({ stream }),

  secret: null,
  setSecret: (secret) => set({ secret }),

  liveEvents: [],
  pushEvent: (e) => set((s) => ({ liveEvents: [e, ...s.liveEvents].slice(0, 100) })),

  speed: { state: "idle", phase: "latency", mbps: 0, percent: 0, quick: false },
  speedStart: (quick) => set({ speed: { state: "running", phase: "latency", mbps: 0, percent: 0, quick } }),
  speedProgress: (p) =>
    set((s) => {
      const phase = (["latency", "download", "upload", "done"].includes(p.phase) ? p.phase : s.speed.phase) as SpeedPhase;
      const next: SpeedRun = { ...s.speed, state: "running", phase, mbps: p.mbps, percent: p.percent };
      if (phase === "latency" && p.mbps > 0) next.latencyMs = p.mbps; // the latency phase reports ms in mbps
      return { speed: next };
    }),
  speedResult: (r) => set((s) => ({ speed: { ...s.speed, state: "result", phase: "done", percent: 100, result: r, latencyMs: r.latency_ms } })),
  speedError: (error) => set((s) => ({ speed: { ...s.speed, state: "error", error } })),
  speedReset: () => set({ speed: { state: "idle", phase: "latency", mbps: 0, percent: 0, quick: false } }),

  config: DEFAULT_CONFIG,
  setConfig: (config) => set({ config }),

  wifiSelected: null,
  setWifiSelected: (wifiSelected) => set({ wifiSelected }),
  wifiLastScan: null,
  setWifiLastScan: (wifiLastScan) => set({ wifiLastScan }),
}));

/** Convenience for non-React code. */
export const ui = {
  toast: (t: Omit<Toast, "id">) => useUI.getState().toast(t),
  error: (title: string, detail?: string) => useUI.getState().toast(detail ? { tone: "error", title, detail } : { tone: "error", title }),
};
