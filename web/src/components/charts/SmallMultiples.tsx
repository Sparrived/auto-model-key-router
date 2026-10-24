// 小倍数：同一时间窗口下的多个单指标趋势，各自独立纵轴，共享横向位置与十字线。
//
// 每格仍是完整的 TimeSeriesChart（含自己的表格视图），外层的 cursorIndex 把
// 悬停/键盘读数同步到所有格；这样比把多个指标塞进一张图更容易比较形状。

import { useState } from "react";
import type { ChartSeries, TimeBucket } from "./chart-utils";
import { TimeSeriesChart } from "./TimeSeriesChart";
import { cn } from "@/lib/cn";

export interface SmallMultiplePanel {
  id: string;
  label: string;
  series: ChartSeries[];
  format?: (value: number) => string;
  percent?: boolean;
  tableUnit?: string;
}

export interface SmallMultiplesProps {
  buckets: TimeBucket[];
  panels: SmallMultiplePanel[];
  format: (value: number) => string;
  height?: number;
  ariaLabel?: string;
  /** 覆盖每个小图的 x 轴目标标签数；仍会按实测宽度做防重叠抽稀。 */
  xTickCount?: number;
  /** 透传给每个 TimeSeriesChart：嵌入 Panel 内容区时去掉外框与标题。 */
  embedded?: boolean;
  className?: string;
}

export function SmallMultiples({
  buckets,
  panels,
  format,
  height = 94,
  ariaLabel = "小倍数趋势",
  xTickCount,
  embedded = false,
  className,
}: SmallMultiplesProps) {
  const [cursor, setCursor] = useState<number | null>(null);
  const [activePanel, setActivePanel] = useState<string | null>(null);

  return (
    <div role="group" aria-label={ariaLabel} className={cn("grid gap-2 sm:grid-cols-2", className)}>
      {panels.map((panel) => (
        <TimeSeriesChart
          key={panel.id}
          buckets={buckets}
          series={panel.series}
          format={panel.format ?? format}
          height={height}
          percent={panel.percent}
          ariaLabel={panel.label}
          tableUnit={panel.tableUnit}
          showLegend={false}
          xTickCount={xTickCount}
          embedded={embedded}
          cursorIndex={cursor}
          onCursorIndexChange={(index) => {
            setCursor(index);
            if (index !== null) setActivePanel(panel.id);
          }}
          showTooltip={activePanel === panel.id}
        />
      ))}
    </div>
  );
}
