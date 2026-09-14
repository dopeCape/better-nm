import { QueryClientProvider } from "@tanstack/react-query";
import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it } from "vitest";
import type { SecretRequest } from "@/api/types";
import { useUI } from "@/state/ui";
import { createMockShell, resetUI, testQueryClient } from "@/test/mockShell";
import { startEventRouting } from "./events";
import { SecretPrompt } from "./SecretPrompt";

const request: SecretRequest = {
  id: "c707d89e",
  connection_uuid: "8694ab34",
  connection_name: "ALHN-F832-5",
  ssid: "ALHN-F832-5",
  vpn: false,
  setting_name: "802-11-wireless-security",
  fields: [{ key: "psk", label: "Wi-Fi password", secret: true }],
  request_new: true,
  user_requested: true,
  created_at: new Date().toISOString(),
  expires_at: new Date(Date.now() + 120_000).toISOString(),
};

describe("secret prompt", () => {
  let shell: ReturnType<typeof createMockShell>;
  beforeEach(() => {
    resetUI();
    shell = createMockShell({
      "GET /v1/secrets/c707d89e": { body: request },
      "GET /v1/secrets": { body: [] },
      "POST /v1/secrets/c707d89e": { status: 200 },
      "POST /v1/secrets/c707d89e/cancel": { status: 200 },
    });
  });

  it("opens on secret-needed after window_show, submits {secrets, save}, closes on secret-resolved", async () => {
    const qc = testQueryClient();
    const off = await startEventRouting(qc);
    render(
      <QueryClientProvider client={qc}>
        <SecretPrompt />
      </QueryClientProvider>,
    );
    act(() => {
      shell.emit("bnm://event", { time: new Date().toISOString(), type: "secret-needed", title: "Password needed for ALHN-F832-5", data: { request_id: "c707d89e" } });
    });
    const dialog = await screen.findByRole("dialog", { name: "Wi-Fi password" });
    expect(dialog).toBeInTheDocument();
    const showIdx = shell.calls.findIndex((c) => c.cmd === "window_show");
    const getIdx = shell.calls.findIndex((c) => c.cmd === "api_request" && c.args.path === "/v1/secrets/c707d89e");
    expect(showIdx).toBeGreaterThanOrEqual(0);
    expect(showIdx).toBeLessThan(getIdx);
    expect(screen.getByText("The saved password was rejected")).toBeInTheDocument();
    expect(screen.getByText(/Expires in/)).toBeInTheDocument();

    const input = screen.getByLabelText("Password");
    expect(input).toHaveFocus();
    await userEvent.type(input, "correct-horse");
    await userEvent.click(screen.getByRole("switch", { name: "Save in the profile" }));
    await userEvent.click(screen.getByRole("button", { name: /^Connect/ }));
    await waitFor(() => expect(shell.requests.find((r) => r.method === "POST" && r.path === "/v1/secrets/c707d89e")).toBeTruthy());
    expect(shell.requests.find((r) => r.method === "POST" && r.path === "/v1/secrets/c707d89e")!.body).toEqual({ secrets: { psk: "correct-horse" }, save: false });
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());

    // A second prompt closes on the matching secret-resolved.
    act(() => useUI.getState().setSecret({ ...request, id: "second" }));
    expect(await screen.findByRole("dialog")).toBeInTheDocument();
    act(() => shell.emit("bnm://event", { time: "", type: "secret-resolved", title: "", data: { request_id: "second", outcome: "cancelled" } }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    off();
  });

  it("cancels on Esc through the daemon", async () => {
    const qc = testQueryClient();
    render(
      <QueryClientProvider client={qc}>
        <SecretPrompt />
      </QueryClientProvider>,
    );
    act(() => useUI.getState().setSecret(request));
    await screen.findByRole("dialog");
    await userEvent.keyboard("{Escape}");
    await waitFor(() => expect(shell.requests.find((r) => r.path === "/v1/secrets/c707d89e/cancel")).toBeTruthy());
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  });
});
