import type { ReactNode } from "react";
import { CircleCheck, Info, OctagonAlert, TriangleAlert, X, type LucideIcon } from "lucide-react";
import { cn } from "@/lib/cn";

export type BannerTone = "neutral" | "info" | "good" | "warn" | "serious" | "critical";

export interface BannerProps {
  tone?: BannerTone;
  /** 覆盖默认的 tone 图标。 */
  icon?: ReactNode;
  title?: ReactNode;
  children?: ReactNode;
  action?: ReactNode;
  onDismiss?: () => void;
  className?: string;
}

const toneClasses: Record<BannerTone, string> = {
  neutral: "bg-sunken text-ink-2",
  info: "bg-select text-ink-2",
  good: "bg-good-bg text-good-ink",
  warn: "bg-warn-bg text-warn-ink",
  serious: "bg-serious-bg text-serious-ink",
  critical: "bg-critical-bg text-critical-ink",
};

const toneIcons: Record<BannerTone, LucideIcon> = {
  neutral: Info,
  info: Info,
  good: CircleCheck,
  warn: TriangleAlert,
  serious: TriangleAlert,
  critical: OctagonAlert,
};

export function Banner({ tone = "neutral", icon, title, children, action, onDismiss, className }: BannerProps) {
  const Icon = toneIcons[tone];
  const role = tone === "critical" || tone === "serious" ? "alert" : "status";

  return (
    <div
      role={role}
      className={cn("flex items-start gap-2 rounded-control px-3 py-2 text-meta", toneClasses[tone], className)}
    >
      <span className="mt-0.5 flex shrink-0 items-center [&_svg]:size-4">
        {icon ?? <Icon aria-hidden className="size-4" />}
      </span>
      <div className="min-w-0 flex-1">
        {title ? <p className="font-medium">{title}</p> : null}
        {children ? <div className={cn(title && "mt-0.5")}>{children}</div> : null}
      </div>
      {action ? <div className="shrink-0">{action}</div> : null}
      {onDismiss ? (
        <button
          type="button"
          onClick={onDismiss}
          aria-label="关闭"
          className="-mr-1 flex size-5 shrink-0 items-center justify-center rounded-[4px] text-ink-3 transition-colors hover:bg-hover hover:text-ink"
        >
          <X aria-hidden className="size-3.5" />
        </button>
      ) : null}
    </div>
  );
}
