import { useState } from "react";
import { actions, useAction } from "@/api/actions";
import { qk } from "@/api/queries";
import type { VPN } from "@/api/types";
import { Icon, type Tone } from "@/components/Icon";
import { Badge, Switch, Tag } from "@/components/ui";
import type { IconName } from "@/design/icon-names";
import { fmtAgeSince, fmtWhen, isZeroTime } from "@/lib/format";

export function vpnBadge(v: VPN): { tone?: Tone; text: string } {
  switch (v.state) {
    case "connected":
      return { tone: "ok", text: "Connected" };
    case "connecting":
      return { tone: "accent", text: "Connecting" };
    case "error":
      return { tone: "error", text: "Error" };
    case "needs-setup":
      return { tone: "warn", text: "Needs setup" };
    case "needs-auth":
      return { tone: "warn", text: "Needs login" };
    case "unavailable":
      return { text: "Unavailable" };
    default:
      return { text: "Off" };
  }
}

export function vpnSecondary(v: VPN): React.ReactNode {
  if (v.state === "error" && v.error) return v.error;
  if (v.state === "needs-setup") return "Installed, but bnm may not drive it yet";
  if (v.state === "unavailable") return v.detail || "Backend not running";
  if (v.backend === "tailscale" && v.tailscale) {
    const t = v.tailscale;
    const ip = t.self_ips?.[0];
    const since = !isZeroTime(v.since) && v.state === "connected" ? fmtWhen(v.since) : "";
    const exit = v.state === "connected" ? (t.exit_node_on && t.exit_node_name ? `exit node ${t.exit_node_name}` : "no exit node") : "";
    return (
      <>
        {ip && <span className="mono">{ip}</span>}
        {ip && since && " · "}
        {since && (
          <>
            up since <span className="mono">{since}</span>
          </>
        )}
        {exit && (ip || since) && ", "}
        {exit}
      </>
    );
  }
  if (v.backend === "wireguard" && v.wireguard) {
    const w = v.wireguard;
    const hs = !isZeroTime(w.last_handshake) ? fmtAgeSince(w.last_handshake) : "";
    return (
      <>
        <span className="mono">{w.interface_name}</span>
        {hs && (
          <>
            {" · "}last handshake <span className="mono">{hs}</span> ago
          </>
        )}
        {!hs && w.peers?.[0]?.endpoint && (
          <>
            {" · "}
            <span className="mono">{w.peers[0].endpoint}</span>
          </>
        )}
      </>
    );
  }
  if (v.nm_vpn) {
    return (
      <>
        {v.nm_vpn.gateway && <span className="mono">{v.nm_vpn.gateway}</span>}
        {v.nm_vpn.username && (
          <>
            {v.nm_vpn.gateway && " · "}
            {v.nm_vpn.username}
          </>
        )}
      </>
    );
  }
  return v.detail ?? "";
}

export function vpnIcon(v: VPN): { name: IconName; tone?: Tone | "sub" } {
  if (v.state === "connected") return { name: "shield-check-fill", tone: "vpn" };
  if (v.state === "needs-setup" || v.state === "needs-auth") return { name: "shield-warning", tone: "warn" };
  if (v.state === "error") return { name: "shield-warning", tone: "error" };
  return { name: "shield-check", tone: "sub" };
}

/** A VPN row with its switch: optimistic, reverts with the daemon's hint on error. */
export function VpnRow({ vpn }: { vpn: VPN; compact?: boolean }) {
  const [optimistic, setOptimistic] = useState<boolean | null>(null);
  // The optimistic value stands until the daemon reports a new state (or the call fails).
  const [seenState, setSeenState] = useState(vpn.state);
  if (vpn.state !== seenState) {
    setSeenState(vpn.state);
    setOptimistic(null);
  }
  const toggle = useAction(
    async (on: boolean) => {
      if (on) return actions.vpnConnect(vpn.id);
      return actions.vpnDisconnect(vpn.id);
    },
    {
      invalidate: [qk.vpn, qk.active, qk.status],
      label: vpn.name,
      onError: () => setOptimistic(null),
    },
  );
  const badge = vpnBadge(vpn);
  const ic = vpnIcon(vpn);
  const on = vpn.state === "connected" || vpn.state === "connecting";
  const checked = optimistic ?? on;
  const canToggle = vpn.writable && vpn.state !== "needs-setup" && vpn.state !== "unavailable";

  return (
    <div className="row two vpn-row" aria-busy={toggle.pending || undefined}>
      <Icon name={ic.name} tone={ic.tone} className={ic.tone === "sub" ? "lead" : undefined} />
      <div className="grow">
        <div className="primary">
          {vpn.name} <Tag>{vpn.kind}</Tag>
        </div>
        <div className="secondary">{vpnSecondary(vpn)}</div>
      </div>
      <div className="trail">
        <Badge tone={badge.tone}>{badge.text}</Badge>
        <Switch
          label={vpn.name}
          checked={checked}
          disabled={!canToggle}
          busy={toggle.pending}
          onChange={(next) => {
            setOptimistic(next);
            void toggle.run(next);
          }}
        />
      </div>
    </div>
  );
}
