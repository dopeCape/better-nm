import { useCallback, useMemo, useState } from "react";
import { actions, useAction } from "@/api/actions";
import { qk, useVpn } from "@/api/queries";
import type { TailscalePeer, VPN } from "@/api/types";
import { Icon } from "@/components/Icon";
import { Banner, CopyButton, Dot, EmptyState, Field, Keys, PageHead, SectionHead, Select, Skeleton, SwitchRow, Tag } from "@/components/ui";
import type { IconName } from "@/design/icon-names";
import { useHotkey } from "@/app/hotkeys";
import { fmtAgeSince } from "@/lib/format";
import { shell } from "@/shell";
import { ui } from "@/state/ui";
import { VpnRow } from "./vpn/VpnRow";

export function useAddVpnFromFile() {
  const importVpn = useAction(actions.vpnImport, {
    invalidate: [qk.vpn, qk.profiles],
    label: "Import",
    onSuccess: (r) => ui.toast({ tone: "ok", title: `Added ${r.name}`, detail: `${r.kind} profile ${r.uuid.slice(0, 8)}` }),
  });
  const run = useCallback(async () => {
    let picked;
    try {
      picked = await shell.pickVpnFile();
    } catch (e) {
      ui.error("Could not open the file picker", String(e));
      return;
    }
    if (!picked) return;
    const kind = /\.ovpn$/i.test(picked.name) ? "openvpn" : "wireguard";
    const name = picked.name.replace(/\.(ovpn|conf)$/i, "");
    await importVpn.run({ kind, name, content: picked.content });
  }, [importVpn]);
  return { run, pending: importVpn.pending };
}

export function Vpn() {
  const vpn = useVpn();
  const add = useAddVpnFromFile();
  useHotkey("a", add.run);

  const list = vpn.data ?? [];
  const ts = list.find((v) => v.backend === "tailscale");

  return (
    <>
      <PageHead
        title="VPN"
        sub="One switch per tunnel, whatever runs it."
        actions={
          <button type="button" className="btn" onClick={() => void add.run()} disabled={add.pending}>
            <Icon name="file-arrow-up" />
            Add from file
          </button>
        }
      />
      {vpn.isPending ? (
        <div className="list" aria-busy="true">
          {[0, 1, 2].map((i) => (
            <div key={i} className="row two vpn-row">
              <Skeleton w={16} h={16} />
              <div>
                <Skeleton w={140} />
                <Skeleton w={200} h={12} style={{ marginTop: 4 }} />
              </div>
              <Skeleton w={120} h={20} />
            </div>
          ))}
        </div>
      ) : vpn.isError ? (
        <EmptyState error icon="warning-circle" title="Could not list VPNs" actions={<button type="button" className="btn" onClick={() => void vpn.refetch()}>Try again</button>}>
          {String((vpn.error as Error).message)}
        </EmptyState>
      ) : (
        <div className="list">
          {list.map((v) => (
            <VpnRow key={v.id} vpn={v} />
          ))}
          <button type="button" className="row vpn-add" onClick={() => void add.run()} disabled={add.pending}>
            <Icon name="plus" />
            <span className="sub">
              Add a <span className="mono">.ovpn</span> or WireGuard <span className="mono">.conf</span> from a file
            </span>
            <span className="trail">
              <Keys keys={["A"]} />
            </span>
          </button>
        </div>
      )}
      {ts && <TailscaleSection vpn={ts} />}
    </>
  );
}

function peerIcon(p: TailscalePeer): IconName {
  const os = (p.os ?? "").toLowerCase();
  if (os.includes("android") || os.includes("ios")) return "device-mobile";
  if (os.includes("linux") || os.includes("freebsd")) return "desktop";
  return "laptop";
}

