// Command palette (08-command-palette): Ctrl K, fuzzy over actions, networks
// and sections, keyboard driven.
import { useEffect, useMemo, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { actions } from "@/api/actions";
import { describeError } from "@/api/client";
import { qk, useMonitor, useStatus, useVpn, useWifi } from "@/api/queries";
import { Dialog } from "@/components/Dialog";
import { Icon } from "@/components/Icon";
import { Tag } from "@/components/ui";
import type { IconName } from "@/design/icon-names";
import { SECTIONS, type Section } from "@/config/types";
import { bandLabel, securityTag } from "@/lib/format";
import { fuzzy, segments } from "@/lib/fuzzy";
import { shell } from "@/shell";
import { ui, useUI } from "@/state/ui";
import { GO_CHORDS } from "./hotkeys";
import { SECTION_META } from "./Rail";
import { useAddVpnFromFile } from "@/views/Vpn";

export interface PaletteItem {
  id: string;
  group: "Actions" | "Networks" | "Go to";
  icon: IconName;
  label: string;
  hint?: string;
  hintMono?: boolean;
  tag?: string;
  keys?: string[];
  run: () => void | Promise<unknown>;
}

export function filterItems(items: PaletteItem[], query: string): { item: PaletteItem; positions: number[] }[] {
  const q = query.trim();
  const scored = items
    .map((item) => {
      const m = fuzzy(q, item.label);
      return m ? { item, score: m.score, positions: m.positions } : null;
    })
    .filter((x): x is { item: PaletteItem; score: number; positions: number[] } => x !== null);
  if (q) scored.sort((a, b) => b.score - a.score);
  return scored.map(({ item, positions }) => ({ item, positions }));
}

export function usePaletteItems(): PaletteItem[] {
  const qc = useQueryClient();
  const status = useStatus();
  const vpn = useVpn();
  const wifi = useWifi();
  const monitor = useMonitor();
  const setSection = useUI((s) => s.setSection);
  const setWifiSelected = useUI((s) => s.setWifiSelected);
  const trayPresent = useUI((s) => s.trayPresent);
  const add = useAddVpnFromFile();

  return useMemo(() => {
    const items: PaletteItem[] = [];
    const wrap = (label: string, fn: () => Promise<unknown>, invalidate: readonly (readonly unknown[])[] = []) => async () => {
      try {
        await fn();
        for (const k of invalidate) void qc.invalidateQueries({ queryKey: k as unknown[] });
      } catch (e) {
        const d = describeError(e);
        ui.error(`${label}: ${d.title}`, d.detail);
      }
    };

    for (const v of vpn.data ?? []) {
      const on = v.state === "connected" || v.state === "connecting";
      const can = v.writable && v.state !== "needs-setup" && v.state !== "unavailable";
      if (!can) continue;
      items.push({
        id: `vpn:${v.id}`,
        group: "Actions",
        icon: "shield-check",
        label: `Turn ${v.name} ${on ? "off" : "on"}`,
        hint: v.state,
        run: wrap(v.name, () => (on ? actions.vpnDisconnect(v.id) : actions.vpnConnect(v.id)), [qk.vpn]),
      });
      const ts = v.tailscale;
      if (ts && v.state === "connected") {
        const exits = (ts.peers ?? []).filter((p) => p.exit_node_option);
        if (ts.exit_node_on) {
          items.push({ id: "ts-exit-off", group: "Actions", icon: "globe", label: "Stop using the Tailscale exit node", hint: ts.exit_node_name, run: wrap("Exit node", () => actions.tailscaleExitNode("", ts.exit_node_allow_lan), [qk.vpn]) });
        } else {
          for (const p of exits) items.push({ id: `ts-exit:${p.id}`, group: "Actions", icon: "globe", label: `Use Tailscale exit node ${p.name}`, hint: p.online ? "online" : "offline", run: wrap("Exit node", () => actions.tailscaleExitNode(p.id, ts.exit_node_allow_lan), [qk.vpn]) });
        }
      }
    }

    const wifiOn = status.data?.wifi_enabled ?? true;
    items.push({ id: "wifi-toggle", group: "Actions", icon: wifiOn ? "wifi-slash" : "wifi-high", label: wifiOn ? "Turn Wi-Fi off" : "Turn Wi-Fi on", run: wrap("Wi-Fi", () => actions.wifiEnabled(!wifiOn), [qk.status, qk.wifi]) });
    if (wifiOn) items.push({ id: "rescan", group: "Actions", icon: "arrows-clockwise", label: "Rescan Wi-Fi", keys: ["R"], run: wrap("Rescan", () => actions.wifiScan()) });
    items.push({ id: "speed", group: "Actions", icon: "gauge", label: "Run speed test", keys: ["5", "↵"], run: () => setSection("speed") });
    items.push({ id: "speed-quick", group: "Actions", icon: "lightning", label: "Quick speed test", keys: ["5", "Q"], run: () => setSection("speed") });
    const paused = monitor.data?.paused ?? false;
    items.push({ id: "monitor-pause", group: "Actions", icon: paused ? "play" : "pause", label: paused ? "Resume monitoring" : "Pause monitoring", keys: ["P"], run: wrap("Monitoring", () => (paused ? actions.monitorResume() : actions.monitorPause()), [qk.monitor]) });
    items.push({ id: "baseline-reset", group: "Actions", icon: "arrow-counter-clockwise", label: "Reset baseline", run: wrap("Reset baseline", () => actions.monitorReset(), [qk.monitor]) });
    items.push({ id: "vpn-add", group: "Actions", icon: "file-arrow-up", label: "Add VPN from file", keys: ["A"], run: () => add.run() });
    items.push({ id: "notify-test", group: "Actions", icon: "bell", label: "Send test notification", run: wrap("Test notification", () => actions.notifyTest()) });
    items.push({ id: "open-config", group: "Actions", icon: "gear-six", label: "Open desktop config folder", run: wrap("Config", () => shell.configReveal()) });
    // The window is frameless: these are its close button.
    if (trayPresent) items.push({ id: "hide", group: "Actions", icon: "x", label: "Hide to tray", run: wrap("Hide", () => shell.windowHide()) });
    items.push({ id: "quit", group: "Actions", icon: "sign-out", label: "Quit bnm", run: wrap("Quit", () => shell.windowClose()) });

    for (const n of wifi.data ?? []) {
      if (n.active) continue;
      items.push({
        id: `net:${n.ssid}`,
        group: "Networks",
        icon: "wifi-high",
        label: n.ssid,
        hint: `${bandLabel(n.band, n.frequency_mhz)} · ${n.strength}%`,
        hintMono: true,
        tag: securityTag(n.security),
        run: async () => {
          setWifiSelected(n.ssid);
          setSection("wifi");
          if (n.known) {
            try {
              await actions.wifiConnect({ ssid: n.ssid });
              void qc.invalidateQueries({ queryKey: qk.wifi });
            } catch (e) {
              const d = describeError(e);
              ui.error(`${n.ssid}: ${d.title}`, d.detail);
            }
          }
        },
      });
    }

    const chordFor = (s: Section) => Object.entries(GO_CHORDS).find(([, v]) => v === s)?.[0]?.toUpperCase() ?? "";
    for (const s of SECTIONS) items.push({ id: `go:${s}`, group: "Go to", icon: SECTION_META[s].icon, label: SECTION_META[s].label, keys: ["G", chordFor(s)], run: () => setSection(s) });
    return items;
  }, [qc, status.data, vpn.data, wifi.data, monitor.data, setSection, setWifiSelected, trayPresent, add]);
}

export function Palette() {
  const open = useUI((s) => s.paletteOpen);
  if (!open) return null;
  return <PaletteBody />;
}

/** Mounted only while open, so every opening starts with an empty query. */
function PaletteBody() {
  const setOpen = useUI((s) => s.setPaletteOpen);
  const items = usePaletteItems();
  const [query, setQueryState] = useState("");
  const [sel, setSel] = useState(0);
  const setQuery = (q: string) => {
    setQueryState(q);
    setSel(0);
  };
  const inputRef = useRef<HTMLInputElement>(null);
  const listRef = useRef<HTMLDivElement>(null);
  const titleId = "palette-title";

  const results = useMemo(() => filterItems(items, query), [items, query]);
  const groups = useMemo(() => {
    const g = new Map<PaletteItem["group"], { item: PaletteItem; positions: number[] }[]>();
    for (const r of results) {
      const list = g.get(r.item.group) ?? [];
      list.push(r);
      g.set(r.item.group, list);
    }
    return (["Actions", "Networks", "Go to"] as const).filter((k) => g.has(k)).map((k) => ({ name: k, rows: g.get(k)! }));
  }, [results]);
  const flat = useMemo(() => groups.flatMap((g) => g.rows), [groups]);

  useEffect(() => {
    const el = listRef.current?.querySelector<HTMLElement>(`[data-index="${sel}"]`);
    el?.scrollIntoView({ block: "nearest" });
  }, [sel]);

  const run = (i: number) => {
    const r = flat[i];
    if (!r) return;
    setOpen(false);
    void r.item.run();
  };
  const onKey = (e: React.KeyboardEvent) => {
    if (e.key === "ArrowDown") {
      e.preventDefault();
      setSel((s) => Math.min(flat.length - 1, s + 1));
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      setSel((s) => Math.max(0, s - 1));
    } else if (e.key === "Enter") {
      e.preventDefault();
      run(sel);
    } else if (e.key === "Tab") {
      // Tab jumps to the next group.
      e.preventDefault();
      const cur = flat[sel]?.item.group;
      const next = flat.findIndex((r, i) => i > sel && r.item.group !== cur);
      setSel(next >= 0 ? next : 0);
    }
  };

  let index = -1;
  return (
    <Dialog open onClose={() => setOpen(false)} labelledBy={titleId} align="top" className="palette" initialFocus={inputRef}>
      <span id={titleId} className="sr-only">
        Command palette
      </span>
      <div className="search">
        <Icon name="magnifying-glass" />
        <input ref={inputRef} placeholder="Type a command or a network" aria-label="Search" value={query} onChange={(e) => setQuery(e.target.value)} onKeyDown={onKey} role="combobox" aria-expanded="true" aria-controls="palette-list" aria-activedescendant={flat[sel] ? `palette-item-${sel}` : undefined} aria-autocomplete="list" />
        <kbd>Esc</kbd>
      </div>
      <div ref={listRef} id="palette-list" role="listbox" aria-label="Results" className="palette-list">
        {groups.map((g) => (
          <div className="group" key={g.name} role="group" aria-label={g.name}>
            <div className="group-title">{g.name}</div>
            {g.rows.map(({ item, positions }) => {
              index += 1;
              const i = index;
              return (
                <button key={item.id} id={`palette-item-${i}`} data-index={i} type="button" role="option" className="item" aria-selected={i === sel} onMouseEnter={() => setSel(i)} onClick={() => run(i)} tabIndex={-1}>
                  <Icon name={item.icon} />
                  <span>
                    {segments(item.label, positions).map((s, j) => (s.hit ? <b key={j}>{s.text}</b> : <span key={j}>{s.text}</span>))}
                  </span>
                  {item.hint && <span className={`hint${item.hintMono ? " mono" : ""}`}>{item.hint}</span>}
                  {item.tag && (
                    <span className="ml-auto">
                      <Tag>{item.tag}</Tag>
                    </span>
                  )}
                  {item.keys && (
                    <span className="keys">
                      {item.keys.map((k) => (
                        <kbd key={k}>{k}</kbd>
                      ))}
                    </span>
                  )}
                  {i === sel && !item.keys && !item.tag && (
                    <span className="keys">
                      <kbd>↵</kbd>
                    </span>
                  )}
                </button>
              );
            })}
          </div>
        ))}
        {!flat.length && (
          <div className="group">
            <div className="palette-empty sub">No match for "{query}".</div>
          </div>
        )}
      </div>
      <div className="foot">
        <span>
          <kbd>↑</kbd>
          <kbd>↓</kbd> move
        </span>
        <span>
          <kbd>↵</kbd> run
        </span>
        <span>
          <kbd>Tab</kbd> next group
        </span>
        <span className="ml-auto">
          {flat.length} result{flat.length === 1 ? "" : "s"}
        </span>
      </div>
    </Dialog>
  );
}
