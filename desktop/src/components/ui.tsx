// Small presentational pieces that map 1:1 onto base.css classes.
import { useEffect, useId, useRef, useState, type ReactNode } from "react";
import { Icon, type Tone } from "./Icon";
import type { IconName } from "@/design/icon-names";

export function cx(...parts: (string | false | null | undefined)[]): string {
  return parts.filter(Boolean).join(" ");
}

/* ---------- Signal bars ---------- */
export function Sig({ level, off, label }: { level: 0 | 1 | 2 | 3 | 4; off?: boolean; label?: string }) {
  return (
    <span className={cx("sig", off && "off")} data-level={level} role="img" aria-label={label ?? `Signal ${level} of 4`}>
      <i />
      <i />
      <i />
      <i />
    </span>
  );
}

/* ---------- Badge / tag ---------- */
export function Badge({ tone, icon, children, title }: { tone?: Tone; icon?: IconName; children: ReactNode; title?: string }) {
  return (
    <span className={cx("badge", tone)} title={title}>
      {icon && <Icon name={icon} spin={icon === "spinner-gap"} />}
      {children}
    </span>
  );
}

export function Tag({ children, title }: { children: ReactNode; title?: string }) {
  return (
    <span className="tag" title={title}>
      {children}
    </span>
  );
}

export function Dot({ tone, title }: { tone?: "ok" | "warn" | "error" | "vpn" | "accent"; title?: string }) {
  return <span className={cx("dot", tone)} title={title} {...(title ? { role: "img", "aria-label": title } : { "aria-hidden": true })} />;
}

/* ---------- Keys ---------- */
export function Kbd({ children, title }: { children: ReactNode; title?: string }) {
  return <kbd title={title}>{children}</kbd>;
}

export function Keys({ keys, enter }: { keys?: string[]; enter?: boolean }) {
  return (
    <span className="keys">
      {keys?.map((k) => (
        <kbd key={k}>{k}</kbd>
      ))}
      {enter && (
        <kbd title="Enter" aria-label="Enter">
          <Icon name="arrow-elbow-down-left" />
        </kbd>
      )}
    </span>
  );
}

/* ---------- Switch ---------- */
export interface SwitchProps {
  checked: boolean | "mixed";
  onChange?: (next: boolean) => void;
  disabled?: boolean;
  label: string;
  id?: string;
  busy?: boolean;
}

export function Switch({ checked, onChange, disabled, label, id, busy }: SwitchProps) {
  return (
    <button
      id={id}
      type="button"
      role="switch"
      className="switch"
      aria-checked={checked}
      aria-label={label}
      aria-disabled={disabled || busy ? true : undefined}
      disabled={disabled}
      aria-busy={busy || undefined}
      onClick={() => {
        if (disabled || busy) return;
        onChange?.(checked !== true);
      }}
    />
  );
}

export function SwitchRow({ label, hint, ...sw }: SwitchProps & { hint?: ReactNode }) {
  const id = useId();
  return (
    <div className="switch-row">
      <span className="label" id={id}>
        {label}
        {hint && <span className="hint">{hint}</span>}
      </span>
      <Switch {...sw} label={label} />
    </div>
  );
}

/* ---------- Segmented ---------- */
export function Segmented<T extends string>({ value, options, onChange, label }: { value: T; options: { value: T; label: string }[]; onChange: (v: T) => void; label: string }) {
  return (
    <div className="segmented" role="group" aria-label={label}>
      {options.map((o) => (
        <button key={o.value} type="button" aria-pressed={o.value === value} onClick={() => onChange(o.value)}>
          {o.label}
        </button>
      ))}
    </div>
  );
}

/* ---------- Field / Select ---------- */
export function Field({ label, htmlFor, help, error, children, className }: { label: ReactNode; htmlFor?: string; help?: ReactNode; error?: ReactNode; children: ReactNode; className?: string }) {
  return (
    <div className={cx("field", className)}>
      {htmlFor ? <label htmlFor={htmlFor}>{label}</label> : <span className="field-label">{label}</span>}
      {children}
      {error ? (
        <span className="error" role="alert">
          <Icon name="warning-circle" />
          {error}
        </span>
      ) : (
        help && <span className="help">{help}</span>
      )}
    </div>
  );
}

