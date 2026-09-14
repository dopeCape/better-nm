import { useEffect, useMemo, useRef, useState } from "react";
import { actions, useAction } from "@/api/actions";
import { qk, useDevices, useStatus, useWifi } from "@/api/queries";
import type { WifiNetwork } from "@/api/types";
import { useHotkey } from "@/app/hotkeys";
import { Icon } from "@/components/Icon";
import { Badge, EmptyState, Keys, PageHead, Segmented, Sig, Skeleton, Switch, Tag } from "@/components/ui";
import { bandLabel, fmtAge, isSecured, sigLevel } from "@/lib/format";
import { useUI } from "@/state/ui";
import { dedupe, filterNetworks, sortNetworks, type WifiFilter } from "./wifi/sort";
import { WifiDetail } from "./wifi/WifiDetail";

export function Wifi() {
  const status = useStatus();
  const wifi = useWifi();
  const devices = useDevices();
  const [text, setText] = useState("");
  const [filter, setFilter] = useState<WifiFilter>("all");
  const filterRef = useRef<HTMLInputElement>(null);
  const listRef = useRef<HTMLDivElement>(null);
  const selected = useUI((s) => s.wifiSelected);
  const setSelected = useUI((s) => s.setWifiSelected);
  const lastScan = useUI((s) => s.wifiLastScan);
  const setLastScan = useUI((s) => s.setWifiLastScan);
  const [now, setNow] = useState(() => Date.now());

  const radioOn = status.data?.wifi_enabled ?? true;
  const hardware = status.data?.wifi_hardware ?? true;
  const wifiDevice = devices.data?.find((d) => d.kind === "wifi")?.name ?? wifi.data?.[0]?.device ?? "";

  const all = useMemo(() => sortNetworks(dedupe(wifi.data ?? [])), [wifi.data]);
  const shown = useMemo(() => filterNetworks(all, text, filter), [all, text, filter]);
  const active = all.find((n) => n.active);
  const current = all.find((n) => n.ssid === selected) ?? null;

  // Default selection: the connected network.
  useEffect(() => {
    if (selected === null && active) setSelected(active.ssid);
  }, [selected, active, setSelected]);

  // "Last scan 20 s ago" ticks every 5 s.
  useEffect(() => {
    const h = window.setInterval(() => setNow(Date.now()), 5000);
    return () => window.clearInterval(h);
  }, []);

  const rescan = useAction(actions.wifiScan, {
    label: "Rescan",
    onSuccess: () => setLastScan(Date.now()),
  });
  const radio = useAction(actions.wifiEnabled, { invalidate: [qk.status, qk.wifi], label: "Wi-Fi radio" });

  useHotkey("r", () => void rescan.run(), radioOn);
  useHotkey("/", () => filterRef.current?.focus());
  // Esc closes the detail pane (overlays and inputs take Esc before it gets here).
  useHotkey("Escape", () => setSelected(null), current !== null);

  // Rows fade in as the scan finds them: SSIDs not seen in a previous render
  // get the enter animation (the whole list on first load, staggered).
  const [prevAll, setPrevAll] = useState<WifiNetwork[]>([]);
  const [seen, setSeen] = useState<Set<string>>(() => new Set());
  const [fresh, setFresh] = useState<Set<string>>(() => new Set());
  if (all !== prevAll) {
    setPrevAll(all);
    const next = new Set(all.filter((n) => !seen.has(n.ssid)).map((n) => n.ssid));
    setFresh(next);
    if (next.size) setSeen(new Set([...seen, ...next]));
  }

  const lastSeenMax = all.reduce((m, n) => Math.max(m, n.last_seen ? new Date(n.last_seen).getTime() : 0), 0);
  const scanAt = lastScan ?? (lastSeenMax || null);
  const onListKey = (e: React.KeyboardEvent) => {
    if (e.key !== "ArrowDown" && e.key !== "ArrowUp") return;
    const rows = Array.from(listRef.current?.querySelectorAll<HTMLButtonElement>("button.row") ?? []);
    const i = rows.findIndex((r) => r === document.activeElement);
    if (i < 0) return;
    e.preventDefault();
    rows[Math.max(0, Math.min(rows.length - 1, i + (e.key === "ArrowDown" ? 1 : -1)))]?.focus();
  };

  return (
    <>
      <PageHead
        title="Wi-Fi"
        sub={
          !radioOn ? (
            "The radio is off. Nothing in range."
          ) : wifi.isPending ? (
            <Skeleton w={260} />
          ) : (
            <>
              {all.length} network{all.length === 1 ? "" : "s"} in range
              {wifiDevice && (
                <>
                  {" "}on <span className="mono">{wifiDevice}</span>
                </>
              )}
              .{scanAt && <> Last scan {fmtAge(now - scanAt)} ago.</>}
            </>
          )
        }
        actions={
          <>
            <button type="button" className="btn" onClick={() => void rescan.run()} disabled={!radioOn || rescan.pending}>
              <Icon name="arrows-clockwise" />
              Rescan <Keys keys={["R"]} />
            </button>
            <span className="switch-row" style={{ minHeight: 0, gap: 8 }}>
              <span className="sub">Wi-Fi</span>
              <Switch label="Wi-Fi radio" checked={radioOn} disabled={!hardware} busy={radio.pending} onChange={(on) => void radio.run(on)} />
            </span>
          </>
        }
      />
      <div className="wifi-layout">
        <div>
          <div className="toolbar">
            <div className="input-wrap grow">
              <input ref={filterRef} className="input" placeholder="Filter networks" aria-label="Filter networks" value={text} onChange={(e) => setText(e.target.value)} disabled={!radioOn} onKeyDown={(e) => e.key === "Escape" && setText("")} />
              <span className="trail">
                <Keys keys={["/"]} />
              </span>
            </div>
            <Segmented
              label="Show"
              value={filter}
              onChange={setFilter}
              options={[
                { value: "all", label: "All" },
                { value: "saved", label: "Saved" },
                { value: "5ghz", label: "5 GHz" },
              ]}
            />
          </div>
          {!radioOn ? (
            <EmptyState
              icon="wifi-slash"
              title="Wi-Fi is off"
              className="mt-4"
              actions={
                <button type="button" className="btn btn-primary" onClick={() => void radio.run(true)} disabled={!hardware || radio.pending}>
                  <Icon name="wifi-high" />
                  Turn on Wi-Fi
                </button>
              }
            >
              {hardware ? "Turn the radio on to see networks in range. Saved profiles reconnect on their own." : "The hardware switch is off, or there is no Wi-Fi card."}
            </EmptyState>
          ) : wifi.isPending ? (
            <>
              <div className="list-head wifi-row">
                <span />
                <span>Network</span>
                <span>Band</span>
                <span />
                <span className="right" />
              </div>
              <div className="list" aria-busy="true" aria-label="Scanning">
                {Array.from({ length: 6 }, (_, i) => (
                  <div key={i} className="row wifi-row">
                    <Skeleton w={18} h={12} />
                    <Skeleton w={`${45 + ((i * 23) % 40)}%`} />
                    <Skeleton w={70} h={12} />
                    <Skeleton w={16} h={16} />
                    <span />
                  </div>
                ))}
              </div>
            </>
          ) : wifi.isError ? (
            <EmptyState error icon="warning-circle" title="Could not list networks" className="mt-4" actions={<button type="button" className="btn" onClick={() => void wifi.refetch()}>Try again</button>}>
              {(wifi.error as Error).message}
            </EmptyState>
          ) : !all.length ? (
            <EmptyState
              icon="cell-signal-none"
              title="Nothing in range"
              className="mt-4"
              actions={
                <button type="button" className="btn btn-primary" onClick={() => void rescan.run()} disabled={rescan.pending}>
                  <Icon name="arrows-clockwise" />
                  Rescan
                </button>
              }
            >
              No access points answered the last scan. Move closer to the router or scan again.
            </EmptyState>
          ) : (
            <>
              <div className="list-head wifi-row">
                <span />
                <span>Network</span>
                <span>Band</span>
                <span />
                <span className="right" />
              </div>
              <div className="list" ref={listRef} onKeyDown={onListKey} role="listbox" aria-label="Networks in range" tabIndex={-1}>
                {shown.map((n, i) => (
                  <WifiRow key={`${n.ssid}|${n.device}`} n={n} selected={n.ssid === selected} fresh={fresh.has(n.ssid)} index={i} onSelect={() => setSelected(n.ssid)} />
                ))}
                {!shown.length && (
                  <EmptyState title="No match" className="mt-3" style={{ padding: "var(--s-6)" }}>
                    Nothing in range matches the filter.
                  </EmptyState>
                )}
              </div>
            </>
          )}
        </div>
        <aside className="detail" aria-label="Network details">
          {current && radioOn ? (
            <WifiDetail key={current.ssid} network={current} device={wifiDevice} />
          ) : (
            <EmptyState title="Nothing selected" style={{ padding: "var(--s-8) var(--s-4)" }}>
              Pick a network to see its details and connect.
            </EmptyState>
          )}
        </aside>
      </div>
    </>
  );
}

function WifiRow({ n, selected, fresh, index, onSelect }: { n: WifiNetwork; selected: boolean; fresh: boolean; index: number; onSelect: () => void }) {
  return (
    <button type="button" role="option" className={`row wifi-row${fresh ? " enter" : ""}`} aria-selected={selected} onClick={onSelect} style={fresh ? { animationDelay: `${index * 24}ms` } : undefined}>
      <Sig level={sigLevel(n.strength)} label={`Signal ${n.strength}%`} />
      <span className="primary">{n.ssid}</span>
      <span className="secondary mono">
        {bandLabel(n.band, n.frequency_mhz)}
        <span className="muted"> · </span>
        {n.channel}
      </span>
      {isSecured(n.security) ? <Icon name="lock-simple" className="lead" tone="sub" label="Secured" /> : <span className="ic" aria-hidden="true" />}
      <span className="trail">
        {n.active ? (
          <Badge tone="ok" icon="check">
            Connected
          </Badge>
        ) : n.known ? (
          <Tag title="Saved profile">saved</Tag>
        ) : null}
      </span>
    </button>
  );
}
