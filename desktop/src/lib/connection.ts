// Derived view of "what am I connected to", shared by the rail, top strip and overview.
import { useDevices, useMonitor, useStatus, useVpn, useWifi } from "@/api/queries";
import type { BaselineState, Connectivity, Device, MonitorStatus, VPN, WifiNetwork } from "@/api/types";
import { fmtWhen } from "./format";

export const LEARNING_TARGET = 40; // internal/monitor engine.go MinSamples

export interface ConnectionSummary {
  loading: boolean;
  connected: boolean;
  kind: "wifi" | "ethernet" | "other" | "none";
  name: string;
  /** First IPv4 with prefix, e.g. 192.168.1.73/24 */
  cidr: string;
  device?: Device;
  network?: WifiNetwork;
  connectivity: Connectivity;
  wifiEnabled: boolean;
  wifiHardware: boolean;
  vpns: VPN[];
  vpnUp: VPN[];
  monitor?: MonitorStatus;
}

export function useConnection(): ConnectionSummary {
  const status = useStatus();
  const devices = useDevices();
  const wifi = useWifi();
  const vpn = useVpn();
  const monitor = useMonitor();

  const s = status.data;
  const primary = s?.primary;
  const devName = primary?.devices[0];
  const device = devices.data?.find((d) => d.name === devName);
  const network = wifi.data?.find((n) => n.active);
  const kind: ConnectionSummary["kind"] = !primary ? "none" : primary.type === "wifi" ? "wifi" : primary.type === "ethernet" ? "ethernet" : "other";
  const vpns = vpn.data ?? [];
  return {
    loading: status.isPending,
    connected: !!primary && primary.state === "activated",
    kind,
    name: primary?.profile_name ?? "",
    cidr: primary?.ipv4?.[0] ?? device?.ipv4?.[0] ?? "",
    ...(device ? { device } : {}),
    ...(network ? { network } : {}),
    connectivity: s?.connectivity ?? "unknown",
    wifiEnabled: s?.wifi_enabled ?? true,
    wifiHardware: s?.wifi_hardware ?? true,
    vpns,
    vpnUp: vpns.filter((v) => v.state === "connected"),
    ...(monitor.data ? { monitor: monitor.data } : {}),
  };
}

export interface QualityBadge {
  tone: "ok" | "warn" | "error" | undefined;
  icon: "check" | "spinner-gap" | "warning-circle" | "pause";
  text: string;
  state: BaselineState | "paused" | "unknown";
}

/** The badge text for the top strip and the overview: "Quality ok", "Learning 12/40", "Degraded 14:02". */
export function qualityBadge(m: MonitorStatus | undefined): QualityBadge {
  if (!m) return { tone: undefined, icon: "spinner-gap", text: "Monitor idle", state: "unknown" };
  if (m.paused) return { tone: undefined, icon: "pause", text: "Monitor paused", state: "paused" };
  switch (m.state) {
    case "ok":
      return { tone: "ok", icon: "check", text: "Quality ok", state: "ok" };
    case "learning": {
      const n = Math.min(LEARNING_TARGET, Math.max(...m.anchors.map((a) => a.sample_count), 0));
      return { tone: "warn", icon: "spinner-gap", text: `Learning ${n}/${LEARNING_TARGET}`, state: "learning" };
    }
    case "degraded": {
      const since = m.anchors.find((a) => a.state === "degraded")?.since;
      const t = fmtWhen(since);
      return { tone: "error", icon: "warning-circle", text: t ? `Degraded ${t}` : "Degraded", state: "degraded" };
    }
    default:
      return { tone: undefined, icon: "spinner-gap", text: "Monitor idle", state: "idle" };
  }
}

export function internetLabel(c: Connectivity, connected: boolean): { tone: "ok" | "warn" | undefined; text: string } {
  if (!connected) return { tone: undefined, text: "No internet" };
  switch (c) {
    case "full":
      return { tone: "ok", text: "Internet" };
    case "portal":
      return { tone: "warn", text: "Sign-in needed" };
    case "limited":
      return { tone: "warn", text: "Limited" };
    case "none":
      return { tone: undefined, text: "No internet" };
    default:
      return { tone: undefined, text: "Checking" };
  }
}
