// 按时间堆叠柱：构成随时间的绝对量或占比。
//
// 柱体以整列为命中区：鼠标移动到某列任意高度都能一次读到该时间点的全部系列。
// 最后一桶未闭合时用斜纹覆盖，而不是把柱子画矮了事。

import { useId, useMemo, useState, type ReactNode } from "react";
import { formatClockSeconds } from "@/lib/format";
import { axisScale, formatPercentValue, timeAxisTicks, type TimeAxisPoint } from "@/lib/chart-math";
import { seriesColor } from "@/lib/series-colors";
import { ChartFrame } from "./ChartFrame";
import { ChartTooltip } from "./ChartTooltip";
import { Legend } from "./Legend";
import {
  CHART_EMPTY_TEXT,
  ChartTable,
  finite,
  roundedTopRect,
  type ChartSeries,
  type TimeBucket,
} from "./chart-utils";
import { useChartSize } from "./useChartSize";

export interface StackedColumnsProps {
  buckets: TimeBucket[];
  series: ChartSeries[];
  format: (value: number) => string;
  height?: number;
  variant?: "stacked" | "percent";
  ariaLabel?: string;
  tableUnit?: string;
  busy?: boolean;
  error?: ReactNode;
  emptyText?: string;
  onPointClick?: (index: number) => void;
  /** 不传时系列数 ≥ 2 才显示图例。 */
  showLegend?: boolean;
  /** 覆盖自动计算的 x 轴目标标签数；仍会按实测宽度做防重叠抽稀。 */
  xTickCount?: number;
  /** 嵌入 Panel 内容区时由 ChartFrame 去掉外框、内边距与标题。 */
  embedded?: boolean;
  className?: string;
}

const PAD = { top: 12, right: 16, bottom: 28, left: 54 };

interface StackPoint {
  id: string;
  label: string;
  color: string;
  value: number | null;
  bottom: number;
  top: number;
}

