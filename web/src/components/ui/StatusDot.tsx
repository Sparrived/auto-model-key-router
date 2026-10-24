import type { ReactNode } from "react";
import { cn } from "@/lib/cn";
import type { BadgeTone } from "./Badge";

export type StatusTone = BadgeTone;
export type StatusDotSize = "sm" | "md";

interface StatusDotBaseProps {
  tone: StatusTone;
  size?: StatusDotSize;
  pulse?: boolean;
  className?: string;
}

/** 无文字时必须给 aria-label：颜色是唯一载体的事不做。 */
export type StatusDotProps = StatusDotBaseProps &
  ({ label: ReactNode; "aria-label"?: string } | { label?: undefined; "aria-label": string });

const dotClasses: Record<StatusTone, string> = {
  neutral: "bg-neutral",
  good: "bg-good",
  warn: "bg-warn",
  serious: "bg-serious",
  critical: "bg-critical",
  accent: "bg-ink",
};

export function StatusDot(props: StatusDotProps) {
  const { tone, size = "md", pulse = false, className } = props;
  const label = props.label;
  const ariaLabel = props["aria-label"];

  return (
    <span
      role={label ? undefined : "img"}
      aria-label={label ? undefined : ariaLabel}
      className={cn("inline-flex items-center gap-1.5", size === "sm" ? "text-meta" : "text-body", className)}
    >
      <span aria-hidden className="relative flex shrink-0">
        {pulse ? (
          <span
            className={cn(
              "absolute inline-flex size-full rounded-pill opacity-60 motion-reduce:animate-none",
              "animate-ping",
              dotClasses[tone],
            )}
          />
        ) : null}
        <span className={cn("relative inline-flex rounded-pill", size === "sm" ? "size-1.5" : "size-2", dotClasses[tone])} />
      </span>
      {label ? <span className="text-ink-2">{label}</span> : null}
    </span>
  );
}
