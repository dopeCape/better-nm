import { useEffect, useId, useMemo, useState } from "react";
import { actions, useAction } from "@/api/actions";
import { qk, useDaemonConfig, useDaemonStatus, useStatus } from "@/api/queries";
import type { DaemonConfig } from "@/api/types";
import { Icon } from "@/components/Icon";
import { Badge, Banner, Field, PageHead, SectionHead, Segmented, Select, Skeleton, SwitchRow } from "@/components/ui";
import type { CustomTokens, Density, DesktopConfigPatch } from "@/config/types";
import { fmtUptime, nsToSeconds } from "@/lib/format";
import { shell } from "@/shell";
import { ui, useUI } from "@/state/ui";
import { applyConfig, currentTokens, isHex, normaliseHex, previewTokens } from "@/theme/apply";
import { PRESETS, TOKEN_NAMES, type TokenName, type Tokens } from "@/theme/presets";

function useDesktopConfigSet() {
  return useAction(async (patch: DesktopConfigPatch) => shell.configSet(patch), { label: "Desktop config" });
}

export function Settings() {
  const config = useUI((s) => s.config);
  const set = useDesktopConfigSet();
  const daemonCfg = useDaemonConfig();
  const status = useStatus();
  const daemon = useDaemonStatus();

  return (
    <>
      <PageHead
        title="Settings"
        sub={
          <>
            Appearance is saved to <span className="mono">{config.source_path ?? "~/.config/bnmdesktop/config.yaml"}</span>; notifications and monitoring to the daemon's <span className="mono">config.toml</span>. Both are picked up live.
          </>
        }
      />
      <div className="settings">
        <Appearance />

        <section className="section">
          <SectionHead title="Desktop" />
          <div className="grid-2">
            <Field label="Tray icon" htmlFor="set-tray" help="Auto shows one when the desktop has a status area.">
              <Select
                id="set-tray"
                value={config.tray}
                onChange={(v) => void set.run({ tray: v as "auto" | "on" | "off" })}
                options={[
                  { value: "auto", label: "Auto" },
                  { value: "on", label: "Always" },
                  { value: "off", label: "Never" },
                ]}
              />
            </Field>
            <Field label="Start on" htmlFor="set-start">
              <Select
                id="set-start"
                value={config.start_section}
                onChange={(v) => void set.run({ start_section: v as typeof config.start_section })}
                options={["overview", "wifi", "vpn", "quality", "speed", "devices", "settings"].map((s) => ({ value: s, label: s === "wifi" ? "Wi-Fi" : s === "vpn" ? "VPN" : s[0]!.toUpperCase() + s.slice(1) }))}
              />
            </Field>
          </div>
          <div className="list mt-4">
            <SwitchRow label="Close to tray" hint="Closing the window keeps bnm running in the tray" checked={config.close_to_tray} disabled={config.tray === "off"} onChange={(on) => void set.run({ close_to_tray: on })} />
            <SwitchRow label="Reduce motion" hint="Skip the switch, dialog and chart animations even if the desktop allows them" checked={config.reduced_motion} onChange={(on) => void set.run({ reduced_motion: on })} />
          </div>
        </section>

        <Notifications cfg={daemonCfg.data} loading={daemonCfg.isPending} />
        <Monitoring cfg={daemonCfg.data} loading={daemonCfg.isPending} />

        <section className="section">
          <SectionHead
            title="Daemon"
            actions={
              daemon.isPending ? (
                <Skeleton w={80} h={20} />
              ) : daemon.data?.running ? (
                <Badge tone="ok" icon="check">
                  Running
                </Badge>
              ) : (
                <Badge tone="error" icon="warning-circle">
                  Not running
                </Badge>
              )
            }
          />
          <div className="grid-2">
            <dl className="kv tight">
              <dt>Version</dt>
              <dd>
                {status.data ? (
                  <>
                    <span className="mono">bnmd {status.data.version}</span>
                    <span className="sub">api {status.data.api_version}</span>
                  </>
                ) : (
                  <Skeleton w={120} />
                )}
              </dd>
              <dt>Uptime</dt>
              <dd>{status.data ? <span className="mono">{fmtUptime(status.data.uptime_seconds)}</span> : <Skeleton w={60} />}</dd>
              <dt>Socket</dt>
              <dd>{daemon.data ? <span className="mono truncate">{daemon.data.socket}</span> : <Skeleton w={200} />}</dd>
              <dt>NetworkManager</dt>
              <dd>{status.data ? <span className="mono">{status.data.nm_version}</span> : <Skeleton w={60} />}</dd>
            </dl>
            <DaemonActions />
          </div>
        </section>
      </div>
    </>
  );
}