export function StackedColumns({
  buckets,
  series,
  format,
  height = 220,
  variant = "stacked",
  ariaLabel = "堆叠柱状图",
  tableUnit,
  busy = false,
  error,
  emptyText = CHART_EMPTY_TEXT,
  onPointClick,
  showLegend,
  xTickCount,
  embedded = false,
  className,
}: StackedColumnsProps) {
  const { ref, width } = useChartSize();
  const patternId = useId().replace(/:/g, "");
  const [hidden, setHidden] = useState<Set<string>>(() => new Set());
  const [hovered, setHovered] = useState<number | null>(null);

  const visible = series.filter((item) => !hidden.has(item.id));
  const colors = useMemo(() => {
    const map = new Map<string, string>();
    series.forEach((item) => map.set(item.id, item.color ?? seriesColor(`stacked-columns:${ariaLabel}`, item.id)));
    return map;
  }, [series]);

  const columns = useMemo(() => {
    return buckets.map((_, index) => {
      const values = visible.map((item) => finite(item.values[index]));
      const total = values.reduce<number>((sum, value) => sum + (value ?? 0), 0);
      let bottom = 0;
      const points: StackPoint[] = visible.map((item, itemIndex) => {
        const raw = values[itemIndex];
        const value = variant === "percent" ? (raw === null ? null : total > 0 ? raw / total : 0) : raw;
        const point: StackPoint = {
          id: item.id,
          label: item.label,
          color: colors.get(item.id) ?? "var(--series-1)",
          value,
          bottom,
          top: bottom,
        };
        if (value !== null) {
          point.top = bottom + value;
          bottom += value;
        }
        return point;
      });
      return { index, points, total: variant === "percent" && total > 0 ? 1 : total };
    });
  }, [buckets, visible, colors, variant]);

  const axisValues = columns.flatMap((column) => column.points.flatMap((point) => point.value === null ? [] : [point.top]));
  const axis = useMemo(
    () => axisScale(variant === "percent" ? axisValues.map((value) => value * 100) : axisValues, {
      percent: variant === "percent",
      ticks: 4,
    }),
    [axisValues, variant],
  );

  const plotWidth = Math.max(1, width - PAD.left - PAD.right);
  const plotHeight = Math.max(1, height - PAD.top - PAD.bottom);
  const band = buckets.length ? plotWidth / buckets.length : plotWidth;
  const barWidth = Math.max(1, Math.min(28, band * 0.62));
  const y = (value: number) => {
    const normalized = variant === "percent" ? value * 100 : value;
    return PAD.top + plotHeight - ((normalized - axis.min) / Math.max(1e-9, axis.max - axis.min)) * plotHeight;
  };
  const xTicks = useMemo(
    () => timeAxisTicks(
      buckets.map((bucket) => ({ started_at: bucket.startedAt })),
      {
        plotWidth,
        mode: "band",
        maxTicks: Math.max(2, xTickCount ?? (width < 560 ? 4 : 7)),
        x: (point: TimeAxisPoint) => PAD.left + (point.index + 0.5) * band,
      },
    ),
    [buckets, band, plotWidth, width, xTickCount],
  );

  function toggle(id: string) {
    setHidden((current) => {
      const next = new Set(current);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  }

  return (
    <ChartFrame
      className={className}
      ariaLabel={ariaLabel}
      busy={busy}
      error={error}
      empty={!buckets.length || !series.length}
      emptyText={emptyText}
      legend={(showLegend ?? series.length >= 2) ? <Legend items={series.map((item) => ({ id: item.id, label: item.label, color: colors.get(item.id), marker: "square" }))} hidden={hidden} onToggle={toggle} ariaLabel={`${ariaLabel}图例`} /> : undefined}
      table={<StackTable buckets={buckets} series={visible} colors={colors} variant={variant} format={format} unit={tableUnit} />}
      embedded={embedded}
    >
      <div ref={ref} className="relative w-full" style={{ height }} role="img" aria-label={`${ariaLabel}；悬停或聚焦某一列读取全部系列`}>
        <svg width={width} height={height} viewBox={`0 0 ${width} ${height}`} className="block w-full overflow-visible" aria-hidden="true">
          {axis.ticks.map((tick) => {
            const tickY = y(variant === "percent" ? tick / 100 : tick);
            return (
              <g key={`tick-${tick}`}>
                <line x1={PAD.left} x2={width - PAD.right} y1={tickY} y2={tickY} stroke={tick === axis.min ? "var(--rule-strong)" : "var(--rule)"} strokeWidth={1} />
                <text x={PAD.left - 8} y={tickY + 3.5} textAnchor="end" className="fill-ink-3" style={{ fontSize: 10, fontVariantNumeric: "tabular-nums" }}>
                  {variant === "percent" ? `${Math.round(tick)}%` : format(Number(tick))}
                </text>
              </g>
            );
          })}

          <defs>
            <pattern id={`hatch-${patternId}`} patternUnits="userSpaceOnUse" width="6" height="6" patternTransform="rotate(45)">
              <line x1="0" y1="0" x2="0" y2="6" stroke="var(--warn)" strokeWidth="1.5" opacity="0.7" />
            </pattern>
          </defs>

          {columns.map((column) => {
            const left = PAD.left + column.index * band + (band - barWidth) / 2;
            const incomplete = buckets[column.index]?.complete === false;
            const dim = hovered !== null && hovered !== column.index;
            return (
              <g key={`column-${column.index}`} opacity={dim ? 0.35 : 1} style={{ transition: "opacity 120ms ease" }}>
                {column.points.map((point) => {
                  if (point.value === null || point.value <= 0) return null;
                  const top = y(point.top);
                  const bottom = y(point.bottom);
                  const barHeight = Math.max(0, bottom - top);
                  if (!barHeight) return null;
                  const d = roundedTopRect(left, top, barWidth, barHeight, 3);
                  return <path key={point.id} d={d} fill={point.color} />;
                })}
                {incomplete ? (
                  <rect x={left} y={y(column.total)} width={barWidth} height={Math.max(0, y(0) - y(column.total))} fill={`url(#hatch-${patternId})`} stroke="var(--warn)" strokeWidth={1} pointerEvents="none" />
                ) : null}
              </g>
            );
          })}

          {xTicks.map((tick) => (
            <text
              key={`x-${tick.time}-${tick.index}`}
              x={tick.x}
              y={height - 8}
              textAnchor={tick.anchor}
              className="fill-ink-3"
              style={{ fontSize: 10, fontVariantNumeric: "tabular-nums" }}
            >
              {tick.label}
            </text>
          ))}

          {columns.map((column) => (
            <rect
              key={`hit-${column.index}`}
              x={PAD.left + column.index * band}
              y={PAD.top}
              width={band}
              height={plotHeight}
              fill="transparent"
              style={{ cursor: onPointClick ? "pointer" : "default" }}
              onPointerEnter={() => setHovered(column.index)}
              onPointerLeave={() => setHovered(null)}
              onClick={() => onPointClick?.(column.index)}
            />
          ))}
        </svg>

        {hovered !== null && columns[hovered] ? (
          <ChartTooltip
            open
            x={PAD.left + (hovered + 0.5) * band}
            y={8}
            width={width}
            title={`${formatClockSeconds(buckets[hovered].startedAt)}${buckets[hovered].complete ? "" : " · 累加中"}`}
            note={buckets[hovered].complete ? undefined : "当前桶尚未闭合，读数偏低。"}
            rows={columns[hovered].points.map((point) => ({
              id: point.id,
              label: point.label,
              color: point.color,
              value: point.value === null ? "—" : variant === "percent" ? formatPercentValue(point.value) : format(point.value),
            }))}
          />
        ) : null}
      </div>
    </ChartFrame>
  );
}

function StackTable({
  buckets,
  series,
  colors,
  variant,
  format,
  unit,
}: {
  buckets: TimeBucket[];
  series: ChartSeries[];
  colors: Map<string, string>;
  variant: "stacked" | "percent";
  format: (value: number) => string;
  unit?: string;
}) {
  if (!buckets.length) return null;
  return (
    <ChartTable
      caption="堆叠柱数据表"
      head={
        <tr>
          <th className="whitespace-nowrap px-2 py-1.5 font-medium">时间</th>
          {series.map((item) => (
            <th key={item.id} className="whitespace-nowrap px-2 py-1.5 text-right font-medium">
              <span className="mr-1 inline-block size-2 rounded-[2px] align-middle" style={{ backgroundColor: colors.get(item.id) }} />
              {item.label}{unit ? `（${unit}）` : ""}
            </th>
          ))}
        </tr>
      }
    >
      {buckets.map((bucket, index) => {
        const raw = series.map((item) => finite(item.values[index]));
        const total = raw.reduce<number>((sum, value) => sum + (value ?? 0), 0);
        return (
          <tr key={`${bucket.startedAt}-${index}`} className="border-t border-rule">
            <td className="whitespace-nowrap px-2 py-1 text-ink-3">{formatClockSeconds(bucket.startedAt)}</td>
            {raw.map((value, itemIndex) => (
              <td key={series[itemIndex].id} className="px-2 py-1 text-right tabular-nums">
                {value === null
                  ? "—"
                  : variant === "percent"
                    ? formatPercentValue(total > 0 ? value / total : 0)
                    : format(value)}
              </td>
            ))}
          </tr>
        );
      })}
    </ChartTable>
  );
}
