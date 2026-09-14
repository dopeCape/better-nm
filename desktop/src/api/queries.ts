// TanStack Query hooks for every route the views read. Invalidation is driven by
// `bnm://change` (see app/events.ts); the refetch intervals are only a safety net.
import { useQuery, type QueryClient } from "@tanstack/react-query";
import { api } from "./client";
import type { ActiveConnection, ChangeKind, DaemonConfig, Device, Event, MonitorStatus, Profile, Sample, SecretRequest, SpeedResult, Status, VPN, WifiNetwork } from "./types";
import { shell } from "@/shell";

export const qk = {
  status: ["status"] as const,
  devices: ["devices"] as const,
  active: ["active"] as const,
  wifi: ["wifi"] as const,
  profiles: ["profiles"] as const,
  profile: (uuid: string) => ["profiles", uuid] as const,
  vpn: ["vpn"] as const,
  monitor: ["monitor"] as const,
  samples: (key: string | undefined, limit: number) => ["monitor", "samples", key ?? "", limit] as const,
  speedHistory: ["speed", "history"] as const,
  events: ["events"] as const,
  daemonConfig: ["config"] as const,
  daemonStatus: ["daemon"] as const,
  secrets: ["secrets"] as const,
};

/**
 * Which queries a `core.Change` kind invalidates. Mirrors the TUI's loadsFor
 * (internal/ui/tui/tui.go): an `active` hint also moves the Wi-Fi list's
 * `active` flag and the devices' addresses; a `profiles` hint changes `known`
 * on networks and the wired profile a device can toggle.
 */
export const CHANGE_TARGETS: Record<ChangeKind, readonly (readonly unknown[])[]> = {
  status: [qk.status],
  devices: [qk.devices, qk.status],
  wifi: [qk.wifi],
  profiles: [qk.profiles, qk.wifi, qk.devices],
  active: [qk.active, qk.status, qk.devices, qk.wifi, qk.vpn],
  vpn: [qk.vpn],
  monitor: [qk.monitor, ["monitor", "samples"]],
};

export function invalidateFor(qc: QueryClient, kind: ChangeKind): void {
  const targets = CHANGE_TARGETS[kind] ?? [qk.status];
  for (const key of targets) void qc.invalidateQueries({ queryKey: key as unknown[] });
}

export function useStatus() {
  return useQuery({ queryKey: qk.status, queryFn: () => api.get<Status>("/v1/status"), refetchInterval: 30_000 });
}
export function useDevices() {
  return useQuery({ queryKey: qk.devices, queryFn: () => api.get<Device[]>("/v1/devices"), refetchInterval: 60_000 });
}
export function useActive() {
  return useQuery({ queryKey: qk.active, queryFn: () => api.get<ActiveConnection[]>("/v1/active") });
}
export function useWifi() {
  return useQuery({ queryKey: qk.wifi, queryFn: () => api.get<WifiNetwork[]>("/v1/wifi"), refetchInterval: 30_000 });
}
export function useProfiles() {
  return useQuery({ queryKey: qk.profiles, queryFn: () => api.get<Profile[]>("/v1/profiles") });
}
export function useProfile(uuid: string | undefined) {
  return useQuery({ queryKey: qk.profile(uuid ?? ""), queryFn: () => api.get<Profile>(`/v1/profiles/${uuid}`), enabled: !!uuid });
}
export function useVpn() {
  return useQuery({ queryKey: qk.vpn, queryFn: () => api.get<VPN[]>("/v1/vpn"), refetchInterval: 30_000 });
}
export function useMonitor() {
  return useQuery({ queryKey: qk.monitor, queryFn: () => api.get<MonitorStatus>("/v1/monitor"), refetchInterval: 30_000 });
}
export function useSamples(limit = 200, key?: string) {
  const q = new URLSearchParams();
  if (key) q.set("key", key);
  q.set("limit", String(limit));
  return useQuery({ queryKey: qk.samples(key, limit), queryFn: () => api.get<Sample[]>(`/v1/monitor/samples?${q}`), refetchInterval: 30_000 });
}
export function useSpeedHistory(limit = 50) {
  return useQuery({ queryKey: qk.speedHistory, queryFn: () => api.get<SpeedResult[]>(`/v1/speed/history?limit=${limit}`) });
}
export function useEvents(limit = 50) {
  return useQuery({ queryKey: qk.events, queryFn: () => api.get<Event[]>(`/v1/events?limit=${limit}`) });
}
export function useDaemonConfig() {
  return useQuery({ queryKey: qk.daemonConfig, queryFn: () => api.get<DaemonConfig>("/v1/config") });
}
export function useDaemonStatus() {
  return useQuery({ queryKey: qk.daemonStatus, queryFn: () => shell.daemonStatus(), refetchInterval: 30_000 });
}
export function usePendingSecrets() {
  return useQuery({ queryKey: qk.secrets, queryFn: () => api.get<SecretRequest[]>("/v1/secrets"), retry: false });
}
