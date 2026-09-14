import type { IconName } from "@/design/icon-names";

export type Tone = "ok" | "warn" | "error" | "accent" | "vpn" | "wifi";

export interface IconProps {
  name: IconName;
  size?: "sm" | "md" | "lg" | "xl" | "2xl";
  tone?: Tone | "sub" | "muted";
  /** Accessible name. Without one the icon is decorative (aria-hidden). */
  label?: string;
  className?: string;
  spin?: boolean;
}

const SIZE: Record<NonNullable<IconProps["size"]>, string> = { sm: "ic ic-sm", md: "ic", lg: "ic ic-lg", xl: "ic ic-xl", "2xl": "ic ic-2xl" };

/** Phosphor glyph from the design's sprite: <svg class="ic"><use href="#ph-name"/></svg>. */
export function Icon({ name, size = "md", tone, label, className, spin }: IconProps) {
  const cls = [SIZE[size], tone ? (tone === "sub" || tone === "muted" ? tone : `tone-${tone}`) : "", spin ? "spin" : "", className ?? ""].filter(Boolean).join(" ");
  return (
    <svg className={cls} {...(label ? { role: "img", "aria-label": label } : { "aria-hidden": true })} focusable="false">
      <use href={`#ph-${name}`} />
    </svg>
  );
}
