import { act, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { CHANGE_TARGETS, qk } from "@/api/queries";
import type { ChangeKind, SecretRequest, SpeedResult } from "@/api/types";
import { useUI } from "@/state/ui";
import { createMockShell, resetUI, testQueryClient } from "@/test/mockShell";
import { resetEventRouting, startEventRouting } from "./events";

const request = (id: string): SecretRequest => ({
  id,
  connection_uuid: "u",
  connection_name: "ALHN-F832-5",
  ssid: "ALHN-F832-5",
  vpn: false,
  setting_name: "802-11-wireless-security",
  fields: [{ key: "psk", label: "Wi-Fi password", secret: true }],
  request_new: false,
  user_requested: true,
  created_at: new Date().toISOString(),
  expires_at: new Date(Date.now() + 120_000).toISOString(),
});

const result: SpeedResult = { time: "t", network_key: "wifi:x", provider: "cloudflare", download_mbps: 21, upload_mbps: 9, latency_ms: 12, jitter_ms: 1, bytes_moved: 1, duration: 1, quick: false } as unknown as SpeedResult;

const tick = () => new Promise((r) => setTimeout(r, 0));

describe("event routing", () => {
  let shell: ReturnType<typeof createMockShell>;
  beforeEach(() => {
    resetUI();
    resetEventRouting();
    shell = createMockShell({
      "GET /v1/secrets": { body: [] },
      "GET /v1/secrets/a": { body: request("a") },
      "GET /v1/secrets/b": { body: request("b") },
    });
  });

  it("invalidates the right queries for every change kind", async () => {
    const qc = testQueryClient();
    const spy = vi.spyOn(qc, "invalidateQueries");
    const off = startEventRouting(qc);
    await tick();
    for (const kind of Object.keys(CHANGE_TARGETS) as ChangeKind[]) {
      spy.mockClear();
      act(() => shell.emit("bnm://change", { kind }));
      const keys = spy.mock.calls.map((c) => JSON.stringify(c[0]?.queryKey));
      for (const target of CHANGE_TARGETS[kind]) expect(keys).toContain(JSON.stringify(target));
    }
    // The pairs the views depend on.
    expect(CHANGE_TARGETS.active).toContainEqual(qk.wifi);
    expect(CHANGE_TARGETS.profiles).toContainEqual(qk.devices);
    expect(CHANGE_TARGETS.monitor).toContainEqual(["monitor", "samples"]);
    off();
  });

  it("stopping before the listeners resolve leaves no handler behind (StrictMode)", async () => {
    const qc = testQueryClient();
    const spy = vi.spyOn(qc, "invalidateQueries");
    const off = startEventRouting(qc);
    off(); // the effect cleanup runs before any `listen` promise settles
    await tick();
    await tick();
    act(() => shell.emit("bnm://change", { kind: "wifi" }));
    act(() => shell.emit("bnm://event", { time: "", type: "connected", title: "x" }));
    expect(spy).not.toHaveBeenCalled();
    expect(useUI.getState().liveEvents).toHaveLength(0);
    // A second start works as the first one would have.
    const off2 = startEventRouting(qc);
    await tick();
    act(() => shell.emit("bnm://change", { kind: "wifi" }));
    expect(spy).toHaveBeenCalled();
    off2();
  });

  it("applies the same config twice without a second toast", async () => {
    const qc = testQueryClient();
    shell.config = { ...shell.config, theme: "nope", errors: ["unknown key `bogus`"] };
    const off = startEventRouting(qc);
    await waitFor(() => expect(useUI.getState().config.theme).toBe("nope"));
    // config_get returned once and the shell emits the same config again (the contract's "two identical events").
    act(() => shell.emit("bnm://config", shell.config));
    act(() => shell.emit("bnm://config", shell.config));
    const toasts = useUI.getState().toasts;
    expect(toasts.filter((t) => t.title.startsWith("Unknown theme"))).toHaveLength(1);
    expect(toasts.filter((t) => t.title.startsWith("Some desktop config keys"))).toHaveLength(1);
    expect(document.documentElement.dataset.theme).toBe("dark");
    // A changed error list toasts again.
    act(() => shell.emit("bnm://config", { ...shell.config, errors: ["`density`: \"huge\""] }));
    expect(useUI.getState().toasts.filter((t) => t.title.startsWith("Some desktop config keys"))).toHaveLength(2);
    off();
  });

  it("queues a second secret-needed and drops a resolved request wherever it sits", async () => {
    const qc = testQueryClient();
    const off = startEventRouting(qc);
    await tick();
    act(() => shell.emit("bnm://event", { time: "", type: "secret-needed", title: "", data: { request_id: "a" } }));
    await waitFor(() => expect(useUI.getState().secret?.id).toBe("a"));
    act(() => shell.emit("bnm://event", { time: "", type: "secret-needed", title: "", data: { request_id: "b" } }));
    await waitFor(() => expect(useUI.getState().secretQueue.map((r) => r.id)).toEqual(["a", "b"]));
    expect(useUI.getState().secret?.id).toBe("a"); // the first prompt stays on screen
    // The queued one resolves elsewhere (CLI answered it): gone from the queue, the shown one untouched.
    act(() => shell.emit("bnm://event", { time: "", type: "secret-resolved", title: "", data: { request_id: "b", outcome: "answered" } }));
    expect(useUI.getState().secretQueue.map((r) => r.id)).toEqual(["a"]);
    expect(useUI.getState().secret?.id).toBe("a");
    // The shown one resolves: nothing left.
    act(() => shell.emit("bnm://event", { time: "", type: "secret-resolved", title: "", data: { request_id: "a", outcome: "cancelled" } }));
    expect(useUI.getState().secret).toBeNull();
    // A resolved id we never saw is a no-op; the same request twice enqueues once.
    act(() => shell.emit("bnm://event", { time: "", type: "secret-resolved", title: "", data: { request_id: "zzz" } }));
    act(() => useUI.getState().setSecret(request("c")));
    act(() => useUI.getState().setSecret(request("c")));
    expect(useUI.getState().secretQueue).toHaveLength(1);
    // Dismissing the head shows the next.
    act(() => useUI.getState().setSecret(request("d")));
    act(() => useUI.getState().setSecret(null));
    expect(useUI.getState().secret?.id).toBe("d");
    off();
  });

  it("routes speed events only into a running test; cancelled resets instead of erroring", async () => {
    const qc = testQueryClient();
    const off = startEventRouting(qc);
    await tick();
    const u = () => useUI.getState();
    // Idle: a late progress frame or a late "cancelled" (the shell emits it after speed_cancel resolves) is ignored.
    act(() => shell.emit("bnm://speed", { phase: "progress", data: { phase: "download", mbps: 5, percent: 10, bytes: 1 } }));
    act(() => shell.emit("bnm://speed", { phase: "error", error: "cancelled" }));
    expect(u().speed.state).toBe("idle");
    // Running: progress lands, cancel goes back to idle rather than showing "cancelled" as an error.
    act(() => u().speedStart(false));
    act(() => shell.emit("bnm://speed", { phase: "progress", data: { phase: "download", mbps: 5, percent: 10, bytes: 1 } }));
    expect(u().speed.mbps).toBe(5);
    act(() => shell.emit("bnm://speed", { phase: "error", error: "cancelled" }));
    expect(u().speed.state).toBe("idle");
    // A real error shows.
    act(() => u().speedStart(true));
    act(() => shell.emit("bnm://speed", { phase: "error", error: "no route to host" }));
    expect(u().speed).toMatchObject({ state: "error", error: "no route to host" });
    // A result finishes the run and refreshes the history.
    const spy = vi.spyOn(qc, "invalidateQueries");
    act(() => u().speedStart(false));
    act(() => shell.emit("bnm://speed", { phase: "result", data: result }));
    expect(u().speed.state).toBe("result");
    expect(spy.mock.calls.some((c) => JSON.stringify(c[0]?.queryKey) === JSON.stringify(qk.speedHistory))).toBe(true);
    off();
  });

  it("asks the shell for the tray after every config and refetches everything when the stream comes back", async () => {
    const qc = testQueryClient();
    shell.trayPresent = true;
    const off = startEventRouting(qc);
    await waitFor(() => expect(useUI.getState().trayPresent).toBe(true));
    shell.trayPresent = false;
    act(() => shell.emit("bnm://config", shell.config));
    await waitFor(() => expect(useUI.getState().trayPresent).toBe(false));

    const spy = vi.spyOn(qc, "invalidateQueries");
    act(() => shell.emit("bnm://stream", { connected: false, error: "gone", attempt: 2 }));
    expect(useUI.getState().stream).toEqual({ connected: false, error: "gone", attempt: 2 });
    expect(spy).not.toHaveBeenCalled();
    act(() => shell.emit("bnm://stream", { connected: true }));
    expect(spy).toHaveBeenCalledWith();
    off();
  });
});
