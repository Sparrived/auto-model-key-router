// 内联迷你趋势：只表达方向，不显示坐标轴；悬停仍可读单点数值。
//
// 放在表格行里时高度只有 28px，任何网格/标签都会把行高撑坏，因此这里只用
// 一条实线 + 末点。缺口断线，不补零。

import { useState } from "react";
import { formatClockSeconds } from "@/lib/format";
import { ChartTooltip } from "./ChartTooltip";
import { clamp, readable } from "./chart-utils";
import { useChartSize } from "./useChartSize";

export interface SparklineProps {
  values: Array<number | null | undefined>;
  color?: string;
  height?: number;
  ariaLabel?: string;
  format?: (value: number) => string;
  timestamps?: string[];
  className?: string;
}

const PAD = { top: 3, right: 4, bottom: 3, left: 2 };

export function Sparkline({
  values,
  color = "var(--series-1)",
  height = 28,
  ariaLabel = "迷你趋势",
  format = (value) => String(value),
  timestamps,
  className,
}: SparklineProps) {
  const { ref, width } = useChartSize(160);
  const [hovered, setHovered] = useState<number | null>(null);

  const finiteValues = values.filter(readable);
  const min = finiteValues.length ? Math.min(...finiteValues) : 0;
  const max = finiteValues.length ? Math.max(...finiteValues) : 0;
  const range = max - min || 1;
  const plotWidth = Math.max(1, width - PAD.left - PAD.right);
  const plotHeight = Math.max(1, height - PAD.top - PAD.bottom);
  const x = (index: number) => (values.length <= 1 ? PAD.left + plotWidth / 2 : PAD.left + (index / (values.length - 1)) * plotWidth);
  const y = (value: number) => PAD.top + plotHeight - ((value - min) / range) * plotHeight;

  const segments: Array<Array<{ index: number; x: number; y: number }>> = [];
  let current: Array<{ index: number; x: number; y: number }> = [];
  values.forEach((value, index) => {
    if (!readable(value)) {
      if (current.length) segments.push(current);
      current = [];
      return;
    }
    current.push({ index, x: x(index), y: y(value) });
  });
  if (current.length) segments.push(current);
  const last = segments[segments.length - 1]?.at(-1);

  function handlePointer(event: React.PointerEvent<SVGRectElement>) {
    const rect = event.currentTarget.getBoundingClientRect();
    const ratio = rect.width > 0 ? (event.clientX - rect.left) / rect.width : 0;
    setHovered(clamp(Math.round(ratio * (values.length - 1)), 0, Math.max(0, values.length - 1)));
  }

  return (
    <div ref={ref} className={`relative inline-block w-full align-middle ${className ?? ""}`} style={{ height }} role="img" aria-label={ariaLabel}>
      <svg width={width} height={height} viewBox={`0 0 ${width} ${height}`} className="block w-full overflow-visible" aria-hidden="true">
        {segments.map((segment, index) => (
          <path
            key={index}
            d={segment.map((point, pointIndex) => `${pointIndex === 0 ? "M" : "L"}${point.x.toFixed(2)},${point.y.toFixed(2)}`).join(" ")}
            fill="none"
            stroke={color}
            strokeWidth={1.5}
            strokeLinecap="round"
            strokeLinejoin="round"
          />
        ))}
        {last ? <circle cx={last.x} cy={last.y} r={2.5} fill={color} /> : null}
        <rect
          x={PAD.left}
          y={PAD.top}
          width={plotWidth}
          height={plotHeight}
          fill="transparent"
          onPointerMove={handlePointer}
          onPointerLeave={() => setHovered(null)}
        />
      </svg>
      {hovered !== null && values[hovered] !== null && values[hovered] !== undefined ? (
        <ChartTooltip
          open
          x={x(hovered)}
          y={-height - 36}
          width={width}
          title={timestamps?.[hovered] ? formatClockSeconds(timestamps[hovered]) : undefined}
          rows={[{ label: "数值", value: format(values[hovered] as number), color }]}
        />
      ) : null}
    </div>
  );
}
