import { useMemo } from "react";
import { useEvents, useSamples } from "@/api/queries";
import type { Event } from "@/api/types";
import { Chart } from "@/components/Chart";
import { Icon } from "@/components/Icon";
import { Badge, CopyButton, EmptyState, PageHead, SectionHead, Sig, Skeleton, Tag } from "@/components/ui";
import type { IconName } from "@/design/icon-names";
import { qualityBadge, useConnection } from "@/lib/connection";
import { bandLabel, driverLabel, fmtMs, fmtWhen, ipOnly, securityTag, sigLevel } from "@/lib/format";
import { seriesFor } from "@/lib/samples";
import { useUI } from "@/state/ui";
import { VpnRow } from "./vpn/VpnRow";

export function Overview() {
  const c = useConnection();
  const setSection = useUI((s) => s.setSection);
  const sub = c.loading ? "" : !c.connected ? "Not connected." : `On ${c.kind === "wifi" ? "Wi-Fi" : c.kind === "ethernet" ? "a wired link" : c.name}.${c.vpnUp.length ? ` ${c.vpnUp.map((v) => v.name).join(", ")} ${c.vpnUp.length > 1 ? "are" : "is"} up.` : ""}`;

  return (
    <>
      <PageHead title="Overview" sub={c.loading ? <Skeleton w={200} /> : sub} />
      <div className="cols cols-3-2">
        <div>
          <section className="section flush">
            <SectionHead
              title="Connection"
              actions={
                c.kind === "wifi" && (
                  <button type="button" className="btn btn-ghost" onClick={() => setSection("wifi")}>
                    Wi-Fi details <Icon name="arrow-right" />
                  </button>
                )
              }
            />
            <ConnectionKv c={c} />
          </section>
          <section className="section">
            <SectionHead
              title="VPN"
              actions={
                <button type="button" className="btn btn-ghost" onClick={() => setSection("vpn")}>
                  Manage <Icon name="arrow-right" />
                </button>
              }
            />
            <VpnList c={c} />
          </section>
        </div>
        <div>
          <QualitySection c={c} />
          <section className="section">
            <SectionHead title="Recent events" />
            <EventsList />
          </section>
        </div>
      </div>
    </>
  );
}

function ConnectionKv({ c }: { c: ReturnType<typeof useConnection> }) {
  if (c.loading) {
    return (
      <dl className="kv" aria-busy="true">
        {["Network", "Device", "IPv4", "Gateway", "DNS", "Signal", "Link"].map((k) => (
          <KvRow key={k} k={k}>
            <Skeleton w={140} />
          </KvRow>
        ))}
      </dl>
    );
  }
  if (!c.connected) {
    return (
      <EmptyState icon="wifi-slash" title="Not connected" actions={<button type="button" className="btn btn-primary" onClick={() => useUI.getState().setSection("wifi")}>Pick a network</button>}>
        {c.wifiEnabled ? "Nothing is carrying the default route right now." : "Wi-Fi is off and no wired link is up."}
      </EmptyState>
    );
  }
  const d = c.device;
  const n = c.network;
  const primaryDns = d?.dns?.[0] ?? "";
  const gw = d?.gateway4 ?? "";
  return (
    <dl className="kv">
      <KvRow k="Network">
        {c.name} {n && securityTag(n.security) && <Tag>{securityTag(n.security)}</Tag>}
      </KvRow>
      <KvRow k="Device">
        <span className="mono">{d?.name ?? ""}</span>
        {d && driverLabel(d.driver) && <span className="sub">{driverLabel(d.driver)}</span>}
      </KvRow>
      <KvRow k="IPv4">
        <span className="mono">{c.cidr}</span>
        {c.cidr && <CopyButton text={ipOnly(c.cidr)} label="Copy IPv4" />}
      </KvRow>
      {gw && (
        <KvRow k="Gateway">
          <span className="mono">{gw}</span>
        </KvRow>
      )}
      {primaryDns && (
        <KvRow k="DNS">
          <span className="mono">{d?.dns?.join(", ")}</span>
        </KvRow>
      )}
      {n && (
        <KvRow k="Signal">
          <Sig level={sigLevel(n.strength)} />
          <span className="mono">{n.strength}%</span>
          <span className="sub">
            {bandLabel(n.band, n.frequency_mhz)}, channel <span className="mono">{n.channel}</span>
          </span>
        </KvRow>
      )}
      {!!d?.speed_mbps && (
        <KvRow k="Link">
          <span className="mono">{d.speed_mbps} Mbit/s</span>
        </KvRow>
      )}
    </dl>
  );
}

function KvRow({ k, children }: { k: string; children: React.ReactNode }) {
  return (
    <>
      <dt>{k}</dt>
      <dd>{children}</dd>
    </>
  );
}

function VpnList({ c }: { c: ReturnType<typeof useConnection> }) {
  if (c.loading) {
    return (
      <div className="list" aria-busy="true">
        {[0, 1].map((i) => (
          <div key={i} className="row two vpn-row">
            <Skeleton w={16} h={16} />
            <div>
              <Skeleton w={120} />
              <Skeleton w={160} h={12} style={{ marginTop: 4 }} />
            </div>
            <Skeleton w={110} h={20} />
          </div>
        ))}
      </div>
    );
  }
  if (!c.vpns.length) {
    return (
      <EmptyState icon="shield-check" title="No VPN yet" actions={<button type="button" className="btn" onClick={() => useUI.getState().setSection("vpn")}>Add from file</button>}>
        Import a WireGuard or OpenVPN file, or install Tailscale.
      </EmptyState>
    );
  }
  return (
    <div className="list">
      {c.vpns.map((v) => (
        <VpnRow key={v.id} vpn={v} compact />
      ))}
    </div>
  );
}

