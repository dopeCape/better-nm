import { useId, useMemo, useState } from "react";
import { actions, useAction } from "@/api/actions";
import { ApiError, describeError } from "@/api/client";
import { qk, useProfile } from "@/api/queries";
import type { IPConfig, Profile, WifiNetwork } from "@/api/types";
import { Icon } from "@/components/Icon";
import { Badge, Field, Keys, SectionHead, Segmented, Sig, Skeleton, SwitchRow } from "@/components/ui";
import { bandLabel, fmtWhen, isSecured, securityLabel, sigLevel } from "@/lib/format";
import { ui, useUI } from "@/state/ui";

/** "Wrong password" when the daemon says the secret was rejected; else its own words. */
export function connectErrorText(err: unknown): string {
  const d = describeError(err);
  const m = `${d.title} ${d.detail ?? ""}`.toLowerCase();
  if (/(secret|password|psk|auth)/.test(m) && /(reject|wrong|invalid|no secrets|cancel|expired|bad)/.test(m)) {
    return "Wrong password. The router rejected it; check for a typo.";
  }
  return d.detail ? `${d.title}. ${d.detail}` : d.title;
}

export function WifiDetail({ network: n, device }: { network: WifiNetwork; device: string }) {
  const profile = useProfile(n.known ? n.profile_uuid : undefined);
  const [password, setPassword] = useState("");
  const [username, setUsername] = useState("");
  const [show, setShow] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const setSelected = useUI((s) => s.setWifiSelected);
  const pskId = useId();
  const userId = useId();

  const secured = isSecured(n.security);
  const eap = n.security === "wpa-eap";
  const needsPassword = !n.known && secured;

  const connect = useAction(actions.wifiConnect, {
    invalidate: [qk.wifi, qk.status, qk.active, qk.profiles],
    silent: true,
    onSuccess: () => {
      setPassword("");
      setError(null);
    },
    onError: (err) => {
      setError(connectErrorText(err));
      if (err instanceof ApiError && err.code === "permission") ui.error(err.message, err.hint);
    },
  });
  const disconnect = useAction(actions.wifiDisconnect, { invalidate: [qk.wifi, qk.status, qk.active], label: "Disconnect" });
  const forget = useAction(actions.wifiForget, {
    invalidate: [qk.wifi, qk.profiles, qk.status],
    label: "Forget",
    onSuccess: () => ui.toast({ tone: "ok", title: `Forgot ${n.ssid}` }),
  });
  const autoconnect = useAction(actions.profileAutoconnect, { invalidate: [qk.profiles, qk.wifi], label: "Autoconnect" });

  const doConnect = () => {
    if (needsPassword && !password) {
      setError(eap ? "Enter the username and password." : "Enter the password.");
      return;
    }
    const req = { ssid: n.ssid, ...(device ? { device } : {}), ...(password ? { password } : {}), ...(eap && username ? { username } : {}) };
    void connect.run(req);
  };

  const sinceText = n.active && profile.data?.timestamp ? fmtWhen(profile.data.timestamp) : "";

  return (
    <>
      <div className="detail-head">
        <div>
          <h2 className="h2">{n.ssid}</h2>
          <div className="sub sm mt-2">
            {n.active ? (
              sinceText ? (
                <>
                  Connected since <span className="mono">{sinceText}</span>
                </>
              ) : (
                "Connected"
              )
            ) : n.known ? (
              profile.data ? (
                `Saved, autoconnect ${profile.data.autoconnect ? "on" : "off"}`
              ) : (
                "Saved"
              )
            ) : (
              "Not saved"
            )}
          </div>
        </div>
        {n.active && (
          <Badge tone="ok" icon="check">
            Connected
          </Badge>
        )}
      </div>

      <dl className="kv tight narrow mt-4">
        <dt>Signal</dt>
        <dd>
          <Sig level={sigLevel(n.strength)} label={`Signal ${n.strength}%`} />
          <span className="mono">{n.strength}%</span>
        </dd>
        <dt>Band</dt>
        <dd>
          <span className="mono">{bandLabel(n.band, n.frequency_mhz)}</span>
          <span className="sub">
            channel <span className="mono">{n.channel}</span>
          </span>
        </dd>
        <dt>Frequency</dt>
        <dd>
          <span className="mono">{n.frequency_mhz} MHz</span>
        </dd>
        <dt>Security</dt>
        <dd>
          {secured && <Icon name="lock-simple" tone="sub" />}
          {securityLabel(n.security)}
        </dd>
        {n.bssids[0] && (
          <>
            <dt>Access point</dt>
            <dd>
              <span className="mono">{n.bssids[0].toLowerCase()}</span>
            </dd>
          </>
        )}
        {n.known && (
          <>
            <dt>Profile</dt>
            <dd>{profile.data ? <span className="sub">Saved, autoconnect {profile.data.autoconnect ? "on" : "off"}</span> : profile.isError ? <span className="sub">Saved</span> : <Skeleton w={140} />}</dd>
          </>
        )}
      </dl>

      {n.known ? (
        <>
          <div className="list mt-2">
            <SwitchRow label="Connect automatically" hint="Join this network whenever it is in range" checked={profile.data?.autoconnect ?? true} disabled={!profile.data} busy={autoconnect.pending} onChange={(on) => void autoconnect.run(n.profile_uuid!, on)} />
          </div>
          <div className="section">
            <SectionHead as="h3" title={secured ? "Password" : "Connection"} />
            {secured && <PasswordField id={pskId} value={password} onChange={setPassword} show={show} setShow={setShow} error={error} onEnter={doConnect} help="Stored in the profile. Enter a new one here if the router changed." />}
            {!secured && error && (
              <span className="error sm flex" role="alert">
                <Icon name="warning-circle" />
                {error}
              </span>
            )}
            <div className="flex mt-4">
              {n.active ? (
                <button type="button" className="btn" onClick={() => void disconnect.run(device || undefined)} disabled={disconnect.pending}>
                  <Icon name="wifi-slash" />
                  Disconnect
                </button>
              ) : (
                <button type="button" className="btn btn-primary" onClick={doConnect} disabled={connect.pending} aria-busy={connect.pending || undefined}>
                  {connect.pending ? "Connecting" : "Connect"} <Keys enter />
                </button>
              )}
              {password && <span className="sub sm">{n.active ? "Reconnects with the new password" : "Updates the saved password"}</span>}
            </div>
          </div>
          {profile.data && <IpSettings key={`${profile.data.uuid}:${profile.data.version_id}`} profile={profile.data} onForget={() => void forget.run(n.profile_uuid!)} forgetPending={forget.pending} />}
        </>
      ) : (
        <div className="section">
          {eap && (
            <Field label="Username" htmlFor={userId} className="mb-3">
              <input id={userId} className="input" value={username} onChange={(e) => setUsername(e.target.value)} autoComplete="username" />
            </Field>
          )}
          {secured ? (
            <PasswordField id={pskId} value={password} onChange={setPassword} show={show} setShow={setShow} error={error} onEnter={doConnect} autoFocus />
          ) : (
            <p className="sub">Open network. Anyone nearby can read what you send over it.</p>
          )}
          {!secured && error && (
            <span className="error sm mt-2 flex" role="alert">
              <Icon name="warning-circle" />
              {error}
            </span>
          )}
          <div className="flex mt-4">
            <button type="button" className="btn btn-primary" onClick={doConnect} disabled={connect.pending} aria-busy={connect.pending || undefined}>
              {connect.pending ? "Connecting" : "Connect"} <Keys enter />
            </button>
            <button type="button" className="btn btn-ghost" onClick={() => setSelected(null)}>
              Cancel
            </button>
          </div>
        </div>
      )}
    </>
  );
}

