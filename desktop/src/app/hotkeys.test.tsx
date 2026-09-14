import { act, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { useUI } from "@/state/ui";
import { createMockShell, resetUI } from "@/test/mockShell";
import { Dialog } from "@/components/Dialog";
import { installHotkeys, useHotkey } from "./hotkeys";

function press(key: string, target: Element = document.body, init: KeyboardEventInit = {}) {
  const ev = new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true, ...init });
  act(() => {
    target.dispatchEvent(ev);
  });
  return ev;
}

function Page({ onSlash, onEsc, escEnabled = true }: { onSlash: () => void; onEsc: () => void; escEnabled?: boolean }) {
  useHotkey("/", onSlash);
  useHotkey("Escape", onEsc, escEnabled);
  const paletteOpen = useUI((s) => s.paletteOpen);
  const secret = useUI((s) => s.secret);
  return (
    <div>
      <input aria-label="Filter" />
      <button type="button">Go</button>
      {secret && (
        <Dialog open onClose={() => useUI.getState().setSecret(null)} labelledBy="d">
          <span id="d">Dialog</span>
          <input aria-label="Password" />
        </Dialog>
      )}
      {paletteOpen && (
        <Dialog open onClose={() => useUI.getState().setPaletteOpen(false)} labelledBy="p" className="palette" align="top">
          <span id="p">Palette</span>
          <input aria-label="Search" />
        </Dialog>
      )}
    </div>
  );
}

describe("hotkeys", () => {
  let off: () => void;
  beforeEach(() => {
    resetUI();
    createMockShell();
    off = installHotkeys();
  });
  afterEach(() => off());

  it("1..7 switch sections, G chords go to, / focuses the filter, none of it inside inputs", () => {
    const calls: string[] = [];
    render(<Page onSlash={() => calls.push("slash")} onEsc={() => calls.push("esc")} />);
    press("4");
    expect(useUI.getState().section).toBe("quality");
    press("7");
    expect(useUI.getState().section).toBe("settings");
    press("g");
    press("w");
    expect(useUI.getState().section).toBe("wifi");
    const slash = press("/");
    expect(calls).toEqual(["slash"]);
    expect(slash.defaultPrevented).toBe(true); // the key must not be typed anywhere

    const input = screen.getByLabelText("Filter");
    act(() => input.focus());
    press("1", input);
    press("/", input);
    press("r", input);
    expect(useUI.getState().section).toBe("wifi");
    expect(calls).toEqual(["slash"]);
    // Esc in an input blurs it instead of reaching the page handler.
    press("Escape", input);
    expect(document.activeElement).not.toBe(input);
    expect(calls).toEqual(["slash"]);
    // Ctrl K works even inside the input.
    act(() => input.focus());
    press("k", input, { ctrlKey: true });
    expect(useUI.getState().paletteOpen).toBe(true);
    // Enter on a focused button is the button's own click, not a page hotkey.
    act(() => useUI.getState().setPaletteOpen(false));
    const btn = screen.getByRole("button", { name: "Go" });
    act(() => btn.focus());
    const enter = press("Enter", btn);
    expect(enter.defaultPrevented).toBe(false);
  });

  it("Esc closes the palette first, then the dialog, then the pane", async () => {
    const calls: string[] = [];
    render(<Page onSlash={() => calls.push("slash")} onEsc={() => calls.push("pane")} />);
    act(() => useUI.getState().setSecret({ id: "s", connection_uuid: "", connection_name: "", vpn: false, setting_name: "", fields: [], request_new: false, user_requested: false, created_at: "", expires_at: "" }));
    act(() => useUI.getState().setPaletteOpen(true));
    const search = await screen.findByLabelText("Search");
    expect(search).toHaveFocus();
    // Page hotkeys stay quiet while an overlay is open.
    press("1", search);
    press("4");
    expect(useUI.getState().section).toBe("overview");
    press("Escape", search);
    expect(useUI.getState().paletteOpen).toBe(false);
    expect(useUI.getState().secret?.id).toBe("s");
    expect(calls).toEqual([]);
    // Focus returns to the dialog underneath, and its Esc cancels the dialog only.
    const pw = screen.getByLabelText("Password");
    expect(pw).toHaveFocus();
    press("Escape", pw);
    expect(useUI.getState().secret).toBeNull();
    expect(calls).toEqual([]);
    // With nothing on top, Esc reaches the page (the Wi-Fi pane closes on it).
    press("Escape");
    expect(calls).toEqual(["pane"]);
  });
});