/* ---------- Appearance ---------- */

const SWATCH: TokenName[] = ["base", "surface", "accent", "ok", "error", "text"];

function Appearance() {
  const config = useUI((s) => s.config);
  const set = useDesktopConfigSet();
  const [paletteOpen, setPaletteOpen] = useState(config.theme === "custom");
  const fontId = useId();

  const fontValue = !config.font ? "" : config.font.toLowerCase() === "inter" ? "Inter" : /^system(-ui)?$/i.test(config.font) ? "system" : "other";

  return (
    <section className="section flush">
      <SectionHead title="Appearance" />
      <Field label="Theme" help="Any Base16 scheme converts to a preset; see the palette editor for the 16 tokens.">
        <div className="presets" role="radiogroup" aria-label="Theme">
          {PRESETS.map((p) => (
            <button key={p.id} type="button" className="preset" role="radio" aria-checked={config.theme === p.id} onClick={() => void set.run({ theme: p.id })}>
              <span className="sw" style={{ background: p.tokens.base }} aria-hidden="true">
                {SWATCH.map((t) => (
                  <i key={t} style={{ background: p.tokens[t] }} />
                ))}
              </span>
              <span className="pn">{p.name}</span>
            </button>
          ))}
        </div>
      </Field>

      <details className="mt-4" open={paletteOpen} onToggle={(e) => setPaletteOpen((e.currentTarget as HTMLDetailsElement).open)}>
        <summary className="flex">
          <Icon name="caret-right" className="caret" />
          <span className="h3">Custom palette</span>
          <span className="sub sm">{config.theme === "custom" ? "in use" : "start from the current preset"}</span>
        </summary>
        {paletteOpen && <PaletteEditor />}
      </details>

      <div className="grid-2 mt-4">
        <AccentField key={config.accent} saved={config.accent} onSave={(v) => void set.run({ accent: v })} />
        <Field label="UI font" htmlFor={fontId} help="Machine values always use JetBrains Mono, or your Nerd Font if present.">
          <Select
            id={fontId}
            value={fontValue}
            onChange={(v) => void set.run({ font: v === "other" ? config.font : v })}
            options={[
              { value: "", label: "Geist (bundled)" },
              { value: "Inter", label: "Inter (bundled)" },
              { value: "system", label: "System" },
              ...(fontValue === "other" ? [{ value: "other", label: config.font }] : []),
            ]}
          />
        </Field>
      </div>
      <div className="grid-2 mt-4">
        <Field label="Density" help="Compact fits a 1120 x 720 window without scrolling.">
          <Segmented<Density>
            label="Density"
            value={config.density}
            onChange={(d) => void set.run({ density: d })}
            options={[
              { value: "compact", label: "Compact" },
              { value: "default", label: "Default" },
              { value: "comfortable", label: "Comfortable" },
            ]}
          />
        </Field>
      </div>
    </section>
  );
}

function AccentField({ saved, onSave }: { saved: string; onSave: (v: string) => void }) {
  const id = useId();
  const [accent, setAccent] = useState(saved);
  const bad = !!accent && !isHex(accent);
  return (
    <Field label="Accent" htmlFor={id} help="Overrides the preset's accent. Leave empty for the preset's own." error={bad ? "Use a hex colour like #89b4fa." : undefined}>
      <div className="input-wrap">
        <input
          id={id}
          className="input mono"
          value={accent}
          placeholder="#89b4fa"
          onChange={(e) => setAccent(e.target.value)}
          onBlur={() => {
            if (accent === saved || bad) return;
            onSave(accent ? normaliseHex(accent) : "");
          }}
          onKeyDown={(e) => e.key === "Enter" && (e.currentTarget as HTMLInputElement).blur()}
          aria-invalid={bad ? true : undefined}
        />
        <span className="trail chip" style={{ background: isHex(accent) ? accent : "var(--accent)", right: 8 }} aria-hidden="true" />
      </div>
    </Field>
  );
}