function PasswordField({ id, value, onChange, show, setShow, error, onEnter, help, autoFocus }: { id: string; value: string; onChange: (v: string) => void; show: boolean; setShow: (b: boolean) => void; error: string | null; onEnter: () => void; help?: string; autoFocus?: boolean }) {
  return (
    <Field label="Wi-Fi password" htmlFor={id} help={help} error={error}>
      <div className="input-wrap">
        <input
          id={id}
          className="input mono"
          type={show ? "text" : "password"}
          value={value}
          onChange={(e) => onChange(e.target.value)}
          autoComplete="off"
          aria-invalid={error ? true : undefined}
          autoFocus={autoFocus}
          onKeyDown={(e) => {
            if (e.key === "Enter") {
              e.preventDefault();
              onEnter();
            }
          }}
        />
        <button type="button" className="btn btn-ghost btn-icon trail" aria-label={show ? "Hide password" : "Show password"} aria-pressed={show} onClick={() => setShow(!show)}>
          <Icon name={show ? "eye-slash" : "eye"} />
        </button>
      </div>
    </Field>
  );
}

function IpSettings({ profile, onForget, forgetPending }: { profile: Profile; onForget: () => void; forgetPending: boolean }) {
  const ipv4 = profile.ipv4;
  const initial = useMemo(
    () => ({
      method: (ipv4.method === "manual" ? "manual" : "auto") as "auto" | "manual",
      address: ipv4.addresses?.[0] ?? "",
      gateway: ipv4.gateway ?? "",
      dns: (ipv4.dns ?? []).join(", "),
    }),
    [ipv4],
  );
  const [form, setForm] = useState(initial);
  const dirty = form.method !== initial.method || form.address !== initial.address || form.gateway !== initial.gateway || form.dns !== initial.dns;
  const addrId = useId();
  const gwId = useId();
  const dnsId = useId();
  const [err, setErr] = useState<string | null>(null);

  const apply = useAction(actions.profileIp, {
    invalidate: [qk.profile(profile.uuid), qk.profiles, qk.status],
    silent: true,
    onSuccess: () => {
      setErr(null);
      ui.toast({ tone: "ok", title: "IP settings applied", detail: profile.active ? "Reconnect to use them." : undefined });
    },
    onError: (e) => {
      const d = describeError(e);
      setErr(d.detail ? `${d.title}. ${d.detail}` : d.title);
    },
  });

  const submit = () => {
    const dns = form.dns
      .split(/[,\s]+/)
      .map((s) => s.trim())
      .filter(Boolean);
    const cfg: IPConfig = form.method === "manual" ? { method: "manual", addresses: [form.address.trim()], ...(form.gateway.trim() ? { gateway: form.gateway.trim() } : {}), ...(dns.length ? { dns } : {}) } : { method: "auto", ...(dns.length ? { dns, ignore_auto_dns: true } : {}) };
    if (form.method === "manual" && !form.address.trim()) {
      setErr("Enter an address with its prefix, like 192.168.1.73/24.");
      return;
    }
    void apply.run(profile.uuid, { ipv4: cfg });
  };

  return (
    <div className="section">
      <SectionHead
        as="h3"
        title="IP settings"
        actions={
          <Segmented
            label="IPv4 method"
            value={form.method}
            onChange={(m) => setForm({ ...form, method: m })}
            options={[
              { value: "auto", label: "Automatic" },
              { value: "manual", label: "Manual" },
            ]}
          />
        }
      />
      <div className="form-grid">
        {form.method === "manual" && (
          <>
            <Field label="Address" htmlFor={addrId}>
              <input id={addrId} className="input mono" value={form.address} onChange={(e) => setForm({ ...form, address: e.target.value })} placeholder="192.168.1.73/24" />
            </Field>
            <Field label="Gateway" htmlFor={gwId}>
              <input id={gwId} className="input mono" value={form.gateway} onChange={(e) => setForm({ ...form, gateway: e.target.value })} placeholder="192.168.1.254" />
            </Field>
          </>
        )}
        <Field label="DNS" htmlFor={dnsId} className="span2" help="Comma separated. Leave empty to use the router's." error={err}>
          <input id={dnsId} className="input mono" value={form.dns} onChange={(e) => setForm({ ...form, dns: e.target.value })} placeholder="1.1.1.1, 9.9.9.9" aria-invalid={err ? true : undefined} />
        </Field>
      </div>
      <div className="flex mt-4">
        <button type="button" className="btn btn-primary" onClick={submit} disabled={!dirty || apply.pending}>
          Apply
        </button>
        <button type="button" className="btn btn-ghost" onClick={() => setForm(initial)} disabled={!dirty}>
          Revert
        </button>
        <button type="button" className="btn btn-ghost btn-danger ml-auto" onClick={onForget} disabled={forgetPending}>
          <Icon name="trash" />
          Forget
        </button>
      </div>
    </div>
  );
}
