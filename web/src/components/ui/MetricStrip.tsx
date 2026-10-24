import type { ReactNode } from "react";
import { ArrowDown, ArrowUp, Minus } from "lucide-react";
import { cn } from "../../lib/cn";

export type MetricTone = "good" | "warn" | "serious" | "critical" | "neutral";

export interface MetricDelta {
  direction: "up" | "down" | "flat";
  /** 比例值，0.12 显示为 12%（口径同 legacy ui.js stat()）。 */
  value: number;
  polarity: "inverse" | "direct" | "neutral";
}

export interface MetricCellProps {
  label: ReactNode;
  value: ReactNode;
  unit?: ReactNode;
  hint?: ReactNode;
  delta?: MetricDelta;
  tone?: MetricTone;
  /** 概览主读数专用（44px），由页面保证全站只有一处。 */
  hero?: boolean;
  chart?: ReactNode;
  className?: string;
}

export interface MetricStripProps {
  children: ReactNode;
  className?: string;
}

const TONE_TEXT: Record<MetricTone, string> = {
  good: "text-good-ink",
  warn: "text-warn-ink",
  serious: "text-serious-ink",
  critical: "text-critical-ink",
  neutral: "text-neutral-ink",
};

// 颜色口径与 legacy ui.js stat() 一致：先看 flat / neutral，再看极性。
function deltaColor(delta: MetricDelta): string {
  if (delta.direction === "flat" || delta.polarity === "neutral") return "text-ink-3";
  if (delta.polarity === "inverse") {
    return delta.direction === "up" ? "text-critical-ink" : "text-good-ink";
  }
  return delta.direction === "up" ? "text-good-ink" : "text-critical-ink";
}

function formatDeltaPercent(value: number): string {
  if (!Number.isFinite(value)) return "—";
  const percent = Math.abs(value) * 100;
  const rounded = Math.round(percent * 10) / 10;
  return `${rounded.toFixed(Number.isInteger(rounded) ? 0 : 1)}%`;
}

const DELTA_LABEL: Record<MetricDelta["direction"], string> = {
  up: "上升",
  down: "下降",
  flat: "持平",
};

// 按容器宽度换行而不是横向滚动：主区比视口窄得多（侧栏常驻），按视口断点排成一行时
// 900px 左右的窗口就会出现带滚动条的指标带。flex-wrap + 每格 flex-grow 让每一行
// 自己撑满，奇数个指标落单时自然占满整行，不会露出 gap-px 底色拼成的空格。
export function MetricStrip({ children, className }: MetricStripProps) {
  return (
    <div
      className={cn(
        "metric-strip flex w-full flex-wrap gap-px overflow-hidden rounded-panel border border-rule bg-rule [&>*]:bg-panel",
        className,
      )}
    >
      {children}
    </div>
  );
}

export function MetricCell({
  label,
  value,
  unit,
  hint,
  delta,
  tone,
  hero = false,
  chart,
  className,
}: MetricCellProps) {
  const DeltaIcon = delta?.direction === "up" ? ArrowUp : delta?.direction === "down" ? ArrowDown : Minus;

  return (
    <div className={cn("flex min-w-0 flex-[1_1_9.5rem] flex-col px-4 py-3", className)}>
      <div className="text-[12px] leading-4 text-ink-3">{label}</div>
      <div className="mt-1 flex flex-wrap items-baseline gap-x-1.5 gap-y-0.5">
        <span
          className={cn(
            "font-semibold tabular-nums leading-tight",
            hero ? "text-[44px] leading-none" : "text-[28px] leading-none",
            tone ? TONE_TEXT[tone] : "text-ink",
          )}
        >
          {value}
        </span>
        {unit ? <span className="text-[13px] text-ink-3">{unit}</span> : null}
      </div>
      {delta || hint ? (
        <div className="mt-1 flex flex-wrap items-center gap-x-2 gap-y-0.5 text-[12px] leading-4 text-ink-3">
          {delta ? (
            <span
              className={cn("inline-flex items-center gap-0.5 tabular-nums", deltaColor(delta))}
              title={`${DELTA_LABEL[delta.direction]} ${formatDeltaPercent(delta.value)}`}
              aria-label={`${DELTA_LABEL[delta.direction]} ${formatDeltaPercent(delta.value)}`}
            >
              <DeltaIcon aria-hidden className="size-3.5 shrink-0" />
              {formatDeltaPercent(delta.value)}
            </span>
          ) : null}
          {hint ? <span className="min-w-0">{hint}</span> : null}
        </div>
      ) : null}
      {chart ? <div className="mt-auto pt-2">{chart}</div> : null}
    </div>
  );
}
