// Every write the UI performs, as plain functions over the client, plus a
// useAction hook that toasts the daemon's error and hint on failure.
import { useMutation, useQueryClient, type QueryKey } from "@tanstack/react-query";
import { useCallback } from "react";
import { api, describeError } from "./client";
import { qk } from "./queries";
import type { ConnectWifiRequest, DaemonConfig, IPConfig, SecretAnswer, VPNImportRequest, VPNImportResult } from "./types";
import { ui } from "@/state/ui";

export const actions = {
  wifiScan: (device?: string) => api.post("/v1/wifi/scan", device ? { device } : {}),
  wifiEnabled: (on: boolean) => api.post("/v1/wifi/enabled", { on }),
  wifiConnect: (req: ConnectWifiRequest) => api.post("/v1/wifi/connect", req),
  wifiDisconnect: (device?: string) => api.post("/v1/wifi/disconnect", device ? { device } : {}),
  wifiForget: (uuid: string) => api.post("/v1/wifi/forget", { uuid }),

  profileActivate: (uuid: string, device?: string) => api.post(`/v1/profiles/${uuid}/activate`, device ? { device } : {}),
  profileDeactivate: (uuid: string) => api.post(`/v1/profiles/${uuid}/deactivate`),
  profileAutoconnect: (uuid: string, on: boolean) => api.post(`/v1/profiles/${uuid}/autoconnect`, { on }),
  profileIp: (uuid: string, body: { ipv4?: IPConfig; ipv6?: IPConfig }) => api.put(`/v1/profiles/${uuid}/ip`, body),
  profileDelete: (uuid: string) => api.del(`/v1/profiles/${uuid}`),

  vpnConnect: (id: string) => api.post(`/v1/vpn/${encodeURIComponent(id)}/connect`),
  vpnDisconnect: (id: string) => api.post(`/v1/vpn/${encodeURIComponent(id)}/disconnect`),
  vpnImport: (req: VPNImportRequest) => api.post<VPNImportResult>("/v1/vpn/import", req),
  tailscaleExitNode: (peer: string, allow_lan: boolean) => api.post("/v1/vpn/tailscale/exit-node", { peer, allow_lan }),
  tailscaleExitEnabled: (on: boolean) => api.post("/v1/vpn/tailscale/exit-node/enabled", { on }),
  tailscaleLogin: () => api.post<{ url: string }>("/v1/vpn/tailscale/login"),
  tailscaleLogout: () => api.post("/v1/vpn/tailscale/logout"),
  tailscaleAcceptDns: (on: boolean) => api.post("/v1/vpn/tailscale/accept-dns", { on }),

  monitorPause: () => api.post("/v1/monitor/pause"),
  monitorResume: () => api.post("/v1/monitor/resume"),
  monitorReset: (key?: string) => api.post("/v1/monitor/baseline/reset", key ? { key } : {}),

  secretAnswer: (id: string, a: SecretAnswer) => api.post(`/v1/secrets/${id}`, a),
  secretCancel: (id: string) => api.post(`/v1/secrets/${id}/cancel`),

  daemonConfigSet: (key: string, value: string) => api.put<DaemonConfig>("/v1/config", { key, value }),
  notifyTest: () => api.post("/v1/notify/test"),
};

export interface UseActionOptions<TArgs extends unknown[], TResult> {
  /** Query keys to invalidate after success (the stream usually does it first). */
  invalidate?: QueryKey[];
  onSuccess?: (result: TResult, ...args: TArgs) => void;
  onError?: (err: unknown, ...args: TArgs) => void;
  /** Toast prefix, e.g. "Rescan" -> "Rescan: <error>". */
  label?: string;
  /** Skip the automatic error toast (the view shows the error inline). */
  silent?: boolean;
}

export function useAction<TArgs extends unknown[], TResult>(fn: (...args: TArgs) => Promise<TResult>, opts: UseActionOptions<TArgs, TResult> = {}) {
  const qc = useQueryClient();
  const m = useMutation({
    mutationFn: (args: TArgs) => fn(...args),
    onSuccess: (result, args) => {
      for (const key of opts.invalidate ?? []) void qc.invalidateQueries({ queryKey: key });
      opts.onSuccess?.(result, ...args);
    },
    onError: (err, args) => {
      if (!opts.silent) {
        const d = describeError(err);
        ui.error(opts.label ? `${opts.label}: ${d.title}` : d.title, d.detail);
      }
      opts.onError?.(err, ...args);
    },
  });
  const run = useCallback((...args: TArgs) => m.mutateAsync(args).catch(() => undefined), [m]);
  return { run, pending: m.isPending, error: m.error, reset: m.reset, mutateAsync: (...args: TArgs) => m.mutateAsync(args) };
}

export const invalidateKeys = qk;
