import { describe, expect, it } from "vitest";
import { autoYmax } from "./Chart";

describe("chart y scale", () => {
  it("leaves room for the band and the bulk of the samples, not the odd spike", () => {
    // A 22 ms baseline with a 5.5 ms band: the scale is at least twice the band's top.
    const base = 22;
    const tol = 5.5;
    const calm = Array.from({ length: 60 }, (_, i) => 20 + (i % 5));
    expect(autoYmax(calm, base, tol)).toBe(55);
    // One 343 ms outlier must not flatten the chart; it sits on the top edge instead.
    const spiky = [...calm, 343];
    expect(autoYmax(spiky, base, tol)).toBe(55);
    // Many high samples do move the scale (p95 rules, with headroom).
    const high = Array.from({ length: 60 }, () => 100);
    expect(autoYmax(high, base, tol)).toBe(115);
    // Empty data still yields a usable scale.
    expect(autoYmax([], 0, 0)).toBe(0.1);
  });
});
