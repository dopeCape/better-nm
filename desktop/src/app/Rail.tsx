import { Icon } from "@/components/Icon";
import { Dot } from "@/components/ui";
import type { IconName } from "@/design/icon-names";
import { SECTIONS, type Section } from "@/config/types";
import { useConnection } from "@/lib/connection";
import { ipOnly } from "@/lib/format";
import { useUI } from "@/state/ui";

export const SECTION_META: Record<Section, { label: string; icon: IconName }> = {
  overview: { label: "Overview", icon: "house" },
  wifi: { label: "Wi-Fi", icon: "wifi-high" },
  vpn: { label: "VPN", icon: "shield-check" },
  quality: { label: "Quality", icon: "pulse" },
  speed: { label: "Speed", icon: "gauge" },
  devices: { label: "Devices", icon: "network" },
  settings: { label: "Settings", icon: "gear-six" },
};

export function Rail() {
  const section = useUI((s) => s.section);
  const setSection = useUI((s) => s.setSection);
  const c = useConnection();

  return (
    <aside className="rail">
      <div className="wordmark">
        <span className="mark">
          <Icon name="broadcast" />
        </span>
        bnm
      </div>
      <nav className="nav" aria-label="Sections">
        {SECTIONS.map((s, i) => {
          const m = SECTION_META[s];
          return (
            <button key={s} type="button" className="nav-item" aria-current={section === s ? "page" : undefined} onClick={() => setSection(s)}>
              <Icon name={m.icon} />
              <span>{m.label}</span>
              <kbd aria-hidden="true">{i + 1}</kbd>
            </button>
          );
        })}
      </nav>
      <div className="rail-spacer" />
      <ConnPill c={c} />
    </aside>
  );
}

function ConnPill({ c }: { c: ReturnType<typeof useConnection> }) {
  if (c.loading) {
    return (
      <div className="conn-pill" aria-busy="true">
        <Dot />
        <span className="name sub">Connecting to bnmd</span>
        <span className="meta">&nbsp;</span>
      </div>
    );
  }
  if (!c.connected) {
    return (
      <div className="conn-pill" title="Not connected">
        <Dot />
        <span className="name sub">Not connected</span>
        <span className="meta">{c.kind === "none" && !c.wifiEnabled ? "Wi-Fi off" : "No network"}</span>
      </div>
    );
  }
  const vpn = c.vpnUp[0];
  return (
    <div className="conn-pill" title={`Connected to ${c.name}`}>
      <Dot tone={c.connectivity === "full" ? "ok" : "warn"} />
      <span className="name">{c.name}</span>
      <span className="meta">
        <span className="mono">{ipOnly(c.cidr)}</span>
        {vpn && <Icon name="shield-check" tone="vpn" label={`${vpn.name} up`} />}
      </span>
    </div>
  );
}
