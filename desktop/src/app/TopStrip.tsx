import { Icon } from "@/components/Icon";
import { Badge, Dot, Skeleton } from "@/components/ui";
import { internetLabel, qualityBadge, useConnection } from "@/lib/connection";
import { useUI } from "@/state/ui";

export function TopStrip() {
  const c = useConnection();
  const setPaletteOpen = useUI((s) => s.setPaletteOpen);
  const net = internetLabel(c.connectivity, c.connected);
  const q = qualityBadge(c.monitor);

  return (
    <header className="top" data-tauri-drag-region>
      <div className="crumb">
        {c.loading ? (
          <>
            <Icon name="wifi-high" tone="muted" />
            <Skeleton w={120} />
          </>
        ) : !c.connected ? (
          <>
            <Icon name={c.kind === "none" && !c.wifiEnabled ? "wifi-slash" : "plugs-connected"} tone="muted" label="Not connected" />
            <span className="name sub">Not connected</span>
          </>
        ) : (
          <>
            <Icon name={c.kind === "wifi" ? "wifi-high" : c.kind === "ethernet" ? "plugs-connected" : "network"} tone={c.kind === "wifi" ? "wifi" : "sub"} label={c.kind === "wifi" ? "Wi-Fi" : c.kind === "ethernet" ? "Wired" : "Network"} />
            <span className="name">{c.name}</span>
            {c.cidr && <span className="ip mono">{c.cidr}</span>}
          </>
        )}
      </div>
      <span className="sep" />
      <span className="status">
        <Dot tone={net.tone} />
        {net.text}
      </span>
      <span className="sep" />
      <span className="status">
        <Badge tone={q.tone} icon={q.icon}>
          {q.text}
        </Badge>
      </span>
      <div className="right">
        <button className="search-btn" type="button" onClick={() => setPaletteOpen(true)} aria-keyshortcuts="Control+K" aria-label="Search or run a command">
          <Icon name="magnifying-glass" />
          <span>Search or run a command</span>
          <span className="keys" aria-hidden="true">
            <kbd>Ctrl</kbd>
            <kbd>K</kbd>
          </span>
        </button>
      </div>
    </header>
  );
}