function QualitySection({ c }: { c: ReturnType<typeof useConnection> }) {
  const m = c.monitor;
  const q = qualityBadge(m);
  const samples = useSamples(200);
  const anchors = m?.anchors ?? [];
  const gateway = anchors.find((a) => a.anchor === "gateway");
  const publicAnchor = anchors.find((a) => a.anchor !== "gateway") ?? gateway;
  const dnsMs = anchors.map((a) => a.current_dns_ms).find((v) => v >= 0);

  const series = useMemo(() => (publicAnchor ? seriesFor(samples.data ?? [], publicAnchor.anchor, 48) : []), [samples.data, publicAnchor]);

  return (
    <section className="section flush">
      <SectionHead
        title="Quality"
        actions={
          <Badge tone={q.tone} icon={q.icon}>
            {q.text}
          </Badge>
        }
      />
      {!m ? (
        <div className="stats mb-3" aria-busy="true">
          {[0, 1, 2].map((i) => (
            <div key={i} className="stat">
              <span className="v">
                <Skeleton w={64} h={28} />
              </span>
              <span className="k">
                <Skeleton w={48} h={12} />
              </span>
            </div>
          ))}
        </div>
      ) : (
        <div className="stats mb-3">
          {gateway && <Stat v={fmtMs(gateway.current_rtt_ms)} unit="ms" k="gateway" />}
          {publicAnchor && publicAnchor !== gateway && <Stat v={fmtMs(publicAnchor.current_rtt_ms)} unit="ms" k={publicAnchor.anchor} />}
          {dnsMs !== undefined && <Stat v={fmtMs(dnsMs, 0)} unit="ms" k="DNS" />}
        </div>
      )}
      {publicAnchor && series.length > 1 ? (
        <Chart data={series} base={publicAnchor.baseline_rtt_ms || avg(series)} tol={bandTol(publicAnchor.baseline_rtt_ms || avg(series))} w={340} h={104} tone={publicAnchor.state === "degraded" ? "degraded" : ""} label={`${publicAnchor.anchor} round-trip over the last ${series.length} samples`} />
      ) : (
        <EmptyState title="No samples yet" style={{ padding: "var(--s-6)", minHeight: 123 }}>
          {m?.paused ? "Monitoring is paused." : "The first probe lands within a minute of connecting."}
        </EmptyState>
      )}
      {publicAnchor && series.length > 1 && (
        <div className="chart-legend mt-2">
          <span>
            <i />
            {publicAnchor.anchor} round-trip
          </span>
          <span>
            <i className="band" />
            {publicAnchor.state === "learning" ? "baseline so far" : "baseline"}
          </span>
        </div>
      )}
    </section>
  );
}

function avg(xs: number[]): number {
  const v = xs.filter((x) => x >= 0);
  return v.length ? v.reduce((a, b) => a + b, 0) / v.length : 1;
}

/** Band half-width: a quarter of the baseline, at least 0.3 ms. */
export function bandTol(base: number): number {
  return Math.max(0.3, base * 0.25);
}

function Stat({ v, unit, k }: { v: string; unit: string; k: string }) {
  return (
    <div className="stat">
      <span className="v">
        {v}
        <small>{unit}</small>
      </span>
      <span className="k">{k}</span>
    </div>
  );
}

const EVENT_ICON: Partial<Record<Event["type"], { name: IconName; tone?: "ok" | "warn" | "error" | "vpn" | "wifi" | "accent" }>> = {
  connected: { name: "wifi-high", tone: "wifi" },
  disconnected: { name: "wifi-slash" },
  "no-internet": { name: "warning-circle", tone: "warn" },
  "internet-restored": { name: "check-circle", tone: "ok" },
  "vpn-up": { name: "shield-check", tone: "vpn" },
  "vpn-down": { name: "shield-check" },
  degraded: { name: "pulse", tone: "error" },
  recovered: { name: "pulse", tone: "ok" },
  "secret-needed": { name: "key", tone: "accent" },
  "secret-resolved": { name: "key" },
};

const HIDDEN_EVENTS = new Set<Event["type"]>(["wifi-scan", "state-changed"]);

function EventsList() {
  const events = useEvents(40);
  const live = useUI((s) => s.liveEvents);
  const items = useMemo(() => {
    const seen = new Set<string>();
    const all = [...live, ...[...(events.data ?? [])].reverse()].filter((e) => !HIDDEN_EVENTS.has(e.type));
    const out: Event[] = [];
    for (const e of all) {
      const k = `${e.time}|${e.type}|${e.title}`;
      if (seen.has(k)) continue;
      seen.add(k);
      out.push(e);
      if (out.length >= 6) break;
    }
    return out;
  }, [events.data, live]);

  if (events.isPending) {
    return (
      <ol className="events" aria-busy="true">
        {[0, 1, 2, 3].map((i) => (
          <li key={i}>
            <Skeleton w={16} h={16} />
            <div>
              <Skeleton w={180} />
            </div>
            <Skeleton w={40} h={12} />
          </li>
        ))}
      </ol>
    );
  }
  if (!items.length) return <EmptyState title="Nothing yet">Connections, VPN changes and quality verdicts show up here.</EmptyState>;
  return (
    <ol className="events">
      {items.map((e) => {
        const ic = EVENT_ICON[e.type] ?? { name: "info" as IconName };
        return (
          <li key={`${e.time}-${e.type}`}>
            <Icon name={ic.name} tone={ic.tone} />
            <div>
              <div>{e.title}</div>
              {e.body && <div className="sub sm">{e.body}</div>}
            </div>
            <span className="mono sub sm">{fmtWhen(e.time)}</span>
          </li>
        );
      })}
    </ol>
  );
}
