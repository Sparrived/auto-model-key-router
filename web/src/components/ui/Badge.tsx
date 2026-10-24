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
  neutral: "bg-neutral-bg text-neutral-ink",
  good: "bg-good-bg text-good-ink",
  warn: "bg-warn-bg text-warn-ink",
  serious: "bg-serious-bg text-serious-ink",
  critical: "bg-critical-bg text-critical-ink",
  accent: "bg-select text-ink",
};

const dotClasses: Record<BadgeTone, string> = {
  neutral: "bg-neutral",
  good: "bg-good",
  warn: "bg-warn",
  serious: "bg-serious",
  critical: "bg-critical",
  accent: "bg-ink",
};

export function Badge({ children, tone = "neutral", dot = false, className }: BadgeProps) {
  return (
    <span
      className={cn(
        "inline-flex h-5 items-center gap-1 whitespace-nowrap rounded-chip px-1.5 text-meta font-medium",
        toneClasses[tone],
        className,
      )}
    >
      {dot ? <span aria-hidden className={cn("size-1.5 shrink-0 rounded-pill", dotClasses[tone])} /> : null}
      {children}
    </span>
  );
}
