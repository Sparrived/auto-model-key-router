import type { ReactNode } from "react";
import { X } from "lucide-react";
import { cn } from "@/lib/cn";
import type { BadgeTone } from "./Badge";

export type ChipTone = BadgeTone;

export interface ChipProps {
  children: ReactNode;
  onRemove?: () => void;
  icon?: ReactNode;
  tone?: ChipTone;
  /** 移除按钮的无障碍名称，默认取文本子节点。 */
  label?: string;
  disabled?: boolean;
  className?: string;
}

const toneClasses: Record<ChipTone, string> = {
  neutral: "border-rule bg-panel text-ink-2",
  accent: "border-transparent bg-select text-ink",
  good: "border-transparent bg-good-bg text-good-ink",
  warn: "border-transparent bg-warn-bg text-warn-ink",
  serious: "border-transparent bg-serious-bg text-serious-ink",
  critical: "border-transparent bg-critical-bg text-critical-ink",
};

export function Chip({ children, onRemove, icon, tone = "neutral", label, disabled = false, className }: ChipProps) {
  const accessibleText = label ?? (typeof children === "string" ? children : "该项");

  return (
    <span
      className={cn(
        "inline-flex h-6 max-w-full items-center gap-1 rounded-chip border px-2 text-meta",
        toneClasses[tone],
        disabled && "opacity-60",
        className,
      )}
    >
      {icon ? (
        <span aria-hidden className="flex shrink-0 items-center [&_svg]:size-3">
          {icon}
        </span>
      ) : null}
      <span className="truncate">{children}</span>
      {onRemove ? (
        <button
          type="button"
          onClick={(event) => {
            event.stopPropagation();
            onRemove();
          }}
          aria-label={`移除 ${accessibleText}`}
          disabled={disabled}
          className="-mr-1 flex size-4 shrink-0 items-center justify-center rounded-[3px] text-ink-3 transition-colors hover:bg-hover hover:text-ink disabled:cursor-not-allowed"
        >
          <X aria-hidden className="size-3" />
        </button>
      ) : null}
    </span>
  );
}
