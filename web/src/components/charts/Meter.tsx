// 单个比例读数：例如缓存命中率、配额剩余。轨道用同色的低透明度版本，
// 避免在浅色/深色主题里出现一条与填充色无关的灰色底。

import type { ReactNode } from "react";
import { formatPercentValue } from "@/lib/chart-math";
import { ChartFrame } from "./ChartFrame";
import { ChartTable } from "./chart-utils";

export interface MeterProps {
  value: number;
  label?: string;
  hint?: string;
  tone?: "accent" | "good" | "warn" | "critical" | "neutral";
  format?: (value: number) => string;
  showValue?: boolean;
  ariaLabel?: string;
  busy?: boolean;
  error?: ReactNode;
  /** 嵌入 Panel 内容区时由 ChartFrame 去掉外框、内边距与标题。 */
  embedded?: boolean;
  className?: string;
}

const TONE_COLORS: Record<NonNullable<MeterProps["tone"]>, string> = {
  accent: "var(--accent)",
  good: "var(--good)",
  warn: "var(--warn)",
  critical: "var(--critical)",
  neutral: "var(--neutral)",
};

export function Meter({
  value,
  label,
  hint,
  tone = "accent",
  format = formatPercentValue,
  showValue = true,
  ariaLabel,
  busy = false,
  error,
  embedded = false,
  className,
}: MeterProps) {
  const ratio = Number.isFinite(value) ? Math.min(1, Math.max(0, value)) : 0;
  const color = TONE_COLORS[tone];
  return (
    <ChartFrame
      className={className}
      ariaLabel={ariaLabel ?? label ?? "比例读数"}
      title={label}
      busy={busy}
      error={error}
      subtitle={hint}
      table={
        <ChartTable
          caption={label ?? "比例读数"}
          head={<tr><th className="px-2 py-1.5 font-medium">项目</th><th className="px-2 py-1.5 text-right font-medium">值</th></tr>}
        >
          <tr className="border-t border-rule"><td className="px-2 py-1 text-ink-2">{label ?? "比例"}</td><td className="px-2 py-1 text-right tabular-nums">{format(ratio)}</td></tr>
        </ChartTable>
      }
      embedded={embedded}
    >
      <div className="space-y-1.5">
        <div className="flex items-baseline justify-between gap-2">
          <span className="text-meta text-ink-3">{hint ?? "当前窗口"}</span>
          {showValue ? <span className="text-title font-semibold tabular-nums text-ink">{format(ratio)}</span> : null}
        </div>
        <div
          role="meter"
          aria-valuemin={0}
          aria-valuemax={1}
          aria-valuenow={Number(ratio.toFixed(4))}
          aria-label={ariaLabel ?? label ?? "比例读数"}
          className="h-2.5 w-full overflow-hidden rounded-full"
          style={{ backgroundColor: `color-mix(in srgb, ${color} 18%, transparent)` }}
        >
          <div className="h-full rounded-full transition-[width]" style={{ width: `${ratio * 100}%`, backgroundColor: color }} />
        </div>
      </div>
    </ChartFrame>
  );
}
