import { QueryClientProvider } from "@tanstack/react-query";
import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it } from "vitest";
import type { VPN } from "@/api/types";
import { useUI } from "@/state/ui";
import { createMockShell, resetUI, testQueryClient } from "@/test/mockShell";
import { fuzzy } from "@/lib/fuzzy";
import { filterItems, Palette, type PaletteItem } from "./Palette";

const item = (label: string, group: PaletteItem["group"] = "Actions"): PaletteItem => ({ id: label, group, icon: "gauge", label, run: () => {} });

describe("palette matching", () => {
  it("fuzzy matches subsequences and prefers word starts", () => {
    expect(fuzzy("ta", "Turn Tailscale off")).not.toBeNull();
    expect(fuzzy("ta", "Settings")).toBeNull();
    const a = fuzzy("rs", "Run speed test")!.score;
    const b = fuzzy("rs", "Reset baseline")!.score;
    expect(a).toBeGreaterThan(b);
  });

  it("filters and ranks items, keeping everything on an empty query", () => {
    const items = [item("Turn Tailscale off"), item("Run speed test"), item("VPN", "Go to"), item("TALKTALK-8C3F1", "Networks")];
    expect(filterItems(items, "").map((r) => r.item.label)).toEqual(items.map((i) => i.label));
    const hits = filterItems(items, "ta").map((r) => r.item.label);
    expect(hits).toContain("Turn Tailscale off");
    expect(hits).toContain("TALKTALK-8C3F1");
    expect(hits).not.toContain("VPN");
    expect(filterItems(items, "ta")[0]!.positions.length).toBe(2);
  });
});

describe("palette UI", () => {
  let shell: ReturnType<typeof createMockShell>;
  const tejas: VPN = { id: "abc", name: "tejas", backend: "nm-vpn", kind: "OpenVPN", state: "disconnected", writable: true };
  beforeEach(() => {
    resetUI();
    shell = createMockShell({
      "GET /v1/status": { body: { wifi_enabled: true, connectivity: "full", nm_state: "connected-global", networking: true, wifi_hardware: true, nm_version: "1.56", version: "x", api_version: 1, uptime_seconds: 1, started: "", snapshot_version: 1 } },
      "GET /v1/vpn": { body: [tejas] },
      "GET /v1/wifi": { body: [] },
      "GET /v1/monitor": { body: { network_key: "", state: "ok", anchors: [], interval: 3e10, paused: false } },
      "POST /v1/vpn/abc/connect": { status: 200 },
    });
  });

  it("opens, filters as you type and runs the selected action on Enter", async () => {
    render(
      <QueryClientProvider client={testQueryClient()}>
        <Palette />
      </QueryClientProvider>,
    );
    act(() => useUI.getState().setPaletteOpen(true));
    const input = await screen.findByRole("combobox", { name: "Search" });
    expect(input).toHaveFocus();
    const byText = (t: string) => screen.getAllByRole("option").find((o) => o.textContent?.startsWith(t));
    await waitFor(() => expect(byText("Turn tejas on")).toBeTruthy());
    await userEvent.type(input, "tejas");
    await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(1));
    expect(byText("Turn tejas on")).toHaveAttribute("aria-selected", "true");
    await userEvent.keyboard("{Enter}");
    await waitFor(() => expect(shell.requests.find((r) => r.path === "/v1/vpn/abc/connect")).toBeTruthy());
    expect(useUI.getState().paletteOpen).toBe(false);
  });

  it("offers Quit bnm always and Hide to tray only with a tray, opening the config folder through the shell", async () => {
    render(
      <QueryClientProvider client={testQueryClient()}>
        <Palette />
      </QueryClientProvider>,
    );
    act(() => useUI.getState().setPaletteOpen(true));
    let input = await screen.findByRole("combobox", { name: "Search" });
    const names = () => screen.getAllByRole("option").map((o) => o.textContent ?? "");
    await waitFor(() => expect(names().some((n) => n.startsWith("Quit bnm"))).toBe(true));
    expect(names().some((n) => n.startsWith("Hide to tray"))).toBe(false);
    await userEvent.type(input, "open desktop config");
    await userEvent.keyboard("{Enter}");
    await waitFor(() => expect(shell.calls.some((c) => c.cmd === "config_reveal")).toBe(true));
    expect(shell.calls.some((c) => c.cmd === "open_url")).toBe(false);

    act(() => useUI.getState().setTrayPresent(true));
    act(() => useUI.getState().setPaletteOpen(true));
    input = await screen.findByRole("combobox", { name: "Search" });
    await waitFor(() => expect(names().some((n) => n.startsWith("Hide to tray"))).toBe(true));
    await userEvent.type(input, "hide to tray");
    await userEvent.keyboard("{Enter}");
    await waitFor(() => expect(shell.calls.some((c) => c.cmd === "window_hide")).toBe(true));

    act(() => useUI.getState().setPaletteOpen(true));
    input = await screen.findByRole("combobox", { name: "Search" });
    await userEvent.type(input, "quit bnm");
    await userEvent.keyboard("{Enter}");
    await waitFor(() => expect(shell.calls.some((c) => c.cmd === "window_close")).toBe(true));
    expect(useUI.getState().paletteOpen).toBe(false);
  });

  it("navigates with the arrow keys, goes to a section, and closes on Esc", async () => {
    render(
      <QueryClientProvider client={testQueryClient()}>
        <Palette />
      </QueryClientProvider>,
    );
    act(() => useUI.getState().setPaletteOpen(true));
    const input = await screen.findByRole("combobox", { name: "Search" });
    await userEvent.type(input, "quality");
    await waitFor(() => expect(screen.getAllByRole("option").find((o) => o.textContent?.startsWith("Quality"))).toHaveAttribute("aria-selected", "true"));
    await userEvent.keyboard("{Enter}");
    expect(useUI.getState().section).toBe("quality");
    act(() => useUI.getState().setPaletteOpen(true));
    await screen.findByRole("combobox", { name: "Search" });
    await userEvent.keyboard("{Escape}");
    await waitFor(() => expect(useUI.getState().paletteOpen).toBe(false));
  });
});