function tokensToCustom(t: Tokens): CustomTokens {
  const out: CustomTokens = {};
  for (const k of TOKEN_NAMES) out[k.replace("-", "_") as keyof CustomTokens] = t[k];
  return out;
}

function PaletteEditor() {
  const config = useUI((s) => s.config);
  const set = useDesktopConfigSet();
  const [draft, setDraft] = useState<Tokens>(() => currentTokens());
  const valid = useMemo(() => TOKEN_NAMES.every((t) => isHex(draft[t])), [draft]);

  // Live preview while editing; restore the saved config when the editor unmounts.
  useEffect(() => {
    if (valid) previewTokens(draft);
  }, [draft, valid]);
  useEffect(() => {
    return () => {
      applyConfig(useUI.getState().config);
    };
  }, []);

  const save = () => {
    if (!valid) return;
    void set.run({ theme: "custom", custom: tokensToCustom(draft) });
    ui.toast({ tone: "ok", title: "Custom palette saved" });
  };
  const reset = () => {
    applyConfig(config);
    setDraft(currentTokens());
  };

  return (
    <>
      <div className="tokens mt-3">
        {TOKEN_NAMES.map((t) => {
          const v = draft[t];
          const ok = isHex(v);
          return (
            <label key={t} className="tok">
              <span className="chip" style={{ background: ok ? v : "transparent" }} aria-hidden="true" />
              <span className="tn">{t}</span>
              <input className="input mono" value={v} aria-label={t} aria-invalid={ok ? undefined : true} onChange={(e) => setDraft({ ...draft, [t]: e.target.value })} spellCheck={false} />
            </label>
          );
        })}
      </div>
      <div className="flex mt-3">
        <button type="button" className="btn btn-primary" onClick={save} disabled={!valid}>
          Save as custom
        </button>
        <button type="button" className="btn btn-ghost" onClick={reset}>
          Reset to preset
        </button>
        {!valid && <span className="sub sm">Every token needs a hex colour.</span>}
      </div>
    </>
  );
}

/* ---------- Notifications ---------- */

type NotifyKey = keyof DaemonConfig["notify"];

const NOTIFY_ROWS: { label: string; hint?: string; keys: NotifyKey[]; locked?: boolean }[] = [
  { label: "Connected and disconnected", hint: "Once per network change, roaming is ignored", keys: ["connected", "disconnected"] },
  { label: "No internet", hint: "After 10 s of limited or portal connectivity", keys: ["no_internet", "internet_restored"] },
  { label: "Quality degraded and recovered", hint: "From the baseline engine, rate limited to one per 10 min", keys: ["degraded", "recovered"] },
  { label: "VPN up and down", keys: ["vpn_up", "vpn_down"] },
  { label: "Password prompts", hint: "Always on. A prompt you cannot see is a connection that never comes up.", keys: ["secret_needed"], locked: true },
];

function Notifications({ cfg, loading }: { cfg: DaemonConfig | undefined; loading: boolean }) {
  const setKey = useAction(actions.daemonConfigSet, { invalidate: [qk.daemonConfig], label: "Notifications" });
  const test = useAction(actions.notifyTest, { label: "Test notification", onSuccess: () => ui.toast({ tone: "ok", title: "Test notification sent" }) });
  const notify = cfg?.notify;
  return (
    <section className="section">
      <SectionHead
        title="Notifications"
        actions={
          <button type="button" className="btn btn-ghost" onClick={() => void test.run()} disabled={test.pending}>
            <Icon name="bell" />
            Send a test
          </button>
        }
      />
      <div className="list" aria-busy={loading || undefined}>
        {NOTIFY_ROWS.map((r) => {
          const on = notify ? r.keys.every((k) => notify[k] as boolean) : false;
          const mixed = notify && !on && r.keys.some((k) => notify[k] as boolean);
          return (
            <SwitchRow
              key={r.label}
              label={r.label}
              hint={r.hint}
              checked={r.locked ? true : mixed ? "mixed" : on}
              disabled={r.locked || !notify}
              busy={setKey.pending}
              onChange={(next) => {
                for (const k of r.keys) void setKey.run(`notify.${k}`, next ? "true" : "false");
              }}
            />
          );
        })}
      </div>
    </section>
  );
}

