import { useEffect, useMemo, useRef, useState } from "react";
import { useDaemonConfig, useSpeedHistory } from "@/api/queries";
import type { SpeedResult } from "@/api/types";
import { useHotkey } from "@/app/hotkeys";
import { motionOff } from "@/components/Chart";
import { Icon } from "@/components/Icon";
import { Dot, EmptyState, Keys, PageHead, SectionHead, Select, Skeleton, Tag } from "@/components/ui";
import { useConnection } from "@/lib/connection";
import { fmtBytes, fmtDurationNs, fmtMbps, fmtMs, fmtWhenLong, fmtWhenSentence, providerLabel } from "@/lib/format";
import { shell } from "@/shell";
import { ui, useUI } from "@/state/ui";

export function Speed() {
  const c = useConnection();
  const cfg = useDaemonConfig();
  const history = useSpeedHistory(50);
  const speed = useUI((s) => s.speed);
  const start = useUI((s) => s.speedStart);
  const reset = useUI((s) => s.speedReset);
  const setError = useUI((s) => s.speedError);
  const [provider, setProvider] = useState<string>("");

  const configured = cfg.data?.speed.provider || "cloudflare";
  const chosen = provider || configured;
  const iperf = cfg.data?.speed.iperf3_server ?? "";
  const providers = useMemo(() => {
    const opts = [{ value: "cloudflare", label: "Cloudflare" }];
    if (iperf) opts.push({ value: "iperf3", label: `iperf3 · ${iperf}` });
    if (!opts.some((o) => o.value === configured)) opts.push({ value: configured, label: providerLabel(configured) });
    return opts;
  }, [iperf, configured]);

  const run = async (quick: boolean) => {
    if (speed.state === "running") return;
    start(quick);
    try {
      await shell.speedStart({ provider: chosen, quick, ...(chosen === "iperf3" && iperf ? { server: iperf } : {}) });
    } catch (e) {
      const msg = String(e);
      setError(msg === "speed-running" ? "A test is already running." : msg);
    }
  };
  const cancel = async () => {
    try {
      await shell.speedCancel();
    } catch (e) {
      ui.error("Cancel failed", String(e));
    }
    reset();
  };

  const running = speed.state === "running";
  useHotkey("Enter", () => void run(false), !running);
  useHotkey("q", () => void run(true), !running);
  useHotkey("Escape", () => void cancel(), running);

  const rows = useMemo(() => [...(history.data ?? [])].reverse(), [history.data]);
  const last = rows[0];

  return (
    <>
      <PageHead title="Speed" sub="On demand only. Nothing runs in the background." actions={<Select label="Provider" value={chosen} onChange={setProvider} options={providers} style={{ width: 180 }} disabled={running} />} />

      <section className="hero" aria-live="polite">
        {speed.state === "running" ? (
          <RunningHero />
        ) : speed.state === "result" && speed.result ? (
          <ResultHero r={speed.result} onAgain={() => void run(speed.quick)} />
        ) : (
          <div className="hero-empty">
            <Icon name="gauge" size="2xl" tone="muted" />
            <div className="h2 mt-3">{c.connected ? `Measure ${c.name}` : "Not connected"}</div>
            {speed.state === "error" ? (
              <p className="tone-error mt-2" role="alert">
                {speed.error}
              </p>
            ) : (
              <p className="sub mt-2">A full test moves about 100 MB and takes 20 s. Quick stops at 10 MB.</p>
            )}
            <div className="flex mt-4">
              <button type="button" className="btn btn-primary btn-lg" onClick={() => void run(false)} disabled={!c.connected}>
                <Icon name="play" />
                Run <Keys enter />
              </button>
              <button type="button" className="btn btn-lg" onClick={() => void run(true)} disabled={!c.connected}>
                <Icon name="lightning" />
                Quick <Keys keys={["Q"]} />
              </button>
            </div>
            {history.isPending ? (
              <div className="sub sm mt-4">
                <Skeleton w={260} h={12} />
              </div>
            ) : last ? (
              <div className="sub sm mt-4">
                Last run {fmtWhenSentence(last.time)[0]} <span className="mono">{fmtWhenSentence(last.time)[1]}</span>: <span className="mono">{fmtMbps(last.download_mbps)}</span> down, <span className="mono">{fmtMbps(last.upload_mbps)}</span> up
              </div>
            ) : (
              <div className="sub sm mt-4">No runs yet on this machine.</div>
            )}
          </div>
        )}
      </section>

      <section className="section">
        <SectionHead title="History" sub="this network and others" />
        {history.isPending ? (
          <table className="table" aria-busy="true">
            <thead>
              <HistoryHead />
            </thead>
            <tbody>
              {[0, 1, 2].map((i) => (
                <tr key={i}>
                  <td>
                    <Skeleton w={90} />
                  </td>
                  <td>
                    <Skeleton w={110} />
                  </td>
                  <td>
                    <Skeleton w={70} />
                  </td>
                  <td className="num">
                    <Skeleton w={40} />
                  </td>
                  <td className="num">
                    <Skeleton w={40} />
                  </td>
                  <td className="num">
                    <Skeleton w={40} />
                  </td>
                  <td className="num">
                    <Skeleton w={40} />
                  </td>
                  <td />
                </tr>
              ))}
            </tbody>
          </table>
        ) : history.isError ? (
          <EmptyState error icon="warning-circle" title="Could not load history">
            {(history.error as Error).message}
          </EmptyState>
        ) : !rows.length ? (
          <EmptyState icon="gauge" title="No tests yet">
            Results land here, newest first, with the network they ran on.
          </EmptyState>
        ) : (
          <table className="table">
            <thead>
              <HistoryHead />
            </thead>
            <tbody>
              {rows.map((r) => (
                <tr key={r.time}>
                  <td className="mono">{fmtWhenLong(r.time)}</td>
                  <td className="sub">{r.network_key.replace(/^wifi:/, "")}</td>
                  <td className="sub">
                    {providerLabel(r.provider)}
                    {r.server && r.provider === "iperf3" && (
                      <>
                        {" "}
                        <span className="mono">{r.server}</span>
                      </>
                    )}
                  </td>
                  <td className="num">{fmtMbps(r.download_mbps)}</td>
                  <td className="num">{fmtMbps(r.upload_mbps)}</td>
                  <td className="num">{fmtMs(r.latency_ms)}</td>
                  <td className="num">{fmtMs(r.jitter_ms)}</td>
                  <td className="sub">{r.quick && <Tag>quick</Tag>}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </section>
    </>
  );
}

function HistoryHead() {
  return (
    <tr>
      <th>When</th>
      <th>Network</th>
      <th>Provider</th>
      <th className="num">Down Mbit/s</th>
      <th className="num">Up Mbit/s</th>
      <th className="num">Latency ms</th>
      <th className="num">Jitter ms</th>
      <th />
    </tr>
  );
}

const PHASES = ["latency", "download", "upload"] as const;

/** Eases the displayed number toward the live sample, 8 updates/s (DESIGN.md). */
function useCounter(target: number, active: boolean): number {
  const [shown, setShown] = useState(0);
  const cur = useRef(0);
  const [still] = useState(() => motionOff());
  useEffect(() => {
    if (!active || still) {
      cur.current = 0;
      return;
    }
    const h = window.setInterval(() => {
      const diff = target - cur.current;
      if (Math.abs(diff) < 0.05) {
        cur.current = target;
      } else {
        cur.current += diff * 0.35;
      }
      setShown(cur.current);
    }, 125);
    return () => window.clearInterval(h);
  }, [target, active, still]);
  if (!active) return 0;
  if (still) return target;
  return shown;
}

function RunningHero() {
  const speed = useUI((s) => s.speed);
  const reset = useUI((s) => s.speedReset);
  const mbps = useCounter(speed.phase === "latency" ? 0 : speed.mbps, true);
  const idx = PHASES.indexOf(speed.phase as (typeof PHASES)[number]);
  const cancel = async () => {
    try {
      await shell.speedCancel();
    } finally {
      reset();
    }
  };
  return (
    <div className="hero-run">
      <div className="phase-label sub">
        <Dot tone="accent" />
        {speed.phase === "latency" ? "Latency" : speed.phase === "download" ? "Download" : speed.phase === "upload" ? "Upload" : "Finishing"}
      </div>
      <div className="counter">
        <span className="mono" aria-live="off">
          {mbps.toFixed(1)}
        </span>
        <span className="unit">Mbit/s</span>
      </div>
      <div className="progress" style={{ width: 320 }} role="progressbar" aria-label="Test progress" aria-valuemin={0} aria-valuemax={100} aria-valuenow={Math.round(speed.percent)}>
        {PHASES.map((p, i) => (
          <i key={p} className={i < idx ? "done" : ""} style={i === idx ? ({ "--p": Math.max(0, Math.min(1, speed.percent / 100)) } as React.CSSProperties) : undefined} />
        ))}
      </div>
      <div className="phases sub sm">
        <span>
          latency{speed.latencyMs !== undefined && speed.phase !== "latency" && (
            <>
              {" "}
              <span className="mono">{fmtMs(speed.latencyMs, 0)} ms</span>
            </>
          )}
        </span>
        <span>download</span>
        <span>upload</span>
      </div>
      <div className="mt-4">
        <button type="button" className="btn btn-ghost" onClick={() => void cancel()}>
          Cancel <Keys keys={["Esc"]} />
        </button>
      </div>
    </div>
  );
}

function ResultHero({ r, onAgain }: { r: SpeedResult; onAgain: () => void }) {
  const summary = `${providerLabel(r.provider)}${r.server ? ` via ${r.server}` : ""}, ${fmtBytes(r.bytes_moved)} moved in ${fmtDurationNs(r.duration)}.`;
  const copyText = `${fmtMbps(r.download_mbps)} Mbit/s down, ${fmtMbps(r.upload_mbps)} Mbit/s up, ${fmtMs(r.latency_ms)} ms latency, ${fmtMs(r.jitter_ms)} ms jitter (${summary.replace(/\.$/, "")}, ${r.network_key.replace(/^wifi:/, "")})`;
  const [copied, setCopied] = useState(false);
  return (
    <div className="result">
      <div className="stats">
        <div className="stat">
          <span className="v big">
            {fmtMbps(r.download_mbps)}
            <small>Mbit/s</small>
          </span>
          <span className="k">
            <Icon name="download-simple" />
            download
          </span>
        </div>
        <div className="stat">
          <span className="v big">
            {fmtMbps(r.upload_mbps)}
            <small>Mbit/s</small>
          </span>
          <span className="k">
            <Icon name="upload-simple" />
            upload
          </span>
        </div>
        <div className="stat">
          <span className="v">
            {fmtMs(r.latency_ms)}
            <small>ms</small>
          </span>
          <span className="k">latency</span>
        </div>
        <div className="stat">
          <span className="v">
            {fmtMs(r.jitter_ms)}
            <small>ms</small>
          </span>
          <span className="k">jitter</span>
        </div>
      </div>
      <div className="flex mt-4">
        <p className="sub sm">
          {providerLabel(r.provider)}
          {r.server && (
            <>
              {" "}via <span className="mono">{r.server}</span>
            </>
          )}
          , <span className="mono">{fmtBytes(r.bytes_moved)}</span> moved in <span className="mono">{fmtDurationNs(r.duration)}</span>.
        </p>
        <div className="ml-auto btn-group">
          <button type="button" className="btn btn-primary" onClick={onAgain}>
            <Icon name="arrows-clockwise" />
            Run again <Keys enter />
          </button>
          <button
            type="button"
            className="btn"
            onClick={async () => {
              try {
                await navigator.clipboard.writeText(copyText);
                setCopied(true);
                window.setTimeout(() => setCopied(false), 1200);
              } catch {
                ui.error("Clipboard unavailable");
              }
            }}
          >
            <Icon name={copied ? "check" : "copy"} />
            {copied ? "Copied" : "Copy"}
          </button>
        </div>
      </div>
    </div>
  );
}
