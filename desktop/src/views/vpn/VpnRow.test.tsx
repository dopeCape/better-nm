import { QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it } from "vitest";
import type { VPN } from "@/api/types";
import { useUI } from "@/state/ui";
import { apiError, createMockShell, resetUI, testQueryClient } from "@/test/mockShell";
import { VpnRow } from "./VpnRow";

const vpn = (over: Partial<VPN>): VPN => ({ id: "4997257f", name: "tejas", backend: "nm-vpn", kind: "OpenVPN", state: "disconnected", writable: true, nm_vpn: { service_type: "org.freedesktop.NetworkManager.openvpn", gateway: "vpn.tj.house:1194" }, ...over });

describe("VpnRow switch", () => {
  let shell: ReturnType<typeof createMockShell>;
  beforeEach(() => {
    resetUI();
    shell = createMockShell({ "POST /v1/vpn/4997257f/connect": { status: 200 }, "POST /v1/vpn/4997257f/disconnect": { status: 200 } });
  });
  const mount = (v: VPN) =>
    render(
      <QueryClientProvider client={testQueryClient()}>
        <VpnRow vpn={v} />
      </QueryClientProvider>,
    );

  it("calls connect when switched on, and disconnect when switched off", async () => {
    const { rerender } = mount(vpn({}));
    const sw = screen.getByRole("switch", { name: "tejas" });
    expect(sw).toHaveAttribute("aria-checked", "false");
    await userEvent.click(sw);
    expect(sw).toHaveAttribute("aria-checked", "true"); // optimistic
    await waitFor(() => expect(shell.requests.at(-1)).toEqual({ method: "POST", path: "/v1/vpn/4997257f/connect", body: {} }));
    rerender(
      <QueryClientProvider client={testQueryClient()}>
        <VpnRow vpn={vpn({ state: "connected" })} />
      </QueryClientProvider>,
    );
    await userEvent.click(screen.getByRole("switch", { name: "tejas" }));
    await waitFor(() => expect(shell.requests.at(-1)?.path).toBe("/v1/vpn/4997257f/disconnect"));
  });

  it("reverts the switch and toasts the hint when the daemon refuses", async () => {
    shell.routes["POST /v1/vpn/4997257f/connect"] = apiError(503, "tailscaled is not running", "Start it: systemctl start tailscaled", "unavailable");
    mount(vpn({}));
    const sw = screen.getByRole("switch", { name: "tejas" });
    await userEvent.click(sw);
    await waitFor(() => expect(sw).toHaveAttribute("aria-checked", "false"));
    const toasts = useUI.getState().toasts;
    expect(toasts).toHaveLength(1);
    expect(toasts[0]!.title).toBe("tejas: tailscaled is not running");
    expect(toasts[0]!.detail).toBe("Start it: systemctl start tailscaled");
  });

  it("disables the switch when bnm cannot drive the backend", () => {
    mount(vpn({ id: "tailscale", name: "Tailscale", backend: "tailscale", kind: "Tailscale", state: "connected", writable: false }));
    expect(screen.getByRole("switch", { name: "Tailscale" })).toHaveAttribute("disabled");
    expect(screen.getByText("Connected")).toBeInTheDocument();
  });

  it("labels each state as text, never colour alone", () => {
    mount(vpn({ state: "needs-setup" }));
    expect(screen.getByText("Needs setup")).toBeInTheDocument();
    expect(screen.getByText("Installed, but bnm may not drive it yet")).toBeInTheDocument();
  });
});
