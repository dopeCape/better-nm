// Keyboard map (DESIGN.md): 1..7 sections, Ctrl K palette, / filter, R rescan,
// A add VPN, P pause, Enter/Q speed, Esc cancel, G then O/W/V/Q/S/D/T go-to chords.
// Views register their keys with useHotkey; App installs the one window listener.
import { useEffect } from "react";
import { SECTIONS, type Section } from "@/config/types";
import { useUI } from "@/state/ui";

type Handler = (e: KeyboardEvent) => void;
const registry = new Map<string, Handler[]>();

function norm(key: string): string {
  return key.length === 1 ? key.toLowerCase() : key;
}

export function useHotkey(key: string, handler: Handler | null | undefined, enabled = true): void {
  useEffect(() => {
    if (!enabled || !handler) return;
    const k = norm(key);
    const list = registry.get(k) ?? [];
    list.push(handler);
    registry.set(k, list);
    return () => {
      const l = registry.get(k);
      if (!l) return;
      const i = l.indexOf(handler);
      if (i >= 0) l.splice(i, 1);
      if (!l.length) registry.delete(k);
    };
  }, [key, handler, enabled]);
}

export const GO_CHORDS: Record<string, Section> = { o: "overview", w: "wifi", v: "vpn", q: "quality", s: "speed", d: "devices", t: "settings" };

export function isEditable(el: EventTarget | null): boolean {
  if (!(el instanceof HTMLElement)) return false;
  const tag = el.tagName;
  return tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT" || el.isContentEditable;
}

function overlayOpen(): boolean {
  return !!document.querySelector('[aria-modal="true"]');
}

let chordUntil = 0;

/** Installs the window keydown handler. Returns the cleanup. */
export function installHotkeys(): () => void {
  const onKey = (e: KeyboardEvent) => {
    const st = useUI.getState();
    // Ctrl K everywhere, even inside inputs.
    if ((e.ctrlKey || e.metaKey) && !e.altKey && e.key.toLowerCase() === "k") {
      e.preventDefault();
      st.setPaletteOpen(!st.paletteOpen);
      return;
    }
    if (overlayOpen()) return; // the dialog handles its own keys
    if (e.ctrlKey || e.metaKey || e.altKey) return;

    if (isEditable(e.target)) {
      if (e.key === "Escape") (e.target as HTMLElement).blur();
      return;
    }

    const k = norm(e.key);
    // Enter and Space on a focused control are the control's own click.
    if ((k === "Enter" || k === " ") && e.target instanceof HTMLElement && e.target.closest("button, a, summary, [role='button'], [role='switch'], [role='option']")) return;
    const now = Date.now();
    if (chordUntil > now) {
      chordUntil = 0;
      const s = GO_CHORDS[k];
      if (s) {
        e.preventDefault();
        st.setSection(s);
        return;
      }
    }
    if (k === "g") {
      chordUntil = now + 800;
      return;
    }
    if (/^[1-7]$/.test(k)) {
      e.preventDefault();
      st.setSection(SECTIONS[Number(k) - 1]!);
      return;
    }
    const list = registry.get(k);
    const h = list?.[list.length - 1];
    if (h) {
      e.preventDefault();
      h(e);
    }
  };
  window.addEventListener("keydown", onKey);
  return () => window.removeEventListener("keydown", onKey);
}
