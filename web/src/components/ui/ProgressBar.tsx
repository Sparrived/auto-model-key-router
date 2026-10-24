import type { ReactNode } from "react";
import { cn } from "@/lib/cn";

export type ProgressTone = "neutral" | "good" | "warn" | "critical" | "accent";

export interface ProgressBarProps {
  value: number;
  tone?: ProgressTone;
  label?: ReactNode;
  showValue?: boolean;
  className?: string;
  "aria-label"?: string;
}

const toneClasses: Record<ProgressTone, string> = {
  neutral: "bg-neutral",
  good: "bg-good",
  warn: "bg-warn",
  critical: "bg-critical",
  accent: "bg-accent",
};

export function ProgressBar({
  value,
  tone = "neutral",
  label,
  showValue = false,
  className,
  "aria-label": ariaLabel,
}: ProgressBarProps) {
  const clamped = Math.min(100, Math.max(0, Number.isFinite(value) ? value : 0));
  const percent = Math.round(clamped);

  return (
    <div className={cn("flex w-full flex-col gap-1", className)}>
      {label || showValue ? (
        <div className="flex items-baseline justify-between gap-2 text-meta text-ink-2">
          <span>{label}</span>
          {showValue ? <span className="tabular-nums text-ink-3">{percent}%</span> : null}
        </div>
      ) : null}
      <div
        role="progressbar"
        aria-valuenow={percent}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-label={typeof label === "string" ? label : ariaLabel}
        className="h-1 w-full overflow-hidden rounded-pill bg-sunken"
      >
        <div
          className={cn("h-full rounded-pill transition-[width] duration-[var(--motion-panel)]", toneClasses[tone])}
          style={{ width: `${clamped}%` }}
        />
      </div>
    </div>
  );
}
