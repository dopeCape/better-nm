// Theme engine: turns a DesktopConfig into CSS variables on :root plus the
// data-theme / data-density / data-motion / data-font attributes tokens.css and
// base.css read. Presets come from presets.ts (generated from the design).
import type { CustomTokens, DesktopConfig } from "@/config/types";
import { PRESETS, TOKEN_NAMES, type Preset, type TokenName, type Tokens } from "./presets";

export interface ResolvedTheme {
  /** Preset id the tokens came from, or "custom". */
  id: string;
  scheme: "dark" | "light";
  tokens: Tokens;
  /** Set when the config named a preset that does not exist. */
  warning?: string;
}

const HEX = /^#([0-9a-f]{3}|[0-9a-f]{6}|[0-9a-f]{8})$/i;

export function isHex(v: unknown): v is string {
  return typeof v === "string" && HEX.test(v.trim());
}

/** Expands #rgb to #rrggbb and lowercases; alpha suffix is kept. */
export function normaliseHex(v: string): string {
  const s = v.trim().toLowerCase();
  if (s.length === 4) return "#" + s[1] + s[1] + s[2] + s[2] + s[3] + s[3];
  return s;
}

export function presetById(id: string): Preset | undefined {
  return PRESETS.find((p) => p.id === id);
}

export const DEFAULT_PRESET: Preset = presetById("dark") ?? PRESETS[0]!;

/** Relative luminance of #rrggbb (alpha ignored), 0..1. */
export function luminance(hex: string): number {
  const h = normaliseHex(hex).slice(1, 7);
  const c = [0, 2, 4].map((i) => {
    const v = parseInt(h.slice(i, i + 2), 16) / 255;
    return v <= 0.03928 ? v / 12.92 : Math.pow((v + 0.055) / 1.055, 2.4);
  }) as [number, number, number];
  return 0.2126 * c[0] + 0.7152 * c[1] + 0.0722 * c[2];
}

/** CustomTokens keys are snake_case in YAML; token names use a hyphen. */
function customKey(t: TokenName): keyof CustomTokens {
  return t.replace("-", "_") as keyof CustomTokens;
}

export function resolveTheme(cfg: Pick<DesktopConfig, "theme" | "accent" | "custom">): ResolvedTheme {
  let id = cfg.theme || "dark";
  let base: Preset = DEFAULT_PRESET;
  let warning: string | undefined;
  const tokens = { ...DEFAULT_PRESET.tokens };
  let scheme: "dark" | "light" = "dark";
  let customSelection = false;

  if (id === "custom") {
    // Any token omitted falls back to `dark` (CONTRACT.md).
    for (const t of TOKEN_NAMES) {
      const v = cfg.custom?.[customKey(t)];
      if (isHex(v)) tokens[t] = normaliseHex(v);
    }
    customSelection = isHex(cfg.custom?.selection);
    scheme = luminance(tokens.base) > 0.4 ? "light" : "dark";
  } else {
    const p = presetById(id);
    if (!p) {
      warning = `Unknown theme "${id}", using bnm Dark`;
      id = "dark";
    } else base = p;
    Object.assign(tokens, base.tokens);
    scheme = base.scheme;
  }

  if (isHex(cfg.accent)) {
    tokens.accent = normaliseHex(cfg.accent);
    // --selection is the accent at 15 % alpha unless the palette pinned its own.
    if (!customSelection) tokens.selection = tokens.accent.slice(0, 7) + "26";
  }
  return warning ? { id, scheme, tokens, warning } : { id, scheme, tokens };
}

function fontStack(family: string, fallback: string): string {
  const f = family.trim().replace(/"/g, "");
  return `"${f}", ${fallback}`;
}

export interface ApplyOptions {
  root?: HTMLElement;
}

/** Applies the whole config to the document. Returns what was resolved (for toasts). */
export function applyConfig(cfg: DesktopConfig, opts: ApplyOptions = {}): ResolvedTheme {
  const root = opts.root ?? document.documentElement;
  const theme = resolveTheme(cfg);
  for (const t of TOKEN_NAMES) root.style.setProperty(`--${t}`, theme.tokens[t]);
  root.dataset.theme = theme.id;
  root.style.colorScheme = theme.scheme;

  if (cfg.density && cfg.density !== "default") root.dataset.density = cfg.density;
  else delete root.dataset.density;

  if (cfg.reduced_motion) root.dataset.motion = "0";
  else delete root.dataset.motion;

  const font = (cfg.font || "").trim();
  const lower = font.toLowerCase();
  root.style.removeProperty("--font-ui");
  if (!font || lower === "geist") delete root.dataset.font;
  else if (lower === "inter") root.dataset.font = "inter";
  else if (lower === "system" || lower === "system-ui") root.dataset.font = "system";
  else {
    delete root.dataset.font;
    root.style.setProperty("--font-ui", fontStack(font, '"Geist", "Inter", system-ui, sans-serif'));
  }

  const mono = (cfg.mono_font || "").trim();
  if (!mono || mono.toLowerCase() === "jetbrains mono") root.style.removeProperty("--font-mono");
  else root.style.setProperty("--font-mono", fontStack(mono, '"JetBrains Mono", ui-monospace, monospace'));

  return theme;
}

/** Live preview of a palette without touching the config: same as applyConfig for colours only. */
export function previewTokens(tokens: Partial<Tokens>, root: HTMLElement = document.documentElement): void {
  for (const t of TOKEN_NAMES) {
    const v = tokens[t];
    if (isHex(v)) root.style.setProperty(`--${t}`, normaliseHex(v));
  }
  const base = tokens.base;
  if (isHex(base)) root.style.colorScheme = luminance(base) > 0.4 ? "light" : "dark";
}

/** Reads the currently applied token values, for the palette editor's starting point. */
export function currentTokens(root: HTMLElement = document.documentElement): Tokens {
  const cs = getComputedStyle(root);
  const out = { ...DEFAULT_PRESET.tokens };
  for (const t of TOKEN_NAMES) {
    const v = cs.getPropertyValue(`--${t}`).trim();
    if (isHex(v)) out[t] = normaliseHex(v);
  }
  return out;
}
