import { useMemo } from "react";
import { actions, useAction } from "@/api/actions";
import { qk, useMonitor, useSamples } from "@/api/queries";
import type { Baseline, MonitorStatus, Sample } from "@/api/types";
import { useHotkey } from "@/app/hotkeys";
import { Chart } from "@/components/Chart";
import { Icon } from "@/components/Icon";
import { Banner, EmptyState, Keys, PageHead, Skeleton } from "@/components/ui";
import { LEARNING_TARGET, useConnection } from "@/lib/connection";
import { fmtMs, fmtPercent, fmtWhen, nsToSeconds } from "@/lib/format";
import { degradedText, dnsSeries, seriesFor, spanLabel } from "@/lib/samples";
import { useUI } from "@/state/ui";
import { bandTol } from "./Overview";

export function Quality() {
  const monitor = useMonitor();
  const samples = useSamples(200);
  const c = useConnection();
  const setSection = useUI((s) => s.setSection);
  const m = monitor.data;

  const pause = useAction(async (paused: boolean) => (paused ? actions.monitorResume() : actions.monitorPause()), { invalidate: [qk.monitor], label: "Monitoring" });
  const reset = useAction(actions.monitorReset, { invalidate: [qk.monitor, ["monitor", "samples"]], label: "Reset baseline" });
  useHotkey("p", () => void pause.run(m?.paused ?? false), !!m);

  const interval = m ? nsToSeconds(m.interval) : 30;
  const anchors = m?.anchors ?? [];
  const gateway = anchors.find((a) => a.anchor === "gateway");
  const others = anchors.filter((a) => a.anchor !== "gateway");
  const rows = useMemo(() => samples.data ?? [], [samples.data]);
  const dns = useMemo(() => dnsSeries(rows, 60), [rows]);
  const dnsNow = anchors.map((a) => a.current_dns_ms).find((v) => v >= 0);
  const dnsBase = dns.length ? median(dns) : 0;
  const resolver = rows.find((r) => r.anchor === "gateway")?.anchor_addr ?? "";

  return (
    <>
      <PageHead
        title="Quality"
        sub={
          <>
            How this network behaves against its own history. Probes every <span className="mono">{interval} s</span>.
          </>
        }
        actions={
          <>
            <button type="button" className="btn" onClick={() => void pause.run(m?.paused ?? false)} disabled={!m || pause.pending}>
              <Icon name={m?.paused ? "play" : "pause"} />
              {m?.paused ? "Resume" : "Pause"} <Keys keys={["P"]} />
            </button>
            <button type="button" className="btn btn-ghost" onClick={() => void reset.run()} disabled={!m || reset.pending}>
              <Icon name="arrow-counter-clockwise" />
              Reset baseline
            </button>
          </>
        }
      />
      <VerdictBanner m={m} loading={monitor.isPending} network={c.name} onSpeed={() => setSection("speed")} />

      {monitor.isError ? (
        <EmptyState error icon="warning-circle" title="Could not read the monitor" className="mt-6" actions={<button type="button" className="btn" onClick={() => void monitor.refetch()}>Try again</button>}>
          {(monitor.error as Error).message}
        </EmptyState>
      ) : !m && monitor.isPending ? (
        <AnchorSkeleton />
      ) : !anchors.length ? (
        <EmptyState icon="pulse" title="No anchors yet" className="mt-6">
          {m?.state === "idle" && !c.connected ? "Connect to a network and the first probe lands within a minute." : "The daemon has not probed this network yet."}
        </EmptyState>
      ) : (
        <>
          {gateway && <AnchorSection a={gateway} rows={rows} title="Gateway" />}
          {others.map((a) => (
            <AnchorSection key={a.anchor} a={a} rows={rows} title={a.anchor} />
          ))}
          {dns.length > 1 && (
            <section className="section anchor">
              <div className="anchor-head">
                <div>
                  <h2 className="h3">DNS</h2>
                  <div className="sub sm">
                    resolve time
                    {resolver && (
                      <>
                        {" "}via <span className="mono">{resolver}</span>
                      </>
                    )}
                  </div>
                </div>
                <div className="stats">
                  <Stat v={fmtMs(dnsNow ?? dns[dns.length - 1] ?? 0, 0)} unit="ms" k="now" />
                  <Stat v={fmtMs(dnsBase, 0)} unit="ms" k="typical" sub />
                </div>
              </div>
              <Chart data={dns} base={dnsBase} tol={bandTol(dnsBase)} w={1040} h={120} axis tone="dns" from={spanLabel(rows, "gateway", 60)} label="DNS resolve time" />
            </section>
          )}
          <div className="chart-legend mt-4">
            <span>
              <i />
              measured
            </span>
            <span>
              <i className="band" />
              baseline band
            </span>
            <span>
              <i className="warn" />
              degraded
            </span>
            <span>
              <i className="loss-swatch" />
              lost probe
            </span>
          </div>
        </>
      )}
    </>
  );
}

function median(xs: number[]): number {
  const s = [...xs].filter((v) => v >= 0).sort((a, b) => a - b);
  if (!s.length) return 0;
  const mid = Math.floor(s.length / 2);
  return s.length % 2 ? s[mid]! : (s[mid - 1]! + s[mid]!) / 2;
}

