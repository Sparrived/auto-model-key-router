import type { ReactNode } from "react";
import { cn } from "@/lib/cn";

export type BadgeTone = "neutral" | "good" | "warn" | "serious" | "critical" | "accent";

export interface BadgeProps {
  children: ReactNode;
  tone?: BadgeTone;
  dot?: boolean;
  className?: string;
}

const toneClasses: Record<BadgeTone, string> = {
  neutral: "border-rule-strong bg-transparent text-ink-2",
  good: "border-good/40 bg-transparent text-good-ink",
  warn: "border-warn/45 bg-transparent text-warn-ink",
  serious: "border-serious/45 bg-transparent text-serious-ink",
  critical: "border-critical/45 bg-transparent text-critical-ink",
  accent: "border-accent-ring bg-accent-soft text-accent",
};

const dotClasses: Record<BadgeTone, string> = {
  neutral: "bg-neutral",
  good: "bg-good",
  warn: "bg-warn",
  serious: "bg-serious",
  critical: "bg-critical",
  accent: "bg-accent",
};

export function Badge({ children, tone = "neutral", dot = false, className }: BadgeProps) {
  return (
    <span
      className={cn(
        "inline-flex h-5 items-center gap-1 whitespace-nowrap rounded-chip border px-1.5 font-mono text-[11px] font-medium",
        toneClasses[tone],
        className,
      )}
    >
      {dot ? <span aria-hidden className={cn("size-1.5 shrink-0 rounded-pill", dotClasses[tone])} /> : null}
      {children}
    </span>
  );
}
