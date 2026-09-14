import { describe, expect, it } from "vitest";
import { DEFAULT_CONFIG, type DesktopConfig } from "@/config/types";
import { applyConfig, resolveTheme } from "./apply";
import { PRESETS, TOKEN_NAMES } from "./presets";

const cfg = (over: Partial<DesktopConfig> = {}): DesktopConfig => ({ ...DEFAULT_CONFIG, ...over });

describe("theme engine", () => {
  it("ships the ten presets from the design", () => {
    expect(PRESETS.map((p) => p.id)).toEqual(["dark", "light", "catppuccin-mocha", "catppuccin-latte", "gruvbox-dark", "nord", "tokyo-night", "rose-pine", "dracula", "one-dark"]);
    for (const p of PRESETS) for (const t of TOKEN_NAMES) expect(p.tokens[t]).toMatch(/^#[0-9a-f]{6}([0-9a-f]{2})?$/);
  });

  it.each(PRESETS.map((p) => [p.id, p] as const))("applies %s to :root", (id, preset) => {
    const root = document.createElement("html");
    const res = applyConfig(cfg({ theme: id }), { root });
    expect(res.warning).toBeUndefined();
    expect(root.dataset.theme).toBe(id);
    expect(root.style.getPropertyValue("--accent")).toBe(preset.tokens.accent);
    expect(root.style.getPropertyValue("--base")).toBe(preset.tokens.base);
    expect(root.style.colorScheme).toBe(preset.scheme);
  });

  it("sets color-scheme light for the light presets", () => {
    const light = PRESETS.filter((p) => p.scheme === "light").map((p) => p.id);
    expect(light).toEqual(expect.arrayContaining(["light", "catppuccin-latte"]));
    for (const id of light) {
      const root = document.createElement("html");
      applyConfig(cfg({ theme: id }), { root });
      expect(root.style.colorScheme).toBe("light");
    }
  });

  it("overrides the accent and derives the selection from it", () => {
    const t = resolveTheme({ theme: "nord", accent: "#ABCDEF", custom: {} });
    expect(t.tokens.accent).toBe("#abcdef");
    expect(t.tokens.selection).toBe("#abcdef26");
    expect(t.tokens.base).toBe(PRESETS.find((p) => p.id === "nord")!.tokens.base);
  });

  it("ignores a malformed accent", () => {
    const t = resolveTheme({ theme: "dark", accent: "blue", custom: {} });
    expect(t.tokens.accent).toBe(PRESETS[0]!.tokens.accent);
  });

  it("fills a custom palette from dark for every omitted token", () => {
    const t = resolveTheme({ theme: "custom", accent: "", custom: { base: "#ffffff", accent_text: "#000000" } });
    expect(t.id).toBe("custom");
    expect(t.tokens.base).toBe("#ffffff");
    expect(t.tokens["accent-text"]).toBe("#000000");
    expect(t.tokens.mantle).toBe(PRESETS[0]!.tokens.mantle);
    expect(t.scheme).toBe("light");
  });

  it("falls back to dark with a warning for an unknown preset", () => {
    const root = document.createElement("html");
    const res = applyConfig(cfg({ theme: "solarized" }), { root });
    expect(res.id).toBe("dark");
    expect(res.warning).toMatch(/solarized/);
    expect(root.dataset.theme).toBe("dark");
  });

  it("maps density, motion and fonts to the attributes tokens.css reads", () => {
    const root = document.createElement("html");
    applyConfig(cfg({ density: "compact", reduced_motion: true, font: "Inter", mono_font: "JetBrainsMono Nerd Font" }), { root });
    expect(root.dataset.density).toBe("compact");
    expect(root.dataset.motion).toBe("0");
    expect(root.dataset.font).toBe("inter");
    expect(root.style.getPropertyValue("--font-mono")).toContain('"JetBrainsMono Nerd Font"');
    applyConfig(cfg({ font: "Cantarell" }), { root });
    expect(root.dataset.density).toBeUndefined();
    expect(root.dataset.motion).toBeUndefined();
    expect(root.dataset.font).toBeUndefined();
    expect(root.style.getPropertyValue("--font-ui")).toContain('"Cantarell"');
    expect(root.style.getPropertyValue("--font-mono")).toBe("");
  });
});
