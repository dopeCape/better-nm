// TypeScript mirrors of internal/core (field names are the Go json tags).
// Times are RFC 3339 strings; Go durations are nanoseconds.

export type DeviceKind = "wifi" | "ethernet" | "tun" | "bridge" | "veth" | "vlan" | "wireguard" | "loopback" | "other";
export type DeviceClass = "physical" | "infra" | "loopback";
export type DeviceState = "unmanaged" | "unavailable" | "disconnected" | "connecting" | "connected" | "external" | "failed";

export interface Device {
  name: string;
  ifindex: number;
  kind: DeviceKind;
  class: DeviceClass;
  state: DeviceState;
  managed: boolean;
  owner?: string;
  hwaddr?: string;
  driver?: string;
  ipv4?: string[];
  ipv6?: string[];
  gateway4?: string;
  dns?: string[];
  active_uuid?: string;
  active_name?: string;
  speed_mbps?: number;
  nm_path?: string;
}

export type WifiSecurity = "open" | "owe" | "wep" | "wpa-psk" | "sae" | "wpa-eap";

export interface WifiNetwork {
  ssid: string;
  device: string;
  strength: number;
  security: WifiSecurity;
  frequency_mhz: number;
  channel: number;
  band: string;
  bssids: string[];
  known: boolean;
  profile_uuid?: string;
  active: boolean;
  hidden?: boolean;
  last_seen?: string;
}

export type IPMethod = "auto" | "manual" | "disabled" | "link-local" | "shared" | "ignore";

export interface IPConfig {
  method: IPMethod;
  addresses?: string[];
  gateway?: string;
  dns?: string[];
  dns_search?: string[];
  ignore_auto_dns?: boolean;
  never_default?: boolean;
}

export type ProfileType = "wifi" | "ethernet" | "wireguard" | "vpn" | "bridge" | "other";

export interface WireGuardPeer {
  public_key: string;
  preshared_key?: string;
  endpoint?: string;
  allowed_ips: string[];
  persistent_keepalive?: number;
}

export interface WireGuardSetting {
  public_key?: string;
  listen_port?: number;
  fwmark?: number;
  mtu?: number;
  peers?: WireGuardPeer[];
}

export interface Profile {
  uuid: string;
  name: string;
  type: ProfileType;
  raw_type: string;
  interface_name?: string;
  autoconnect: boolean;
  ssid?: string;
  security?: WifiSecurity;
  vpn_service_type?: string;
  timestamp?: string;
  ipv4: IPConfig;
  ipv6: IPConfig;
  permissions?: string[];
  filename?: string;
  version_id: number;
  active: boolean;
  nm_path?: string;
  wireguard?: WireGuardSetting;
  vpn_data?: Record<string, string>;
}

export type ActiveState = "activating" | "activated" | "deactivating" | "deactivated" | "unknown";

export interface ActiveConnection {
  path: string;
  profile_uuid: string;
  profile_name: string;
  type: ProfileType;
  devices: string[];
  state: ActiveState;
  default4: boolean;
  default6: boolean;
  vpn: boolean;
  vpn_state?: string;
  vpn_banner?: string;
  ipv4?: string[];
  ipv6?: string[];
  gateway4?: string;
  dns?: string[];
  external: boolean;
}

export type Connectivity = "unknown" | "none" | "portal" | "limited" | "full";

export interface Status {
  nm_state: string;
  connectivity: Connectivity;
  networking: boolean;
  wifi_enabled: boolean;
  wifi_hardware: boolean;
  primary?: ActiveConnection;
  network_key?: string;
  nm_version: string;
  permissions?: Record<string, string>;
  // GET /v1/status extras
  version: string;
  api_version: number;
  uptime_seconds: number;
  started: string;
  snapshot_version: number;
}

export interface ConnectWifiRequest {
  device?: string;
  ssid: string;
  password?: string;
  hidden?: boolean;
  username?: string;
}

// ---- VPN ----

export type VPNBackend = "tailscale" | "wireguard" | "nm-vpn";
export type VPNState = "disconnected" | "connecting" | "connected" | "error" | "needs-setup" | "needs-auth" | "unavailable";

export interface TailscalePeer {
  id: string;
  name: string;
  hostname?: string;
  os?: string;
  ips: string[];
  online: boolean;
  exit_node: boolean;
  exit_node_option: boolean;
  relay?: string;
  last_seen?: string;
}

export interface TailscaleInfo {
  backend_state: string;
  version?: string;
  control_url?: string;
  tailnet?: string;
  magic_dns_suffix?: string;
  self_ips?: string[];
  self_name?: string;
  operator_ok: boolean;
  exit_node_id?: string;
  exit_node_name?: string;
  exit_node_on: boolean;
  exit_node_allow_lan: boolean;
  accept_dns: boolean;
  peers?: TailscalePeer[];
  health?: string[];
}