/* ---------- Monitoring ---------- */

function Monitoring({ cfg, loading }: { cfg: DaemonConfig | undefined; loading: boolean }) {
  const setKey = useAction(actions.daemonConfigSet, { invalidate: [qk.daemonConfig, qk.monitor], label: "Monitoring" });
  const ivId = useId();
  const anchorsId = useId();
  const interval = cfg ? nsToSeconds(cfg.monitor.interval) : 30;
  const saved = useMemo(() => (cfg ? ["gateway", ...cfg.monitor.anchors.filter((a) => a !== "gateway")].join(", ") : ""), [cfg]);
  const commitAnchors = (text: string) => {
    const list = text
      .split(/[,\s]+/)
      .map((s) => s.trim())
      .filter((s) => s && s !== "gateway");
    if (list.join(",") === (cfg?.monitor.anchors ?? []).join(",")) return;
    void setKey.run("monitor.anchors", list.join(","));
  };

  return (
    <section className="section">
      <SectionHead title="Monitoring" />
      <div className="grid-2">
        <Field label="Probe interval" htmlFor={ivId}>
          <Select
            id={ivId}
            value={String(interval)}
            disabled={loading || !cfg}
            onChange={(v) => void setKey.run("monitor.interval", `${v}s`)}
            options={[15, 30, 60, 120].includes(interval) ? [15, 30, 60, 120].map((s) => ({ value: String(s), label: `${s} s` })) : [{ value: String(interval), label: `${interval} s` }, ...[15, 30, 60, 120].map((s) => ({ value: String(s), label: `${s} s` }))]}
          />
        </Field>
        <AnchorsField key={saved} id={anchorsId} saved={saved} disabled={loading || !cfg} onCommit={commitAnchors} />
      </div>
    </section>
  );
}

function AnchorsField({ id, saved, disabled, onCommit }: { id: string; saved: string; disabled: boolean; onCommit: (text: string) => void }) {
  const [anchors, setAnchors] = useState(saved);
  return (
    <Field label="Anchors" htmlFor={id} help="Comma separated hosts or IPs. The gateway is always first.">
      <input id={id} className="input mono" value={anchors} disabled={disabled} onChange={(e) => setAnchors(e.target.value)} onBlur={() => onCommit(anchors)} onKeyDown={(e) => e.key === "Enter" && (e.currentTarget as HTMLInputElement).blur()} />
    </Field>
  );
}

/* ---------- Daemon ---------- */

function DaemonActions() {
  const daemon = useDaemonStatus();
  const install = useAction(async () => shell.daemonInstall(), { invalidate: [qk.daemonStatus], label: "Install", onSuccess: () => ui.toast({ tone: "ok", title: "bnmd.service installed and started" }) });
  const restart = useAction(async () => shell.daemonRestart(), { invalidate: [qk.daemonStatus, qk.status], label: "Restart", onSuccess: () => ui.toast({ tone: "ok", title: "Daemon restarted" }) });
  const d = daemon.data;
  return (
    <div>
      {d && !d.unit_installed ? (
        <Banner
          tone="accent"
          icon="terminal-window"
          title="Run as a user service"
          actions={
            <button type="button" className="btn btn-primary" onClick={() => void install.run()} disabled={install.pending}>
              Install
            </button>
          }
        >
          Installs <span className="mono">bnmd.service</span> under <span className="mono">systemd --user</span> so monitoring and prompts work before the app opens.
        </Banner>
      ) : d ? (
        <Banner tone={d.unit_active ? "ok" : "warn"} icon="terminal-window" title={d.unit_active ? "Runs as a user service" : "User service installed but not active"}>
          <span className="mono">bnmd.service</span> under <span className="mono">systemd --user</span>
          {d.pid ? (
            <>
              , pid <span className="mono">{d.pid}</span>
            </>
          ) : null}
          .
        </Banner>
      ) : (
        <Skeleton w="100%" h={64} />
      )}
      <div className="flex mt-3">
        <button type="button" className="btn btn-ghost" onClick={() => void restart.run()} disabled={restart.pending || !d}>
          <Icon name="arrows-clockwise" />
          Restart daemon
        </button>
      </div>
    </div>
  );
}