export function Select({ id, value, onChange, options, label, style, disabled }: { id?: string; value: string; onChange: (v: string) => void; options: { value: string; label: string }[]; label?: string; style?: React.CSSProperties; disabled?: boolean }) {
  return (
    <div className="select-wrap" style={style}>
      <select id={id} className="select" value={value} onChange={(e) => onChange(e.target.value)} aria-label={label} disabled={disabled}>
        {options.map((o) => (
          <option key={o.value} value={o.value}>
            {o.label}
          </option>
        ))}
      </select>
      <Icon name="caret-up-down" />
    </div>
  );
}

/* ---------- Copy ---------- */
export function CopyButton({ text, label = "Copy" }: { text: string; label?: string }) {
  const [done, setDone] = useState(false);
  const t = useRef<number | undefined>(undefined);
  useEffect(() => () => window.clearTimeout(t.current), []);
  return (
    <button
      type="button"
      className="btn btn-ghost btn-icon"
      title={label}
      aria-label={done ? "Copied" : label}
      onClick={async () => {
        try {
          await navigator.clipboard.writeText(text);
          setDone(true);
          window.clearTimeout(t.current);
          t.current = window.setTimeout(() => setDone(false), 1200);
        } catch {
          /* clipboard unavailable */
        }
      }}
    >
      <Icon name={done ? "check" : "copy"} />
    </button>
  );
}

/* ---------- Banner ---------- */
export function Banner({ tone, icon, title, children, actions, className }: { tone?: "ok" | "warn" | "error" | "accent"; icon: IconName; title: ReactNode; children?: ReactNode; actions?: ReactNode; className?: string }) {
  return (
    <div className={cx("banner", tone, className)} role={tone === "error" ? "alert" : "status"}>
      <Icon name={icon} />
      <div className="grow">
        <div className="t">{title}</div>
        {children && <div className="d">{children}</div>}
      </div>
      {actions && <div className="actions">{actions}</div>}
    </div>
  );
}

/* ---------- Empty and error states ---------- */
export function EmptyState({ icon, title, children, actions, error, className, style }: { icon?: IconName; title: ReactNode; children?: ReactNode; actions?: ReactNode; error?: boolean; className?: string; style?: React.CSSProperties }) {
  return (
    <div className={cx("state", error && "error", className)} style={style} role={error ? "alert" : undefined}>
      {icon && <Icon name={icon} size="2xl" />}
      <div className="t">{title}</div>
      {children && <div className="d">{children}</div>}
      {actions && <div className="actions">{actions}</div>}
    </div>
  );
}

/* ---------- Skeleton (sized like the content it stands in for) ---------- */
export function Skeleton({ w, h = 14, className, style }: { w?: number | string; h?: number; className?: string; style?: React.CSSProperties }) {
  return <span className={cx("skel", className)} style={{ width: w, height: h, ...style }} aria-hidden="true" />;
}

export function SkeletonRows({ rows = 5, h }: { rows?: number; h?: number }) {
  return (
    <div className="list" aria-busy="true" aria-label="Loading">
      {Array.from({ length: rows }, (_, i) => (
        <div key={i} className="row skel-row" style={h ? { minHeight: h } : undefined}>
          <Skeleton w={18} h={12} />
          <Skeleton w={`${40 + ((i * 17) % 35)}%`} />
          <Skeleton w={56} />
        </div>
      ))}
    </div>
  );
}

/* ---------- Section head ---------- */
export function SectionHead({ title, sub, actions, as: As = "h2" }: { title: ReactNode; sub?: ReactNode; actions?: ReactNode; as?: "h2" | "h3" }) {
  return (
    <div className="section-head">
      <As className="h3">{title}</As>
      {sub && <span className="sub sm">{sub}</span>}
      {actions && <div className="actions">{actions}</div>}
    </div>
  );
}

export function PageHead({ title, sub, actions }: { title: string; sub?: ReactNode; actions?: ReactNode }) {
  return (
    <div className="page-head">
      <div>
        <h1 className="h1">{title}</h1>
        {sub !== undefined && <p>{sub}</p>}
      </div>
      {actions && <div className="actions">{actions}</div>}
    </div>
  );
}