function VerdictBanner({ m, loading, network, onSpeed }: { m: MonitorStatus | undefined; loading: boolean; network: string; onSpeed: () => void }) {
  if (loading && !m) {
    return (
      <div className="banner" aria-busy="true">
        <Skeleton w={16} h={16} />
        <div className="grow">
          <Skeleton w={160} />
          <Skeleton w={320} h={12} style={{ marginTop: 6 }} />
        </div>
      </div>
    );
  }
  if (!m) return null;
  if (m.paused) {
    return (
      <Banner icon="pause" title="Monitoring paused">
        No probes are running. Resume to keep the baseline current.
      </Banner>
    );
  }
  const total = m.anchors.reduce((n, a) => n + a.sample_count, 0);
  const since = fmtWhen(m.anchors[0]?.since);
  switch (m.state) {
    case "ok":
      return (
        <Banner tone="ok" icon="check-circle-fill" title="Within baseline" actions={<span className="sub sm">{m.anchors[0]?.sample_count ?? 0} samples{since && <> · since <span className="mono">{since}</span></>}</span>}>
          Round-trip and loss look like they usually do{network ? ` on ${network}` : ""}.
        </Banner>
      );
    case "learning": {
      const n = Math.min(LEARNING_TARGET, Math.max(...m.anchors.map((a) => a.sample_count), 0));
      return (
        <Banner tone="warn" icon="spinner-gap" title="Learning this network" actions={<div className="progress" style={{ width: 120 }} role="progressbar" aria-valuemin={0} aria-valuemax={LEARNING_TARGET} aria-valuenow={n} aria-label="Samples collected"><i style={{ "--p": n / LEARNING_TARGET } as React.CSSProperties} /></div>}>
          {n} of {LEARNING_TARGET} samples collected. Verdicts start once the baseline is known.
        </Banner>
      );
    }
    case "degraded": {
      const d = degradedText(m.anchors, fmtWhen(m.anchors.find((a) => a.state === "degraded")?.since));
      return (
        <Banner
          tone="error"
          icon="warning-circle-fill"
          title={d.title}
          actions={
            <button type="button" className="btn" onClick={onSpeed}>
              <Icon name="gauge" />
              Run speed test
            </button>
          }
        >
          {d.body}
        </Banner>
      );
    }
    default:
      return (
        <Banner icon="pulse" title="Monitor idle">
          {total ? "Not connected right now; history is kept." : "Nothing to measure until a network is up."}
        </Banner>
      );
  }
}

function AnchorSection({ a, rows, title }: { a: Baseline; rows: Sample[]; title: string }) {
  const series = useMemo(() => seriesFor(rows, a.anchor, 60), [rows, a.anchor]);
  const addr = rows.find((r) => r.anchor === a.anchor)?.anchor_addr ?? "";
  const method = rows.find((r) => r.anchor === a.anchor)?.method ?? "";
  const base = a.baseline_rtt_ms > 0 ? a.baseline_rtt_ms : median(series);
  const degraded = a.state === "degraded";
  return (
    <section className="section anchor">
      <div className="anchor-head">
        <div>
          <h2 className="h3">{title}</h2>
          <div className="sub sm mono">
            {addr && addr !== title ? addr : ""}
            {addr && addr !== title && method ? " · " : ""}
            {method}
          </div>
        </div>
        <div className="stats">
          <Stat v={fmtMs(a.current_rtt_ms)} unit="ms" k="now" tone={degraded ? "warn" : undefined} />
          <Stat v={a.state === "learning" && a.baseline_rtt_ms <= 0 ? "" : fmtMs(base)} unit="ms" k={a.state === "learning" ? "baseline so far" : "baseline"} sub />
          <Stat v={fmtPercent(a.current_loss)} unit="%" k="loss" sub={a.current_loss === 0} tone={a.current_loss > 0.02 ? "warn" : undefined} />
        </div>
      </div>
      {series.length > 1 ? (
        <Chart data={series} base={base} tol={bandTol(base)} w={1040} h={120} axis tone={degraded ? "degraded" : ""} from={spanLabel(rows, a.anchor, 60)} label={`${title} round-trip over the last ${series.length} samples`} />
      ) : (
        <EmptyState title="No samples yet" style={{ padding: "var(--s-6)", minHeight: 120 }}>
          The first probe lands within {a.sample_count ? "a minute" : "a minute of connecting"}.
        </EmptyState>
      )}
    </section>
  );
}

function AnchorSkeleton() {
  return (
    <div aria-busy="true">
      {[0, 1].map((i) => (
        <section key={i} className="section anchor">
          <div className="anchor-head">
            <div>
              <Skeleton w={100} h={16} />
              <Skeleton w={140} h={12} style={{ marginTop: 6 }} />
            </div>
            <div className="stats">
              {[0, 1, 2].map((j) => (
                <div key={j} className="stat">
                  <span className="v">
                    <Skeleton w={56} h={22} />
                  </span>
                  <span className="k">
                    <Skeleton w={40} h={12} />
                  </span>
                </div>
              ))}
            </div>
          </div>
          <Skeleton w="100%" h={120} />
        </section>
      ))}
    </div>
  );
}

function Stat({ v, unit, k, sub, tone }: { v: string; unit: string; k: string; sub?: boolean; tone?: "warn" }) {
  return (
    <div className="stat">
      <span className={`v${sub ? " sub" : ""}${tone ? ` tone-${tone}` : ""}`}>
        {v || "-"}
        <small>{unit}</small>
      </span>
      <span className="k">{k}</span>
    </div>
  );
}
