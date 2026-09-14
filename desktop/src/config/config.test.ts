import { beforeEach, describe, expect, it } from "vitest";
import { createHttpShell } from "@/shell/http";
import { DEFAULT_CONFIG, withDefaults } from "./types";

describe("desktop config", () => {
  it("merges defaults over a partial object and drops invalid values", () => {
    const c = withDefaults({ theme: "nord", density: "huge", close_to_tray: "yes", custom: { base: "#000" }, errors: ["x"] });
    expect(c.theme).toBe("nord");
    expect(c.density).toBe("default");
    expect(c.close_to_tray).toBe(true);
    expect(c.custom).toEqual({ base: "#000" });
    expect(c.errors).toEqual(["x"]);
    expect(withDefaults(null)).toEqual(DEFAULT_CONFIG);
  });

  describe("browser shell round trip", () => {
    beforeEach(() => localStorage.clear());

    it("config_set merges a patch, persists it, and emits bnm://config", async () => {
      const shell = createHttpShell();
      const seen: unknown[] = [];
      await shell.listen("bnm://config", (c) => seen.push(c));
      const first = await shell.invoke<{ theme: string; source_path: string }>("config_get");
      expect(first.theme).toBe("dark");
      expect(first.source_path).toContain("localStorage");
      await shell.invoke("config_set", { patch: { theme: "catppuccin-latte", accent: "#1e66f5", custom: { base: "#eff1f5" } } });
      await new Promise((r) => setTimeout(r, 0));
      const again = await shell.invoke<{ theme: string; accent: string; custom: { base: string }; close_to_tray: boolean }>("config_get");
      expect(again.theme).toBe("catppuccin-latte");
      expect(again.accent).toBe("#1e66f5");
      expect(again.custom).toEqual({ base: "#eff1f5" });
      expect(again.close_to_tray).toBe(true);
      expect(JSON.parse(localStorage.getItem("bnmdesktop.config")!).theme).toBe("catppuccin-latte");
      await new Promise((r) => setTimeout(r, 0));
      expect(seen.length).toBeGreaterThanOrEqual(2);
      expect((seen.at(-1) as { theme: string }).theme).toBe("catppuccin-latte");
    });
  });
});
