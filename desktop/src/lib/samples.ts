// Turns /v1/monitor/samples rows (newest last) into chart series.
import type { Baseline, Sample } from "@/api/types";
import { fmtPercent } from "./format";

/** RTT series for one anchor, -1 where the probe was lost, last `n` points. */
export function seriesFor(samples: Sample[], anchor: string, n = 60): number[] {
  const rows = samples.filter((s) => s.anchor === anchor);
  return rows.slice(-n).map((s) => (s.rtt_ms < 0 || s.loss >= 1 ? -1 : round2(s.rtt_ms)));
}

/** DNS resolve series (the first anchor row of each round carries dns_ms). */
export function dnsSeries(samples: Sample[], n = 60): number[] {
  return samples
    .filter((s) => s.dns_ms >= 0)
    .slice(-n)
    .map((s) => round2(s.dns_ms));
}

function round2(v: number): number {
  return Math.round(v * 100) / 100;
}

/** The earliest sample time in the series, as a "-30 min" style label. */
export function spanLabel(samples: Sample[], anchor: string, n = 60, now = Date.now()): string {
  const rows = samples.filter((s) => s.anchor === anchor).slice(-n);
  const first = rows[0];
  if (!first) return "";
  const min = Math.round((now - new Date(first.time).getTime()) / 60_000);
  if (min < 1) return "now";
  if (min < 90) return `-${min} min`;
  return `-${Math.round(min / 60)} h`;
}

export interface DegradedText {
  title: string;
  body: string;
}

/**
 * The degraded banner copy, derived from the numbers:
 * "Round-trip to 1.1.1.1 is 2.4x its baseline; loss 6%. The gateway is fine, so it is probably upstream."
 */
export function degradedText(anchors: Baseline[], since: string): DegradedText {
  const bad = anchors.filter((a) => a.state === "degraded");
  const gw = anchors.find((a) => a.anchor === "gateway");
  const parts: string[] = [];
  for (const a of bad) {
    const ratio = a.baseline_rtt_ms > 0 ? a.current_rtt_ms / a.baseline_rtt_ms : 0;
    const name = a.anchor === "gateway" ? "the gateway" : a.anchor;
    const rtt = ratio >= 1.2 ? `Round-trip to ${name} is ${ratio.toFixed(1)}x its baseline` : `Round-trip to ${name} is within baseline`;
    const loss = a.current_loss > 0 ? `; loss ${fmtPercent(a.current_loss)}%` : "";
    parts.push(`${rtt}${loss}.`);
  }
  let where: string;
  if (gw && gw.state === "degraded") where = "The gateway itself is slow, so it is probably the link or the router.";
  else if (gw && gw.state === "ok" && bad.length) where = "The gateway is fine, so it is probably upstream.";
  else where = "";
  return { title: since ? `Degraded since ${since}` : "Degraded", body: [parts.join(" "), where].filter(Boolean).join(" ") };
}