export interface WireGuardInfo {
  interface_name: string;
  public_key?: string;
  listen_port?: number;
  peers?: WireGuardPeer[];
  addresses?: string[];
  last_handshake?: string;
  rx_bytes?: number;
  tx_bytes?: number;
}

export interface NMVPNInfo {
  service_type: string;
  gateway?: string;
  username?: string;
  banner?: string;
  nm_vpn_state?: string;
}

export interface VPN {
  id: string;
  name: string;
  backend: VPNBackend;
  kind: string;
  state: VPNState;
  detail?: string;
  error?: string;
  auth_url?: string;
  since?: string;
  writable: boolean;
  tailscale?: TailscaleInfo;
  wireguard?: WireGuardInfo;
  nm_vpn?: NMVPNInfo;
}

export interface VPNImportRequest {
  kind: "wireguard" | "openvpn";
  name?: string;
  path?: string;
  content?: string;
}

export interface VPNImportResult {
  id: string;
  uuid: string;
  name: string;
  kind: string;
  vpn?: VPN;
}

// ---- Monitor ----

export interface Sample {
  time: string;
  network_key: string;
  anchor: string;
  anchor_addr?: string;
  rtt_ms: number;
  loss: number;
  dns_ms: number;
  method: string;
}

export type BaselineState = "learning" | "ok" | "degraded" | "idle";

export interface Baseline {
  network_key: string;
  anchor: string;
  state: BaselineState;
  sample_count: number;
  baseline_rtt_ms: number;
  baseline_loss: number;
  current_rtt_ms: number;
  current_loss: number;
  current_dns_ms: number;
  since?: string;
  updated_at: string;
}

export interface MonitorStatus {
  network_key: string;
  state: BaselineState;
  anchors: Baseline[];
  last_sample?: string;
  interval: number; // nanoseconds
  paused: boolean;
}

// ---- Speed ----

export interface SpeedOptions {
  provider?: string;
  server?: string;
  quick?: boolean;
  max_bytes?: number;
  network_key?: string;
}

export interface SpeedResult {
  time: string;
  network_key: string;
  provider: string;
  server?: string;
  download_mbps: number;
  upload_mbps: number;
  latency_ms: number;
  jitter_ms: number;
  bytes_moved: number;
  duration: number; // nanoseconds
  quick: boolean;
}

export interface SpeedProgress {
  phase: "latency" | "download" | "upload" | "done" | string;
  mbps: number;
  percent: number;
  bytes: number;
}

// ---- Events ----

export type EventType =
  | "connected"
  | "disconnected"
  | "no-internet"
  | "internet-restored"
  | "vpn-up"
  | "vpn-down"
  | "degraded"
  | "recovered"
  | "wifi-scan"
  | "state-changed"
  | "secret-needed"
  | "secret-resolved";

export interface Event {
  time: string;
  type: EventType;
  network_key?: string;
  title: string;
  body?: string;
  urgency?: "low" | "normal" | "critical";
  data?: Record<string, string>;
}

export type ChangeKind = "status" | "devices" | "wifi" | "profiles" | "active" | "vpn" | "monitor";

export interface Change {
  kind: ChangeKind;
  path?: string;
}

// ---- Secrets ----

export interface SecretField {
  key: string;
  label: string;
  secret: boolean;
}

export interface SecretRequest {
  id: string;
  connection_uuid: string;
  connection_name: string;
  ssid?: string;
  vpn: boolean;
  vpn_kind?: string;
  setting_name: string;
  fields: SecretField[];
  message?: string;
  request_new: boolean;
  user_requested: boolean;
  created_at: string;
  expires_at: string;
}

export interface SecretAnswer {
  secrets: Record<string, string>;
  save: boolean;
}

// ---- Daemon config (GET/PUT /v1/config) ----

export interface DaemonConfig {
  monitor: { interval: number; anchors: string[]; retention_days: number };
  notify: {
    connected: boolean;
    disconnected: boolean;
    no_internet: boolean;
    internet_restored: boolean;
    vpn_up: boolean;
    vpn_down: boolean;
    degraded: boolean;
    recovered: boolean;
    secret_needed: boolean;
    muted_networks: string[];
  };
  speed: { provider: string; iperf3_server: string; max_bytes: number };
  tailscale: { socket: string };
  daemon: { log_level: string };
}

// ---- Errors ----

export type ApiErrorCode = "permission" | "not-found" | "unsupported" | "invalid" | "conflict" | "unavailable" | "internal";

export interface ApiErrorBody {
  error: string;
  hint?: string;
  code?: ApiErrorCode | string;
}
