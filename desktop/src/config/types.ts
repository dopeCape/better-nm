// DesktopConfig: the YAML at $XDG_CONFIG_HOME/bnmdesktop/config.yaml as the shell
// serialises it (snake_case keys). This file is the source of truth for the
// frontend; desktop/src-tauri/src/config.rs must produce the same JSON.

export type Density = "compact" | "default" | "comfortable";
export type Section = "overview" | "wifi" | "vpn" | "quality" | "speed" | "devices" | "settings";

export interface CustomTokens {
  base?: string;
  mantle?: string;
  surface?: string;
  overlay?: string;
  text?: string;
  subtext?: string;
  muted?: string;
  accent?: string;
  accent_text?: string;
  ok?: string;
  warn?: string;
  error?: string;
  vpn?: string;
  wifi?: string;
  border?: string;
  selection?: string;
}

export interface DesktopConfig {
  theme: string;
  accent: string;
  font: string;
  mono_font: string;
  density: Density;
  sidebar: "left" | "hidden";
  start_section: Section;
  tray: "auto" | "on" | "off";
  close_to_tray: boolean;
  reduced_motion: boolean;
  custom: CustomTokens;
  base16: string;
  import: string;
  /** Set by config_get: where the file lives and which keys failed to parse. */
  source_path?: string;
  errors?: string[];
}

export type DesktopConfigPatch = Partial<Omit<DesktopConfig, "source_path" | "errors">>;

export const DEFAULT_CONFIG: DesktopConfig = {
  theme: "dark",
  accent: "",
  font: "",
  mono_font: "",
  density: "default",
  sidebar: "left",
  start_section: "overview",
  tray: "auto",
  close_to_tray: true,
  reduced_motion: false,
  custom: {},
  base16: "",
  import: "",
};

export const SECTIONS: readonly Section[] = ["overview", "wifi", "vpn", "quality", "speed", "devices", "settings"];

export function isSection(s: unknown): s is Section {
  return typeof s === "string" && (SECTIONS as readonly string[]).includes(s);
}

/** Merges defaults over a partial object from the shell or localStorage. */
export function withDefaults(raw: unknown): DesktopConfig {
  const r = (raw && typeof raw === "object" ? raw : {}) as Record<string, unknown>;
  const pick = <K extends keyof DesktopConfig>(k: K, ok: (v: unknown) => v is DesktopConfig[K]): DesktopConfig[K] =>
    ok(r[k]) ? (r[k] as DesktopConfig[K]) : DEFAULT_CONFIG[k];
  const str = (v: unknown): v is string => typeof v === "string";
  const bool = (v: unknown): v is boolean => typeof v === "boolean";
  const oneOf =
    <T extends string>(...xs: T[]) =>
    (v: unknown): v is T =>
      typeof v === "string" && (xs as string[]).includes(v);
  return {
    theme: pick("theme", str),
    accent: pick("accent", str),
    font: pick("font", str),
    mono_font: pick("mono_font", str),
    density: pick("density", oneOf("compact", "default", "comfortable")),
    sidebar: pick("sidebar", oneOf("left", "hidden")),
    start_section: pick("start_section", isSection),
    tray: pick("tray", oneOf("auto", "on", "off")),
    close_to_tray: pick("close_to_tray", bool),
    reduced_motion: pick("reduced_motion", bool),
    custom: r.custom && typeof r.custom === "object" ? (r.custom as CustomTokens) : {},
    base16: pick("base16", str),
    import: pick("import", str),
    ...(str(r.source_path) ? { source_path: r.source_path } : {}),
    ...(Array.isArray(r.errors) ? { errors: r.errors.filter(str) } : {}),
  };
}
