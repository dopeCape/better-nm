// One series over its baseline band: the quality chart from the design, as SVG.
// data: RTT in ms, -1 for a lost probe. The line draws in once on mount (600 ms)
// unless motion is reduced.
import { useEffect, useMemo, useRef } from "react";

export interface ChartProps {
  data: number[];
  base: number;
  tol: number;
  w?: number;
  h?: number;
  axis?: boolean;
  ymax?: number;
  tone?: "" | "degraded" | "dns";
  from?: string;
  draw?: boolean;
  label?: string;
}

/**
 * The y scale: room for the band and the bulk of the samples, not for the odd
 * spike. One 300 ms outlier over a 20 ms baseline would otherwise flatten the
 * line and the band into a sliver, and the band is what the chart is about.
 */
export function autoYmax(valid: number[], base: number, tol: number): number {
  const sorted = [...valid].sort((a, b) => a - b);
  const p95 = sorted.length ? sorted[Math.min(sorted.length - 1, Math.floor(0.95 * (sorted.length - 1)))]! : 0;
  const max = Math.max(2 * (base + tol), p95 * 1.15, 0.1);
  return Math.ceil(max * 10) / 10;
}

export function motionOff(): boolean {
  if (typeof document === "undefined") return true;
  if (document.documentElement.dataset.motion === "0") return true;
  return typeof matchMedia === "function" && matchMedia("(prefers-reduced-motion: reduce)").matches;
}

export function Chart({ data, base, tol, w = 400, h = 120, axis = false, ymax: ymaxIn, tone = "", from = "-30 min", draw = true, label }: ChartProps) {
  const pathRef = useRef<SVGPathElement>(null);
  const drawn = useRef(false);

  const m = useMemo(() => {
    const padL = axis ? 34 : 0;
    const padB = axis ? 16 : 0;
    const padT = 6;
    const valid = data.filter((v) => v >= 0);
    const ymax = ymaxIn ?? autoYmax(valid, base, tol);
    const iw = w - padL;
    const ih = h - padB - padT;
    const n = Math.max(1, data.length - 1);
    const x = (i: number) => padL + (i / n) * iw;
    // Values above the scale sit on the top edge: an outlier reads as "off the chart".
    const y = (v: number) => Math.max(padT, padT + ih - (v / ymax) * ih);
    let d = "";
    let pen = false;
    data.forEach((v, i) => {
      if (v < 0) {
        pen = false;
        return;
      }
      d += `${pen ? "L" : "M"}${x(i).toFixed(1)} ${y(v).toFixed(1)}`;
      pen = true;
    });
    let last = data.length - 1;
    while (last >= 0 && (data[last] ?? -1) < 0) last--;
    const top = y(base + tol);
    const bot = y(Math.max(0, base - tol));
    return { padL, padB, padT, iw, ih, ymax, x, y, d, last, top, bot, by: Math.round(y(base)) + 0.5 };
  }, [data, base, tol, w, h, axis, ymaxIn]);

  useEffect(() => {
    const p = pathRef.current;
    if (!p || drawn.current || !draw || motionOff() || typeof p.getTotalLength !== "function") return;
    drawn.current = true;
    if (!m.d) return;
    p.style.setProperty("--len", p.getTotalLength().toFixed(0));
    p.classList.add("draw");
    const done = () => {
      p.classList.remove("draw");
      p.style.removeProperty("--len");
    };
    p.addEventListener("animationend", done, { once: true });
    return () => p.removeEventListener("animationend", done);
    // mount only
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const ticks = [0, 0.5, 1];
  return (
    <div className="chart">
      <svg viewBox={`0 0 ${w} ${h}`} height={h} role="img" aria-label={label ?? "Round-trip time over the last samples"}>
        <g className="grid">
          {ticks.map((f) => {
            const yy = Math.round(m.padT + m.ih * f) + 0.5;
            return <line key={f} x1={m.padL} x2={w} y1={yy} y2={yy} />;
          })}
        </g>
        <rect className="band" x={m.padL} y={m.top} width={m.iw} height={Math.max(0, m.bot - m.top)} />
        <line className="band-mid" x1={m.padL} x2={w} y1={m.by} y2={m.by} />
        <path ref={pathRef} className={`line ${tone}`.trim()} d={m.d} />
        {data.map((v, i) => (v < 0 ? <rect key={i} className="loss" x={m.x(i) - 1.5} y={m.padT + m.ih - 6} width={3} height={6} /> : null))}
        {m.last >= 0 && <circle className="end" cx={m.x(m.last)} cy={m.y(data[m.last]!)} r={3} />}
        {axis && (
          <g className="axis">
            {ticks.map((f) => (
              <text key={f} x={m.padL - 6} y={m.padT + m.ih * (1 - f) + 3} textAnchor="end">
                {(m.ymax * f).toFixed(m.ymax < 5 ? 1 : 0)}
              </text>
            ))}
            <text x={m.padL} y={h - 2}>
              {from}
            </text>
            <text x={w} y={h - 2} textAnchor="end">
              now
            </text>
          </g>
        )}
      </svg>
    </div>
  );
}
