// Formatting for machine values. Everything here returns plain strings; the
// caller wraps them in the mono face.
import type { WifiSecurity } from "@/api/types";

export const ZERO_TIME = "0001-01-01T00:00:00Z";

export function isZeroTime(iso: string | undefined): boolean {
  return !iso || iso.startsWith("0001-");
}

const pad = (n: number) => String(n).padStart(2, "0");

export function hhmm(d: Date): string {
  return `${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

function sameDay(a: Date, b: Date): boolean {
  return a.getFullYear() === b.getFullYear() && a.getMonth() === b.getMonth() && a.getDate() === b.getDate();
}

/** "14:31" today, "Yesterday", "Sat" this week, else "12 Mar". */
export function fmtWhen(iso: string | undefined, now = new Date()): string {
  if (isZeroTime(iso)) return "";
  const d = new Date(iso!);
  if (Number.isNaN(d.getTime())) return "";
  if (sameDay(d, now)) return hhmm(d);
  const y = new Date(now);
  y.setDate(now.getDate() - 1);
  if (sameDay(d, y)) return "Yesterday";
  const diff = now.getTime() - d.getTime();
  if (diff < 6 * 86_400_000) return d.toLocaleDateString(undefined, { weekday: "short" });
  return d.toLocaleDateString(undefined, { day: "numeric", month: "short" });
}

/** "Today 14:36", "Sat 18:02", "12 Mar 09:14" for tables. */
export function fmtWhenLong(iso: string | undefined, now = new Date()): string {
  if (isZeroTime(iso)) return "";
  const d = new Date(iso!);
  if (Number.isNaN(d.getTime())) return "";
  const t = hhmm(d);
  if (sameDay(d, now)) return `Today ${t}`;
  const y = new Date(now);
  y.setDate(now.getDate() - 1);
  if (sameDay(d, y)) return `Yesterday ${t}`;
  const diff = now.getTime() - d.getTime();
  if (diff < 6 * 86_400_000) return `${d.toLocaleDateString(undefined, { weekday: "short" })} ${t}`;
  return `${d.toLocaleDateString(undefined, { day: "numeric", month: "short" })} ${t}`;
}

/** ["today at", "09:14"], ["yesterday at", "18:02"], ["on Sat at", "11:40"]; the time goes in the mono face. */
export function fmtWhenSentence(iso: string | undefined, now = new Date()): [string, string] {
  if (isZeroTime(iso)) return ["", ""];
  const d = new Date(iso!);
  if (Number.isNaN(d.getTime())) return ["", ""];
  const t = hhmm(d);
  if (sameDay(d, now)) return ["today at", t];
  const y = new Date(now);
  y.setDate(now.getDate() - 1);
  if (sameDay(d, y)) return ["yesterday at", t];
  const diff = now.getTime() - d.getTime();
  if (diff < 6 * 86_400_000) return [`on ${d.toLocaleDateString(undefined, { weekday: "short" })} at`, t];
  return [`on ${d.toLocaleDateString(undefined, { day: "numeric", month: "short" })} at`, t];
}

/** Compact age: "20 s", "3 min", "2 h", "3 d". */
export function fmtAge(ms: number): string {
  if (!Number.isFinite(ms) || ms < 0) return "";
  const s = Math.round(ms / 1000);
  if (s < 60) return `${s} s`;
  const m = Math.round(s / 60);
  if (m < 60) return `${m} min`;
  const h = Math.floor(m / 60);
  if (h < 48) return `${h} h`;
  return `${Math.floor(h / 24)} d`;
}

export function fmtAgeSince(iso: string | undefined, now = Date.now()): string {
  if (isZeroTime(iso)) return "";
  const t = new Date(iso!).getTime();
  return Number.isNaN(t) ? "" : fmtAge(now - t);
}

/** "2 h 14 m", "5 m", "12 s". */
export function fmtUptime(seconds: number): string {
  const s = Math.max(0, Math.floor(seconds));
  const d = Math.floor(s / 86_400);
  const h = Math.floor((s % 86_400) / 3600);
  const m = Math.floor((s % 3600) / 60);
  if (d > 0) return `${d} d ${h} h`;
  if (h > 0) return `${h} h ${m} m`;
  if (m > 0) return `${m} m`;
  return `${s} s`;
}

/** Go duration in nanoseconds to "21 s" / "1.5 s" / "30 s". */
export function fmtDurationNs(ns: number): string {
  const s = ns / 1e9;
  if (s >= 10) return `${Math.round(s)} s`;
  if (s >= 1) return `${s.toFixed(1)} s`;
  return `${Math.round(ns / 1e6)} ms`;
}

/** Nanoseconds to whole seconds, for "Probes every 30 s". */
export function nsToSeconds(ns: number): number {
  return Math.round(ns / 1e9);
}

export function fmtMbps(v: number): string {
  if (!Number.isFinite(v)) return "0.0";
  return v.toFixed(1);
}

export function fmtMs(v: number, digits = 1): string {
  if (!Number.isFinite(v) || v < 0) return "";
  if (v >= 100) return v.toFixed(0);
  return v.toFixed(digits);
}

export function fmtBytes(n: number): string {
  if (n >= 1e9) return `${(n / 1e9).toFixed(1)} GB`;
  if (n >= 1e6) return `${Math.round(n / 1e6)} MB`;
  if (n >= 1e3) return `${Math.round(n / 1e3)} kB`;
  return `${n} B`;
}

export function fmtPercent(fraction: number): string {
  if (!Number.isFinite(fraction) || fraction < 0) return "0";
  const p = fraction * 100;
  return p >= 10 || p === 0 ? p.toFixed(0) : p.toFixed(1).replace(/\.0$/, "");
}

/** Wi-Fi strength 0-100 to the four-bar glyph level 0-4. */
export function sigLevel(strength: number): 0 | 1 | 2 | 3 | 4 {
  if (strength >= 80) return 4;
  if (strength >= 60) return 3;
  if (strength >= 40) return 2;
  if (strength >= 15) return 1;
  return 0;
}

export function bandLabel(band: string | undefined, freqMhz?: number): string {
  if (band === "2.4" || band === "5" || band === "6") return `${band} GHz`;
  if (freqMhz) {
    if (freqMhz < 3000) return "2.4 GHz";
    if (freqMhz < 5900) return "5 GHz";
    return "6 GHz";
  }
  return band ? `${band} GHz` : "";
}

export function securityTag(sec: WifiSecurity | undefined): string {
  switch (sec) {
    case "wpa-psk":
      return "WPA2";
    case "sae":
      return "WPA3";
    case "wpa-eap":
      return "Enterprise";
    case "wep":
      return "WEP";
    case "owe":
      return "OWE";
    case "open":
      return "Open";
    default:
      return "";
  }
}

export function securityLabel(sec: WifiSecurity | undefined): string {
  switch (sec) {
    case "wpa-psk":
      return "WPA2 Personal";
    case "sae":
      return "WPA3 Personal";
    case "wpa-eap":
      return "WPA Enterprise";
    case "wep":
      return "WEP";
    case "owe":
      return "Enhanced Open";
    case "open":
      return "Open";
    default:
      return "";
  }
}

export function isSecured(sec: WifiSecurity | undefined): boolean {
  return sec !== undefined && sec !== "open" && sec !== "owe";
}

/** Strips the prefix length: "192.168.1.73/24" -> "192.168.1.73". */
export function ipOnly(cidr: string | undefined): string {
  return (cidr ?? "").split("/")[0] ?? "";
}

export function providerLabel(p: string | undefined): string {
  switch ((p ?? "").toLowerCase()) {
    case "cloudflare":
      return "Cloudflare";
    case "iperf3":
      return "iperf3";
    case "librespeed":
      return "LibreSpeed";
    default:
      return p ?? "";
  }
}

/** Human name for the interface driver line: "Intel AX211" is not available; the driver is. */
export function driverLabel(driver: string | undefined): string {
  if (!driver || driver === "unknown") return "";
  return driver;
}
