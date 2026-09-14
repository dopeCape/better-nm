// Devices: physical interfaces first, the runtime plumbing (bridges, veths,
// tunnels) collapsed under "Virtual" with an owner tag. Rows follow the Wi-Fi list.
import { useMemo, useState } from "react";
import { actions, useAction } from "@/api/actions";
import { qk, useDevices, useProfiles } from "@/api/queries";
import type { Device, DeviceState, Profile } from "@/api/types";
import { Icon, type Tone } from "@/components/Icon";
import { Badge, EmptyState, PageHead, SectionHead, Skeleton, Switch, Tag } from "@/components/ui";
import type { IconName } from "@/design/icon-names";
import { driverLabel } from "@/lib/format";

function deviceIcon(d: Device): IconName {
  switch (d.kind) {
    case "wifi":
      return "wifi-high";
    case "ethernet":
      return "plugs-connected";
    case "wireguard":
    case "tun":
      return "shield-check";
    case "bridge":
      return "network";
    case "veth":
      return "link";
    case "vlan":
      return "hard-drives";
    default:
      return "network";
  }
}

function stateBadge(s: DeviceState): { tone?: Tone; text: string; icon?: IconName } {
  switch (s) {
    case "connected":
      return { tone: "ok", text: "Connected", icon: "check" };
    case "connecting":
      return { tone: "accent", text: "Connecting" };
    case "external":
      return { tone: "ok", text: "Up" };
    case "disconnected":
      return { text: "Down" };
    case "unavailable":
      return { text: "No link" };
    case "unmanaged":
      return { text: "Unmanaged" };
    case "failed":
      return { tone: "error", text: "Failed", icon: "warning-circle" };
    default:
      return { text: s };
  }
}

export function Devices() {
  const devices = useDevices();
  const profiles = useProfiles();
  const [showVirtual, setShowVirtual] = useState(false);

  const { physical, virtual } = useMemo(() => {
    const all = devices.data ?? [];
    const order = (d: Device) => (d.state === "connected" ? 0 : d.state === "external" ? 1 : d.state === "connecting" ? 2 : d.state === "disconnected" ? 3 : 4);
    const sorted = [...all].sort((a, b) => order(a) - order(b) || a.name.localeCompare(b.name));
    return {
      physical: sorted.filter((d) => d.class === "physical"),
      virtual: sorted.filter((d) => d.class === "infra"),
    };
  }, [devices.data]);

  const up = physical.filter((d) => d.state === "connected" || d.state === "external").length;

  return (
    <>
      <PageHead title="Devices" sub={devices.isPending ? <Skeleton w={220} /> : `${physical.length} physical interface${physical.length === 1 ? "" : "s"}, ${up} up. ${virtual.length} virtual.`} />
      {devices.isPending ? (
        <>
          <ListHead />
          <div className="list" aria-busy="true">
            {[0, 1, 2].map((i) => (
              <div key={i} className="row dev-row">
                <Skeleton w={16} h={16} />
                <Skeleton w={`${40 + i * 12}%`} />
                <Skeleton w={110} h={12} />
                <Skeleton w={90} h={20} />
              </div>
            ))}
          </div>
        </>
      ) : devices.isError ? (
        <EmptyState error icon="warning-circle" title="Could not list devices" actions={<button type="button" className="btn" onClick={() => void devices.refetch()}>Try again</button>}>
          {(devices.error as Error).message}
        </EmptyState>
      ) : !physical.length ? (
        <EmptyState icon="network" title="No network hardware">
          NetworkManager does not see a Wi-Fi card or an ethernet port.
        </EmptyState>
      ) : (
        <>
          <ListHead />
          <div className="list">
            {physical.map((d) => (
              <DeviceRow key={d.name} d={d} profiles={profiles.data ?? []} />
            ))}
          </div>
        </>
      )}

      {virtual.length > 0 && (
        <section className="section">
          <SectionHead
            title={
              <button type="button" className="disclosure" aria-expanded={showVirtual} onClick={() => setShowVirtual((v) => !v)}>
                <Icon name="caret-right" className={showVirtual ? "caret open" : "caret"} />
                Virtual
              </button>
            }
            sub={`${virtual.length} owned by runtimes`}
          />
          {showVirtual && (
            <div className="list">
              {virtual.map((d) => (
                <DeviceRow key={d.name} d={d} profiles={[]} virtual />
              ))}
            </div>
          )}
        </section>
      )}
    </>
  );
}

function ListHead() {
  return (
    <div className="list-head dev-row">
      <span />
      <span>Interface</span>
      <span>Address</span>
      <span className="right" />
    </div>
  );
}

function DeviceRow({ d, profiles, virtual }: { d: Device; profiles: Profile[]; virtual?: boolean }) {
  const badge = stateBadge(d.state);
  const wiredProfile = d.kind === "ethernet" ? profiles.find((p) => p.type === "ethernet" && (!p.interface_name || p.interface_name === d.name)) : undefined;
  const canToggle = d.kind === "ethernet" && d.managed && (d.state === "connected" || (d.state === "disconnected" && !!wiredProfile));
  const toggle = useAction(
    async (on: boolean) => {
      if (on) return actions.profileActivate(wiredProfile!.uuid, d.name);
      return actions.profileDeactivate(d.active_uuid!);
    },
    { invalidate: [qk.devices, qk.status, qk.active], label: d.name },
  );
  const tone: Tone | "sub" = d.kind === "wifi" && d.state === "connected" ? "wifi" : d.kind === "tun" && d.state === "external" ? "vpn" : "sub";
  return (
    <div className="row two dev-row">
      <Icon name={deviceIcon(d)} tone={tone} className={tone === "sub" ? "lead" : undefined} />
      <div className="grow">
        <div className="primary">
          <span className="mono">{d.name}</span>
          {virtual && d.owner && <Tag>{d.owner}</Tag>}
          {!virtual && d.kind !== "wifi" && d.kind !== "ethernet" && <Tag>{d.kind}</Tag>}
        </div>
        <div className="secondary">
          {d.active_name ? (
            <>
              {d.active_name}
              {driverLabel(d.driver) && !virtual && ` · ${driverLabel(d.driver)}`}
            </>
          ) : (
            driverLabel(d.driver) || d.kind
          )}
          {d.hwaddr && d.hwaddr !== "00:00:00:00:00:00" && (
            <>
              {" "}
              <span className="mono muted">{d.hwaddr.toLowerCase()}</span>
            </>
          )}
        </div>
      </div>
      <span className="secondary mono">{d.ipv4?.[0] ?? (d.ipv6?.find((a) => !a.startsWith("fe80")) ?? "")}</span>
      <div className="trail">
        <Badge tone={badge.tone} icon={badge.icon}>
          {badge.text}
        </Badge>
        {canToggle && <Switch label={`${d.name} link`} checked={d.state === "connected"} busy={toggle.pending} onChange={(on) => void toggle.run(on)} />}
      </div>
    </div>
  );
}
