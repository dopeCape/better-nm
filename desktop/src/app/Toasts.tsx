import { useEffect } from "react";
import { Icon } from "@/components/Icon";
import type { IconName } from "@/design/icon-names";
import { useUI, type Toast } from "@/state/ui";

const ICON: Record<Toast["tone"], { name: IconName; tone?: "ok" | "warn" | "error" | "accent" }> = {
  ok: { name: "check-circle-fill", tone: "ok" },
  warn: { name: "warning-circle-fill", tone: "warn" },
  error: { name: "warning-circle-fill", tone: "error" },
  accent: { name: "info", tone: "accent" },
  plain: { name: "info" },
};

function ToastItem({ t }: { t: Toast }) {
  const dismiss = useUI((s) => s.dismissToast);
  useEffect(() => {
    const ms = t.tone === "error" ? 9000 : 5000;
    const h = window.setTimeout(() => dismiss(t.id), ms);
    return () => window.clearTimeout(h);
  }, [t.id, t.tone, dismiss]);
  const ic = ICON[t.tone];
  return (
    <div className="toast" role={t.tone === "error" ? "alert" : "status"}>
      <Icon name={ic.name} tone={ic.tone} />
      <div className="body">
        <div className="t">{t.title}</div>
        {t.detail && <div className="d">{t.detail}</div>}
      </div>
      <button type="button" className="btn btn-ghost btn-icon" aria-label="Dismiss" onClick={() => dismiss(t.id)}>
        <Icon name="x" />
      </button>
    </div>
  );
}

export function Toasts() {
  const toasts = useUI((s) => s.toasts);
  if (!toasts.length) return null;
  return (
    <div className="toast-stack">
      {toasts.map((t) => (
        <ToastItem key={t.id} t={t} />
      ))}
    </div>
  );
}
