// 图表图例：可点击切换系列显隐。文字与色块始终同时存在，不能只靠颜色表达。
//
// 隐藏的系列保留在图例里（只是降透明度），这样用户还能把它点回来，也不会因为
// 布局重排而找不到原来的位置。

import type { ReactNode } from "react";
import { cn } from "@/lib/cn";

export interface LegendItem {
  id: string;
  label: ReactNode;
  color?: string;
  marker?: "line" | "square" | "dot";
}

export interface LegendProps {
  items: LegendItem[];
  hidden?: ReadonlySet<string>;
  onToggle?: (id: string) => void;
  className?: string;
  ariaLabel?: string;
}

export function Legend({ items, hidden, onToggle, className, ariaLabel = "图例" }: LegendProps) {
  if (!items.length) return null;
  return (
    <ul className={cn("flex flex-wrap items-center gap-x-3 gap-y-1.5 text-meta text-ink-2", className)} aria-label={ariaLabel}>
      {items.map((item) => {
        const isHidden = hidden?.has(item.id) ?? false;
        const marker = item.marker ?? "line";
        return (
          <li key={item.id}>
            <button
              type="button"
              onClick={onToggle ? () => onToggle(item.id) : undefined}
              aria-pressed={onToggle ? !isHidden : undefined}
              className={cn(
                "inline-flex items-center gap-1.5 rounded-[3px] text-left transition-opacity",
                onToggle ? "cursor-pointer hover:text-ink" : "cursor-default",
                isHidden && "opacity-35",
              )}
            >
              <span
                aria-hidden
                className={cn(
                  "shrink-0",
                  marker === "square" && "size-2.5 rounded-[2px]",
                  marker === "dot" && "size-2 rounded-full",
                  marker === "line" && "h-[3px] w-3.5 rounded-full",
                )}
                style={{ backgroundColor: item.color ?? "var(--series-1)" }}
              />
              <span className="whitespace-nowrap">{item.label}</span>
            </button>
          </li>
        );
      })}
    </ul>
  );
}
