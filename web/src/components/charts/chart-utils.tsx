// 图表共享类型与纯展示辅助。计算口径仍以 src/lib/chart-math.ts 为准，这里只放
// 绘制层反复要用的小函数（像素裁剪、SVG 路径、百分比轴换算、空值判断）。

import type { ReactNode } from "react";
import { formatCompactNumber, formatPercentValue, textWidth } from "@/lib/chart-math";

export interface TimeBucket {
  startedAt: string;
  complete: boolean;
}

export interface ChartSeries {
  id: string;
  label: string;
  /** 可用于传 --series-N；不传时按系列出现顺序取调色板。 */
  color?: string;
  values: Array<number | null | undefined>;
}

export function clamp(value: number, min: number, max: number): number {
  return Math.min(Math.max(value, min), max);
}

export function finite(value: unknown): number | null {
  return typeof value === "number" && Number.isFinite(value) ? value : null;
}

/** 0 是有效读数，null / undefined / NaN 才是缺口。 */
export function readable(value: unknown): value is number {
  return typeof value === "number" && Number.isFinite(value);
}

export function formatNumber(value: number): string {
  return formatCompactNumber(value);
}

/**
 * 按实际标签宽度抽稀 x 轴刻度：不强制保留首尾，只为每个刻度与已选刻度留出
 * gap 像素。调用方应先按跨度换用更粗的间隔；这个函数只是窄容器下的最后兜底。
 * 返回顺序和输入一致，调用方仍在原位绘制。
 */
export function fitTickLabels<T extends { index: number }>(
  ticks: T[],
  {
    label,
    x,
    anchor,
    gap = 12,
    fontSize = 10,
  }: {
    label: (tick: T) => string;
    x: (tick: T) => number;
    anchor: (tick: T, index: number) => "start" | "middle" | "end";
    gap?: number;
    fontSize?: number;
  },
): T[] {
  const ordered = ticks
    .map((tick, index) => ({ tick, index, x: x(tick) }))
    .sort((left, right) => left.x - right.x);
  const selected: typeof ordered = [];
  for (const item of ordered) {
    const width = textWidth(label(item.tick), fontSize);
    const side = anchor(item.tick, item.index);
    const current = side === "start"
      ? { left: item.x, right: item.x + width }
      : side === "end"
        ? { left: item.x - width, right: item.x }
        : { left: item.x - width / 2, right: item.x + width / 2 };
    const previous = selected[selected.length - 1];
    if (previous) {
      const previousWidth = textWidth(label(previous.tick), fontSize);
      const previousSide = anchor(previous.tick, previous.index);
      const previousBounds = previousSide === "start"
        ? { left: previous.x, right: previous.x + previousWidth }
        : previousSide === "end"
          ? { left: previous.x - previousWidth, right: previous.x }
          : { left: previous.x - previousWidth / 2, right: previous.x + previousWidth / 2 };
      if (current.left < previousBounds.right + gap) continue;
    }
    selected.push(item);
  }
  return selected.map((item) => item.tick);
}

export function formatPercentRatio(value: number): string {
  return formatPercentValue(value);
}

/** 顶部圆角、底部直角的柱子；堆叠柱之间才能严丝合缝。 */
export function roundedTopRect(x: number, y: number, width: number, height: number, radius = 3): string {
  if (height <= 0 || width <= 0) return "";
  const r = Math.min(radius, width / 2, height);
  const bottom = y + height;
  return [
    `M${x},${bottom}`,
    `L${x},${y + r}`,
    `Q${x},${y} ${x + r},${y}`,
    `L${x + width - r},${y}`,
    `Q${x + width},${y} ${x + width},${y + r}`,
    `L${x + width},${bottom}`,
    "Z",
  ].join(" ");
}

/** 时间窗口跨度（秒），优先用真实首尾 ts，退化到桶数 × 桶宽。 */
export function bucketSpanSeconds(buckets: TimeBucket[], bucketSeconds?: number): number {
  if (!buckets.length) return 0;
  const first = Date.parse(buckets[0]?.startedAt ?? "");
  const last = Date.parse(buckets[buckets.length - 1]?.startedAt ?? "");
  if (Number.isFinite(first) && Number.isFinite(last) && last > first) return (last - first) / 1000;
  return Math.max(0, buckets.length - 1) * (bucketSeconds ?? 0);
}

/** 统一的空态文案，避免各图各写一套。 */
export const CHART_EMPTY_TEXT = "当前时间窗口内没有数据";

/** 简单的表格容器：所有图表的「表格视图」共用同一密度与滚动边界。 */
export function ChartTable({
  caption,
  head,
  children,
}: {
  caption: string;
  head: ReactNode;
  children: ReactNode;
}) {
  return (
    <div className="max-h-[300px] overflow-auto rounded-control border border-rule">
      <table className="w-full border-collapse text-meta">
        <caption className="sr-only">{caption}</caption>
        <thead className="sticky top-0 z-[1] bg-sunken text-left text-ink-3">{head}</thead>
        <tbody className="text-ink-2">{children}</tbody>
      </table>
    </div>
  );
}
