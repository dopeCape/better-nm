// The password prompt (06-secret-prompt): opened by a `secret-needed` event,
// fields from the SecretRequest, save toggle, countdown to expiry, closes on
// `secret-resolved`. Submits {secrets, save} to POST /v1/secrets/{id}.
import { useEffect, useId, useRef, useState } from "react";
import { actions, useAction } from "@/api/actions";
import { ApiError, describeError } from "@/api/client";
import type { SecretRequest } from "@/api/types";
import { Dialog } from "@/components/Dialog";
import { Icon } from "@/components/Icon";
import { Banner, Field, Keys, SwitchRow } from "@/components/ui";
import { securityLabel } from "@/lib/format";
import { ui, useUI } from "@/state/ui";
import type { WifiSecurity } from "@/api/types";

function titleFor(r: SecretRequest): string {
  if (r.vpn) return `${r.vpn_kind || "VPN"} password`;
  if (r.setting_name === "802-11-wireless-security" || r.ssid) return "Wi-Fi password";
  const f = r.fields[0];
  return f ? f.label : "Password needed";
}

function subtitleFor(r: SecretRequest): string {
  if (r.vpn) return r.connection_name;
  const sec = r.setting_name === "802-11-wireless-security" ? securityLabel(guessSecurity(r)) : "";
  return [r.ssid || r.connection_name, sec].filter(Boolean).join(" · ");
}

function guessSecurity(r: SecretRequest): WifiSecurity | undefined {
  const keys = r.fields.map((f) => f.key);
  if (keys.includes("psk")) return "wpa-psk";
  if (keys.includes("wep-key0")) return "wep";
  if (keys.includes("password") || keys.includes("identity")) return "wpa-eap";
  return undefined;
}

function useCountdown(expiresAt: string | undefined): number {
  const [left, setLeft] = useState(0);
  useEffect(() => {
    if (!expiresAt) return;
    const end = new Date(expiresAt).getTime();
    const tick = () => setLeft(Math.max(0, Math.round((end - Date.now()) / 1000)));
    tick();
    const h = window.setInterval(tick, 1000);
    return () => window.clearInterval(h);
  }, [expiresAt]);
  return left;
}

export function SecretPrompt() {
  const req = useUI((s) => s.secret);
  const queued = useUI((s) => s.secretQueue.length - 1);
  if (!req) return null;
  // Keyed by request id so a new prompt starts with a clean form; the next
  // queued request takes over when this one is answered, cancelled or resolved.
  return <SecretPromptBody key={req.id} req={req} queued={Math.max(0, queued)} />;
}

function SecretPromptBody({ req, queued }: { req: SecretRequest; queued: number }) {
  const removeSecret = useUI((s) => s.removeSecret);
  const done = () => removeSecret(req.id);
  const [values, setValues] = useState<Record<string, string>>({});
  const [show, setShow] = useState<Record<string, boolean>>({});
  const [save, setSave] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const firstRef = useRef<HTMLInputElement>(null);
  const titleId = useId();
  const left = useCountdown(req.expires_at);

  const answer = useAction(actions.secretAnswer, {
    silent: true,
    onSuccess: done,
    onError: (e) => {
      if (e instanceof ApiError && (e.status === 404 || e.status === 409)) {
        ui.toast({ tone: "warn", title: "That prompt is gone", detail: e.hint ?? e.message });
        done();
        return;
      }
      const d = describeError(e);
      setError(d.detail ? `${d.title}. ${d.detail}` : d.title);
    },
  });
  const cancel = useAction(actions.secretCancel, { silent: true, onSuccess: done, onError: done });

  const fields = req.fields;

  const submit = () => {
    const secrets: Record<string, string> = {};
    for (const f of fields) if (values[f.key]) secrets[f.key] = values[f.key]!;
    const missing = fields.filter((f) => f.secret && !secrets[f.key]);
    if (missing.length) {
      setError(`Enter ${missing.map((f) => f.label.toLowerCase()).join(" and ")}.`);
      return;
    }
    void answer.run(req.id, { secrets, save });
  };
  const mm = Math.floor(left / 60);
  const ss = String(left % 60).padStart(2, "0");

  return (
    <Dialog open onClose={() => void cancel.run(req.id)} labelledBy={titleId} initialFocus={firstRef}>
      <div className="dialog-head">
        <Icon name="key" size="lg" tone="accent" />
        <div>
          <h2 className="h2" id={titleId}>
            {titleFor(req)}
          </h2>
          <div className="sub sm mt-2">{subtitleFor(req)}</div>
        </div>
        <button type="button" className="btn btn-ghost btn-icon close" aria-label="Cancel" onClick={() => void cancel.run(req.id)}>
          <Icon name="x" />
        </button>
      </div>
      <div className="dialog-body">
        {req.request_new && (
          <Banner tone="error" icon="warning-circle-fill" title="The saved password was rejected">
            {req.vpn ? "The server asked again. Enter the current one." : "The router asked again. Enter the current one."}
          </Banner>
        )}
        {req.message && <p className="sub">{req.message}</p>}
        {fields.map((f, i) => {
          const id = `${titleId}-${f.key}`;
          const visible = !f.secret || show[f.key];
          return (
            <Field key={f.key} label={f.label === titleFor(req) ? "Password" : f.label} htmlFor={id} error={i === fields.length - 1 ? error : undefined}>
              <div className="input-wrap">
                <input
                  ref={i === 0 ? firstRef : undefined}
                  id={id}
                  className={f.secret ? "input mono" : "input"}
                  type={visible ? "text" : "password"}
                  value={values[f.key] ?? ""}
                  autoComplete="off"
                  aria-invalid={error ? true : undefined}
                  onChange={(e) => setValues({ ...values, [f.key]: e.target.value })}
                  onKeyDown={(e) => {
                    if (e.key === "Enter") {
                      e.preventDefault();
                      submit();
                    }
                  }}
                />
                {f.secret && (
                  <button type="button" className="btn btn-ghost btn-icon trail" aria-label={show[f.key] ? "Hide password" : "Show password"} aria-pressed={!!show[f.key]} onClick={() => setShow({ ...show, [f.key]: !show[f.key] })}>
                    <Icon name={show[f.key] ? "eye-slash" : "eye"} />
                  </button>
                )}
              </div>
            </Field>
          );
        })}
        <SwitchRow label="Save in the profile" hint="So the next connection does not ask" checked={save} onChange={setSave} />
      </div>
      <div className="dialog-foot">
        <span className="sub sm flex gap-1">
          <Icon name="clock-countdown" />
          {left > 0 ? (
            <>
              Expires in{" "}
              <span className="mono">
                {mm}:{ss}
              </span>
            </>
          ) : (
            "Expired"
          )}
          {queued > 0 && <span className="muted">, {queued} more waiting</span>}
        </span>
        <span className="grow" />
        <button type="button" className="btn" onClick={() => void cancel.run(req.id)} disabled={cancel.pending}>
          Cancel <Keys keys={["Esc"]} />
        </button>
        <button type="button" className="btn btn-primary" onClick={submit} disabled={answer.pending || left === 0} aria-busy={answer.pending || undefined}>
          Connect <Keys enter />
        </button>
      </div>
    </Dialog>
  );
}
