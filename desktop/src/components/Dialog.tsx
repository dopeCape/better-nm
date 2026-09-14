// Modal: scrim + .dialog from base.css. Traps Tab inside, closes on Esc, restores
// focus to what opened it.
import { useEffect, useRef, type ReactNode } from "react";

const FOCUSABLE = 'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';

export interface DialogProps {
  open: boolean;
  onClose: () => void;
  labelledBy: string;
  children: ReactNode;
  /** "top" aligns to the upper third (the command palette); default centres. */
  align?: "center" | "top";
  className?: string;
  role?: "dialog" | "alertdialog";
  /** Element to focus on open; defaults to the first focusable. */
  initialFocus?: React.RefObject<HTMLElement | null>;
}

export function Dialog({ open, onClose, labelledBy, children, align = "center", className = "dialog", role = "dialog", initialFocus }: DialogProps) {
  const box = useRef<HTMLDivElement>(null);
  const restore = useRef<Element | null>(null);

  useEffect(() => {
    if (!open) return;
    restore.current = document.activeElement;
    const el = box.current;
    const target = initialFocus?.current ?? el?.querySelector<HTMLElement>("[data-autofocus]") ?? el?.querySelector<HTMLElement>(FOCUSABLE);
    target?.focus();
    return () => {
      const r = restore.current as HTMLElement | null;
      if (r && typeof r.focus === "function" && document.contains(r)) r.focus();
    };
  }, [open, initialFocus]);

  if (!open) return null;

  const onKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === "Escape") {
      e.stopPropagation();
      onClose();
      return;
    }
    if (e.key !== "Tab" || !box.current) return;
    const items = Array.from(box.current.querySelectorAll<HTMLElement>(FOCUSABLE)).filter((n) => n.offsetParent !== null);
    if (!items.length) return;
    const first = items[0]!;
    const last = items[items.length - 1]!;
    if (e.shiftKey && document.activeElement === first) {
      e.preventDefault();
      last.focus();
    } else if (!e.shiftKey && document.activeElement === last) {
      e.preventDefault();
      first.focus();
    }
  };

  return (
    // The scrim is a click-away target, not a control; keyboard users have Esc.
    // eslint-disable-next-line jsx-a11y/no-static-element-interactions
    <div className={`scrim${align === "top" ? " top" : ""}`} onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <div ref={box} className={className} role={role} aria-modal="true" aria-labelledby={labelledBy} onKeyDown={onKeyDown}>
        {children}
      </div>
    </div>
  );
}
