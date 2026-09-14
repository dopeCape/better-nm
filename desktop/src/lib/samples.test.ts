import { describe, expect, it } from "vitest";
import type { Baseline, Sample } from "@/api/types";
import { degradedText, dnsSeries, seriesFor } from "./samples";

const anchor = (over: Partial<Baseline>): Baseline => ({ network_key: "wifi:Home", anchor: "1.1.1.1", state: "ok", sample_count: 40, baseline_rtt_ms: 9.8, baseline_loss: 0, current_rtt_ms: 10.1, current_loss: 0, current_dns_ms: -1, updated_at: "", ...over });

describe("quality derivations", () => {
  it("derives the degraded banner from the numbers", () => {
    const d = degradedText([anchor({ anchor: "gateway", baseline_rtt_ms: 1.2, current_rtt_ms: 1.3 }), anchor({ state: "degraded", current_rtt_ms: 23.6, current_loss: 0.06 })], "14:02");
    expect(d.title).toBe("Degraded since 14:02");
    expect(d.body).toBe("Round-trip to 1.1.1.1 is 2.4x its baseline; loss 6%. The gateway is fine, so it is probably upstream.");
  });

  it("blames the link when the gateway itself is degraded", () => {
    const d = degradedText([anchor({ anchor: "gateway", state: "degraded", baseline_rtt_ms: 1.0, current_rtt_ms: 4.0 }), anchor({ state: "degraded", current_rtt_ms: 30 })], "");
    expect(d.title).toBe("Degraded");
    expect(d.body).toContain("Round-trip to the gateway is 4.0x its baseline.");
    expect(d.body).toContain("The gateway itself is slow");
  });

  it("builds series per anchor with lost probes as -1", () => {
    const rows: Sample[] = [
      { time: "t1", network_key: "k", anchor: "gateway", rtt_ms: 1.2, loss: 0, dns_ms: 14, method: "icmp" },
      { time: "t1", network_key: "k", anchor: "1.1.1.1", rtt_ms: 10.4, loss: 0, dns_ms: -1, method: "icmp" },
      { time: "t2", network_key: "k", anchor: "gateway", rtt_ms: 1.3, loss: 0, dns_ms: 12, method: "icmp" },
      { time: "t2", network_key: "k", anchor: "1.1.1.1", rtt_ms: -1, loss: 1, dns_ms: -1, method: "icmp" },
    ];
    expect(seriesFor(rows, "1.1.1.1")).toEqual([10.4, -1]);
    expect(seriesFor(rows, "gateway")).toEqual([1.2, 1.3]);
    expect(dnsSeries(rows)).toEqual([14, 12]);
  });
});
