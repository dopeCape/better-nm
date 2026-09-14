import { QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it } from "vitest";
import type { Profile, WifiNetwork } from "@/api/types";
import { apiError, createMockShell, resetUI, testQueryClient } from "@/test/mockShell";
import { filterNetworks, sortNetworks } from "./sort";
import { WifiDetail, connectErrorText } from "./WifiDetail";
import { ApiError } from "@/api/client";

const net = (over: Partial<WifiNetwork>): WifiNetwork => ({
  ssid: "x",
  device: "wlan0",
  strength: 50,
  security: "wpa-psk",
  frequency_mhz: 5180,
  channel: 36,
  band: "5",
  bssids: ["aa:bb:cc:dd:ee:ff"],
  known: false,
  active: false,
  ...over,
});

describe("wifi list", () => {
  it("sorts the active network first, then by signal", () => {
    const sorted = sortNetworks([net({ ssid: "weak", strength: 20 }), net({ ssid: "strong", strength: 90 }), net({ ssid: "home", strength: 60, active: true }), net({ ssid: "mid", strength: 55 })]);
    expect(sorted.map((n) => n.ssid)).toEqual(["home", "strong", "mid", "weak"]);
  });

  it("filters by text, saved and band", () => {
    const list = [net({ ssid: "Home5", band: "5", known: true }), net({ ssid: "Home24", band: "2.4", known: true }), net({ ssid: "Cafe", band: "5" })];
    expect(filterNetworks(list, "home", "all").map((n) => n.ssid)).toEqual(["Home5", "Home24"]);
    expect(filterNetworks(list, "", "saved").map((n) => n.ssid)).toEqual(["Home5", "Home24"]);
    expect(filterNetworks(list, "", "5ghz").map((n) => n.ssid)).toEqual(["Home5", "Cafe"]);
  });
});

describe("connect flow", () => {
  let shell: ReturnType<typeof createMockShell>;
  const profile: Profile = { uuid: "u1", name: "Home", type: "wifi", raw_type: "802-11-wireless", autoconnect: true, ssid: "Home", security: "wpa-psk", ipv4: { method: "auto" }, ipv6: { method: "auto" }, version_id: 1, active: false };

  beforeEach(() => {
    resetUI();
    shell = createMockShell({
      "GET /v1/profiles/u1": { body: profile },
      "POST /v1/wifi/connect": { status: 200 },
    });
  });

  const mount = (n: WifiNetwork) => {
    const qc = testQueryClient();
    return render(
      <QueryClientProvider client={qc}>
        <WifiDetail key={n.ssid} network={n} device="wlan0" />
      </QueryClientProvider>,
    );
  };

  it("asks for a password only on a secured unknown network", () => {
    mount(net({ ssid: "Cafe", security: "wpa-psk" }));
    expect(screen.getByLabelText("Wi-Fi password")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Connect/ })).toBeInTheDocument();
  });

  it("has no password field on an open unknown network and connects without one", async () => {
    mount(net({ ssid: "Nyx-Guest", security: "open" }));
    expect(screen.queryByLabelText("Wi-Fi password")).toBeNull();
    await userEvent.click(screen.getByRole("button", { name: /Connect/ }));
    await waitFor(() => expect(shell.requests.find((r) => r.path === "/v1/wifi/connect")).toBeTruthy());
    expect(shell.requests.find((r) => r.path === "/v1/wifi/connect")!.body).toEqual({ ssid: "Nyx-Guest", device: "wlan0" });
  });

  it("refuses to connect a secured unknown network without a password", async () => {
    mount(net({ ssid: "Cafe" }));
    await userEvent.click(screen.getByRole("button", { name: /Connect/ }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Enter the password.");
    expect(shell.requests.find((r) => r.path === "/v1/wifi/connect")).toBeUndefined();
  });

  it("sends the typed password and re-keys a known network when one is typed", async () => {
    mount(net({ ssid: "Home", known: true, profile_uuid: "u1" }));
    await screen.findAllByText(/Saved, autoconnect on/);
    const pw = screen.getByLabelText("Wi-Fi password");
    await userEvent.type(pw, "new-secret{Enter}");
    await waitFor(() => expect(shell.requests.find((r) => r.path === "/v1/wifi/connect")).toBeTruthy());
    expect(shell.requests.find((r) => r.path === "/v1/wifi/connect")!.body).toEqual({ ssid: "Home", device: "wlan0", password: "new-secret" });
  });

  it("connects a known network without a password when none is typed", async () => {
    mount(net({ ssid: "Home", known: true, profile_uuid: "u1" }));
    await userEvent.click(await screen.findByRole("button", { name: /^Connect/ }));
    await waitFor(() => expect(shell.requests.find((r) => r.path === "/v1/wifi/connect")).toBeTruthy());
    expect(shell.requests.find((r) => r.path === "/v1/wifi/connect")!.body).toEqual({ ssid: "Home", device: "wlan0" });
  });

  it("shows the wrong-password line when the daemon rejects the secret", async () => {
    shell.routes["POST /v1/wifi/connect"] = apiError(400, "no secrets available: the password was rejected", undefined, "invalid");
    mount(net({ ssid: "BT-K2NHW9" }));
    await userEvent.type(screen.getByLabelText("Wi-Fi password"), "hunter2hunter{Enter}");
    expect(await screen.findByRole("alert")).toHaveTextContent("Wrong password. The router rejected it; check for a typo.");
    expect(screen.getByLabelText("Wi-Fi password")).toHaveAttribute("aria-invalid", "true");
  });

  it("keeps the daemon's own words for other errors", () => {
    expect(connectErrorText(new ApiError(403, { error: "not permitted", hint: "run: bnm doctor", code: "permission" }))).toBe("not permitted. run: bnm doctor");
  });
});