function TailscaleSection({ vpn }: { vpn: VPN }) {
  const t = vpn.tailscale;
  const needsSetup = vpn.state === "needs-setup" || (t !== undefined && !t.operator_ok) || !vpn.writable;
  const peers = useMemo(() => [...(t?.peers ?? [])].sort((a, b) => Number(b.online) - Number(a.online) || a.name.localeCompare(b.name)), [t?.peers]);
  const exitOptions = peers.filter((p) => p.exit_node_option);
  const online = peers.filter((p) => p.online).length;
  const [exitBusy, setExitBusy] = useState(false);

  const setExit = useAction(
    async (peer: string, allowLan: boolean) => {
      setExitBusy(true);
      try {
        return await actions.tailscaleExitNode(peer, allowLan);
      } finally {
        setExitBusy(false);
      }
    },
    { invalidate: [qk.vpn], label: "Exit node" },
  );
  const setDns = useAction(actions.tailscaleAcceptDns, { invalidate: [qk.vpn], label: "Accept DNS" });
  const login = useAction(actions.tailscaleLogin, {
    label: "Log in",
    onSuccess: (r) => {
      if (r?.url) void shell.openUrl(r.url);
    },
  });
  const logout = useAction(actions.tailscaleLogout, { invalidate: [qk.vpn], label: "Log out" });
  const recheck = useAction(async () => undefined, { invalidate: [qk.vpn] });

  const exitValue = t?.exit_node_on && t.exit_node_id ? t.exit_node_id : "";
  // The daemon's hint carries the exact command for this user; fall back to the generic one.
  const operatorCmd = vpn.detail?.match(/sudo tailscale set --operator=\S+/)?.[0] ?? "sudo tailscale set --operator=$USER";
  const hintText = vpn.detail && !vpn.detail.includes("sudo tailscale set") ? vpn.detail : "Let your user drive tailscaled without sudo, once:";
  const controlDefault = !t?.control_url || /controlplane\.tailscale\.com/.test(t.control_url);
  const adminUrl = controlDefault ? "https://login.tailscale.com/admin/machines" : t!.control_url!;

  return (
    <section className="section" id="ts-section">
      <SectionHead
        title="Tailscale"
        actions={
          <>
            {t?.tailnet && (
              <span className="sub sm">
                tailnet <span className="mono">{t.tailnet}</span>
              </span>
            )}
            <button type="button" className="btn btn-ghost" onClick={() => void logout.run()} disabled={needsSetup || logout.pending}>
              <Icon name="sign-out" />
              Log out
            </button>
          </>
        }
      />
      {needsSetup && (
        <Banner
          tone="warn"
          icon="shield-warning-fill"
          title="Tailscale needs setup"
          className="mb-4"
          actions={
            <button type="button" className="btn" onClick={() => void recheck.run()}>
              <Icon name="arrows-clockwise" />
              Check again
            </button>
          }
        >
          {hintText}
          <div className="cmd mt-2">
            <code>{operatorCmd}</code>
            <CopyButton text={operatorCmd} label="Copy command" />
          </div>
        </Banner>
      )}
      <div className="cols cols-1-1">
        <div>
          <dl className="kv tight">
            <dt>This machine</dt>
            <dd>
              <span className="mono">{t?.self_name ?? ""}</span>
              {t?.backend_state && <span className="sub">{t.backend_state.toLowerCase()}</span>}
            </dd>
            <dt>Tailscale IP</dt>
            <dd>
              <span className="mono">{t?.self_ips?.[0] ?? ""}</span>
              {t?.self_ips?.[0] && <CopyButton text={t.self_ips[0]} label="Copy Tailscale IP" />}
            </dd>
            {t?.self_name && t.magic_dns_suffix && (
              <>
                <dt>MagicDNS</dt>
                <dd>
                  <span className="mono">
                    {t.self_name}.{t.magic_dns_suffix}
                  </span>
                </dd>
              </>
            )}
            {t?.version && (
              <>
                <dt>Version</dt>
                <dd>
                  <span className="mono">{t.version}</span>
                </dd>
              </>
            )}
          </dl>
          <div className="section" style={{ marginTop: "var(--s-4)", paddingTop: "var(--s-4)" }}>
            <SectionHead as="h3" title="Peers" sub={`${online} online`} />
            {peers.length ? (
              <div className="list">
                {peers.map((p) => (
                  <div key={p.id} className="row peer-row">
                    <Icon name={peerIcon(p)} className="lead" tone="sub" />
                    <span className="primary">
                      {p.name} {p.exit_node_option && <Tag>exit node</Tag>}
                    </span>
                    <span className="secondary mono">{p.ips[0] ?? ""}</span>
                    <Dot tone={p.online ? "ok" : undefined} title={p.online ? "online" : `offline, last seen ${fmtAgeSince(p.last_seen) || "unknown"}`} />
                  </div>
                ))}
              </div>
            ) : (
              <EmptyState title="No peers" style={{ padding: "var(--s-6)" }}>
                Devices you add to the tailnet show up here.
              </EmptyState>
            )}
          </div>
        </div>
        <div>
          <Field label="Exit node" htmlFor="ts-exit" help="Route all traffic through a peer that offers it.">
            <Select
              id="ts-exit"
              value={exitValue}
              disabled={needsSetup || exitBusy || !exitOptions.length}
              onChange={(v) => void setExit.run(v, t?.exit_node_allow_lan ?? false)}
              options={[{ value: "", label: exitOptions.length ? "None (direct)" : "No peer offers one" }, ...exitOptions.map((p) => ({ value: p.id, label: p.online ? p.name : `${p.name} (offline)` }))]}
            />
          </Field>
          <div className="list mt-4">
            <SwitchRow label="Allow LAN access" hint="Keep talking to printers and NAS while an exit node is on" checked={t?.exit_node_allow_lan ?? false} disabled={needsSetup || !exitValue} busy={exitBusy} onChange={(next) => void setExit.run(exitValue, next)} />
            <SwitchRow label="Accept DNS" hint="Use MagicDNS and the tailnet's nameservers" checked={t?.accept_dns ?? false} disabled={needsSetup} busy={setDns.pending} onChange={(next) => void setDns.run(next)} />
          </div>
          <div className="flex mt-4">
            <button type="button" className="btn" onClick={() => void shell.openUrl(adminUrl)}>
              <Icon name="arrow-square-out" />
              Open admin console
            </button>
            <button type="button" className="btn btn-ghost" onClick={() => void login.run()} disabled={login.pending}>
              <Icon name="sign-in" />
              {vpn.state === "needs-auth" ? "Log in" : "Log in again"}
            </button>
          </div>
        </div>
      </div>
    </section>
  );
}
