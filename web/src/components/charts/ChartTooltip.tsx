// 图表读数气泡：位置跟随指针，但会被夹在容器内，不越出图表边界。
//
// 用绝对定位而不是 portal：气泡属于当前图，滚动时随图一起走，也不会在截图中
// 与图表错位。pointer-events: none 保证它不会抢走底层柱/点的 hover。

import type { CSSProperties, ReactNode } from "react";
import { clamp } from "./chart-utils";

export interface TooltipRow {
  id?: string;
  label: ReactNode;
  value: ReactNode;
  color?: string;
}

export interface ChartTooltipProps {
  open: boolean;
  /** 相对于图表容器的像素坐标。 */
  x: number;
  y: number;
  /** 图表容器宽度，用于夹取。 */
  width: number;
  title?: ReactNode;
  rows?: TooltipRow[];
  note?: ReactNode;
  children?: ReactNode;
  className?: string;
}

const ESTIMATED_WIDTH = 210;

export function ChartTooltip({
  open,
  x,
  y,
  width,
  title,
  rows,
  note,
  children,
  className,
}: ChartTooltipProps) {
  if (!open) return null;

  const left = width > ESTIMATED_WIDTH + 16
    ? clamp(x + 12, 8, width - Math.min(ESTIMATED_WIDTH, width - 16) - 8)
    : 8;
  const transform = x > width - ESTIMATED_WIDTH - 24 ? "translateX(-100%)" : undefined;
  const style: CSSProperties = {
    left: transform ? left - 24 : left,
    top: Math.max(4, y),
    transform,
    maxWidth: Math.min(ESTIMATED_WIDTH, Math.max(120, width - 16)),
    pointerEvents: "none",
  };

  return (
    <div
      role="status"
      aria-live="polite"
      className={`absolute z-[3] min-w-[140px] rounded-control border border-rule bg-raised px-2.5 py-2 text-meta text-ink shadow-[var(--shadow-float)] ${className ?? ""}`}
      style={style}
    >
      {title ? <div className="mb-1 border-b border-rule pb-1 text-[11px] text-ink-3">{title}</div> : null}
      {children}
      {rows?.length ? (
        <div className="space-y-1">
          {rows.map((row, index) => (
            <div key={row.id ?? `${index}`} className="flex items-center gap-2">
              {row.color ? (
                <span aria-hidden className="h-[3px] w-3 shrink-0 rounded-full" style={{ backgroundColor: row.color }} />
              ) : null}
              <span className="min-w-0 flex-1 truncate text-ink-3">{row.label}</span>
              <span className="shrink-0 font-medium tabular-nums text-ink">{row.value}</span>
            </div>
          ))}
        </div>
      ) : null}
      {note ? <div className="mt-1 border-t border-rule pt-1 text-[11px] text-ink-3">{note}</div> : null}
    </div>
  );
}
