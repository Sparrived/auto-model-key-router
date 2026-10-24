// 时间序列图：折线 / 面积 / 堆叠面积 / 百分比堆叠面积共用一套坐标系。
//
// 关键约定：
// - 缺失读数（null）断线，不补 0；两侧都有读数时用虚线桥接，告诉读者"这里没有
//   采样"，而不是"这里掉到 0"。
// - 最后一个桶若 complete=false，尾段用虚线，读作"累加中"。
// - 指针吸附最近桶，一个 tooltip 列出全部系列；左右方向键可在桶之间移动。
// - 图例可点击隐藏系列；表格视图是同一份数据的可读替代。

import { useMemo, useState, type ReactNode } from "react";
import { formatClockSeconds } from "@/lib/format";
import {
  axisScale,
  formatPercentValue,
  gapBridges,
  timeAxisTicks,
} from "@/lib/chart-math";
import { seriesColor } from "@/lib/series-colors";
import { ChartFrame } from "./ChartFrame";
import { ChartTooltip } from "./ChartTooltip";
import { Legend } from "./Legend";
import {
  CHART_EMPTY_TEXT,
  ChartTable,
  clamp,
  finite,
  type ChartSeries,
  type TimeBucket,
} from "./chart-utils";
import { useChartSize } from "./useChartSize";

export type TimeSeriesVariant = "line" | "area" | "stacked-area" | "stacked-percent";

export interface ReferenceLine {
  value: number;
  label?: string;
  color?: string;
}

export interface TimeSeriesChartProps {
  buckets: TimeBucket[];
  series: ChartSeries[];
  /**
   * 数值展示函数。percent 图表传入的 value 仍是原始计量（如 0.984），
   * 由组件在坐标轴上换算成 0–100%，tooltip 用这个函数展示。
   */
  format: (value: number) => string;
  height?: number;
  variant?: TimeSeriesVariant;
  /** 指标本身是 0–1 的比率；坐标轴显示 0–100%。 */
  percent?: boolean;
  /** 从窗口起点开始的累计值。 */
  cumulative?: boolean;
  referenceLines?: ReferenceLine[];
  ariaLabel?: string;
  tableUnit?: string;
  busy?: boolean;
  error?: ReactNode;
  emptyText?: string;
  onPointClick?: (index: number) => void;
  /** 外部同步十字线；不传则内部管理。 */
  cursorIndex?: number | null;
  onCursorIndexChange?: (index: number | null) => void;
  showTooltip?: boolean;
  /** 不传时系列数 ≥ 2 才显示图例；单系列图由标题说明指标。 */
  showLegend?: boolean;
  showXAxis?: boolean;
  /** 覆盖自动计算的 x 轴目标标签数；仍会按实测宽度做防重叠抽稀。 */
  xTickCount?: number;
  /** 嵌入 Panel 内容区时由 ChartFrame 去掉外框、内边距与标题。 */
  embedded?: boolean;
  className?: string;
}

interface RenderedPoint {
  index: number;
  value: number | null;
  top: number | null;
  bottom: number | null;
}

const PAD = { top: 12, right: 16, bottom: 28, left: 54 };

export function TimeSeriesChart({
  buckets,
  series,
  format,
  height = 220,
  variant = "line",
  percent = false,
  cumulative = false,
  referenceLines = [],
  ariaLabel = "时间序列图",
  tableUnit,
  busy = false,
  error,
  emptyText = CHART_EMPTY_TEXT,
  onPointClick,
  cursorIndex,
  onCursorIndexChange,
  showTooltip = true,
  showLegend,
  showXAxis = true,
  xTickCount,
  embedded = false,
  className,
}: TimeSeriesChartProps) {
  const { ref, width } = useChartSize();
  const [hidden, setHidden] = useState<Set<string>>(() => new Set());
  const [internalCursor, setInternalCursor] = useState<number | null>(null);

  const isPercent = percent || variant === "stacked-percent";
  const isStacked = variant === "stacked-area" || variant === "stacked-percent";
  const activeIndex = cursorIndex === undefined ? internalCursor : cursorIndex;

  const visibleSeries = useMemo(
    () => series.filter((item) => !hidden.has(item.id)),
    [series, hidden],
  );

  const colorOf = useMemo(() => {
    const map = new Map<string, string>();
    series.forEach((item) => {
      map.set(item.id, item.color ?? seriesColor(`time-series:${ariaLabel}`, item.id));
    });
    return map;
  }, [series]);

  // 每个系列在「原始值 / 累计 / 堆叠占比」三种口径下的可绘制点。
  const rendered = useMemo(() => {
    const out = new Map<string, RenderedPoint[]>();
    for (const item of visibleSeries) {
      let acc = 0;
      const points: RenderedPoint[] = buckets.map((_, index) => {
        const raw = finite(item.values[index]);
        let value: number | null = raw;
        if (cumulative && variant !== "stacked-percent" && variant !== "stacked-area") {
          if (raw !== null) {
            acc += raw;
            value = acc;
          } else {
            value = null;
          }
        }
        return { index, value, top: null, bottom: null };
      });
      out.set(item.id, points);
    }

    if (isStacked) {
      // 先求每个桶可见系列的总量，再逐层累加。占比堆叠按每个桶的总量归一化。
      for (let index = 0; index < buckets.length; index += 1) {
        const values = visibleSeries.map((item) => finite(item.values[index]));
        const total = values.reduce<number>((sum, value) => sum + (value ?? 0), 0);
        let bottomValue = 0;
        visibleSeries.forEach((item, seriesIndex) => {
          const raw = values[seriesIndex];
          const point = out.get(item.id)?.[index];
          if (!point) return;
          if (raw === null) {
            point.value = null;
            point.top = null;
            point.bottom = null;
            return;
          }
          const share = total > 0 ? raw / total : 0;
          const value = variant === "stacked-percent" ? share : raw;
          point.value = value;
          point.bottom = bottomValue;
          point.top = bottomValue + value;
          bottomValue += value;
        });
      }
    } else {
      for (const item of visibleSeries) {
        for (const point of out.get(item.id) ?? []) {
          if (point.value === null) continue;
          point.top = point.value;
          point.bottom = 0;
        }
      }
    }
    return out;
  }, [buckets, visibleSeries, cumulative, isStacked, variant]);

  const axisValues = useMemo(() => {
    const values: number[] = [];
    for (const item of visibleSeries) {
      for (const point of rendered.get(item.id) ?? []) {
        if (point.top !== null) values.push(point.top);
        if (point.value !== null) values.push(point.value);
      }
    }
    for (const line of referenceLines) values.push(line.value);
    return values;
  }, [visibleSeries, rendered, referenceLines]);

  // chart-math 的 percent 轴以 0–100 为单位；数据域的 0–1 在这里换算，刻度
  // 仍显示成 0%..100%，不会出现 0.25% 这种错读。
  const axis = useMemo(
    () => axisScale(isPercent ? axisValues.map((value) => value * 100) : axisValues, {
      percent: isPercent,
      ticks: 4,
    }),
    [axisValues, isPercent],
  );

  const plotWidth = Math.max(1, width - PAD.left - PAD.right);
  const plotHeight = Math.max(1, height - PAD.top - PAD.bottom);
  const x = (index: number) => (buckets.length <= 1 ? PAD.left + plotWidth / 2 : PAD.left + (index / (buckets.length - 1)) * plotWidth);
  const y = (value: number) => {
    const normalized = isPercent ? value * 100 : value;
    return PAD.top + plotHeight - ((normalized - axis.min) / Math.max(1e-9, axis.max - axis.min)) * plotHeight;
  };
  const firstTime = Date.parse(buckets[0]?.startedAt ?? "");
  const lastTime = Date.parse(buckets[buckets.length - 1]?.startedAt ?? "");
  const xAtTime = (time: number) => (
    buckets.length <= 1 || !Number.isFinite(firstTime) || !Number.isFinite(lastTime) || lastTime <= firstTime
      ? PAD.left + plotWidth / 2
      : PAD.left + ((time - firstTime) / (lastTime - firstTime)) * plotWidth
  );
  const xTicks = useMemo(
    () => timeAxisTicks(
      buckets.map((bucket) => ({ started_at: bucket.startedAt })),
      {
        plotWidth,
        mode: "line",
        maxTicks: Math.max(2, xTickCount ?? (width < 560 ? 4 : 7)),
        x: (point) => xAtTime(point.time),
        anchor: (point) => point.time <= firstTime ? "start" : point.time >= lastTime ? "end" : "middle",
      },
    ),
    [buckets, firstTime, lastTime, plotWidth, width, xTickCount],
  );

  function setCursor(index: number | null) {
    if (cursorIndex === undefined) setInternalCursor(index);
    onCursorIndexChange?.(index);
  }

  function toggle(id: string) {
    setHidden((current) => {
      const next = new Set(current);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  }

  function handlePointer(event: React.PointerEvent<SVGRectElement>) {
    if (!buckets.length) return;
    const rect = event.currentTarget.getBoundingClientRect();
    const ratio = rect.width > 0 ? (event.clientX - rect.left) / rect.width : 0;
    setCursor(clamp(Math.round(ratio * (buckets.length - 1)), 0, buckets.length - 1));
  }

  function handleKey(event: React.KeyboardEvent<HTMLDivElement>) {
    if (!buckets.length) return;
    if (event.key !== "ArrowLeft" && event.key !== "ArrowRight" && event.key !== "Home" && event.key !== "End") return;
    event.preventDefault();
    const current = activeIndex ?? (event.key === "ArrowLeft" ? buckets.length - 1 : 0);
    const next = event.key === "Home"
      ? 0
      : event.key === "End"
        ? buckets.length - 1
        : clamp(current + (event.key === "ArrowRight" ? 1 : -1), 0, buckets.length - 1);
    setCursor(next);
  }

  const legendItems = series.map((item) => ({
    id: item.id,
    label: item.label,
    color: colorOf.get(item.id),
    marker: "line" as const,
  }));

  const empty = buckets.length === 0 || series.length === 0;

  return (
    <ChartFrame
      className={className}
      ariaLabel={ariaLabel}
      busy={busy}
      error={error}
      empty={empty}
      emptyText={emptyText}
      legend={(showLegend ?? series.length >= 2) ? <Legend items={legendItems} hidden={hidden} onToggle={toggle} ariaLabel={`${ariaLabel}图例`} /> : undefined}
      table={<TimeSeriesTable buckets={buckets} series={visibleSeries} rendered={rendered} format={format} isPercent={isPercent} unit={tableUnit} />}
      embedded={embedded}
    >
      <div
        ref={ref}
        className="relative w-full outline-none focus-visible:ring-2 focus-visible:ring-focus"
        style={{ height }}
        tabIndex={0}
        role="img"
        aria-label={`${ariaLabel}；使用左右方向键读取各时间点`}
        onKeyDown={handleKey}
        onFocus={() => {
          if (activeIndex === null && buckets.length) setCursor(buckets.length - 1);
        }}
      >
        <svg width={width} height={height} viewBox={`0 0 ${width} ${height}`} className="block w-full overflow-visible" aria-hidden="true">
          {/* 网格与 Y 轴 */}
          {axis.ticks.map((tick) => {
            const tickY = y(isPercent ? tick / 100 : tick);
            return (
              <g key={`tick-${tick}`}>
                <line x1={PAD.left} x2={width - PAD.right} y1={tickY} y2={tickY} stroke={tick === axis.min ? "var(--rule-strong)" : "var(--rule)"} strokeWidth={1} />
                <text x={PAD.left - 8} y={tickY + 3.5} textAnchor="end" className="fill-ink-3" style={{ fontSize: 10, fontVariantNumeric: "tabular-nums" }}>
                  {isPercent ? `${Math.round(tick)}%` : format(Number(tick))}
                </text>
              </g>
            );
          })}

          {/* 参考线 */}
          {referenceLines.map((line, index) => {
            const lineY = y(line.value);
            return (
              <g key={`ref-${index}`}>
                <line x1={PAD.left} x2={width - PAD.right} y1={lineY} y2={lineY} stroke={line.color ?? "var(--warn)"} strokeWidth={1.25} strokeDasharray="4 4" />
                {line.label ? (
                  <text x={width - PAD.right} y={lineY - 4} textAnchor="end" className="fill-ink-3" style={{ fontSize: 10 }}>
                    {line.label}
                  </text>
                ) : null}
              </g>
            );
          })}

          {/* 面积与折线 */}
          {visibleSeries.map((item) => {
            const points = rendered.get(item.id) ?? [];
            const color = colorOf.get(item.id) ?? "var(--series-1)";
            const showArea = variant !== "line";
            return (
              <g key={item.id}>
                {showArea ? (
                  <AreaPath points={points} x={x} y={y} color={color} opacity={isStacked ? 0.14 : 0.13} />
                ) : null}
                <LinePath
                  points={points}
                  x={x}
                  y={y}
                  color={color}
                  partial={buckets[buckets.length - 1]?.complete === false}
                  strokeWidth={isStacked ? 1.5 : 2.25}
                />
                <GapBridges points={points} x={x} y={y} color={color} />
              </g>
            );
          })}

          {/* X 轴 */}
          {showXAxis ? xTicks.map((tick) => (
            <text
              key={`x-${tick.time}`}
              x={tick.x}
              y={height - 8}
              textAnchor={tick.anchor}
              className="fill-ink-3"
              style={{ fontSize: 10, fontVariantNumeric: "tabular-nums" }}
            >
              {tick.label}
            </text>
          )) : null}

          {/* 十字线与读数点 */}
          {activeIndex !== null && buckets[activeIndex] ? (
            <g pointerEvents="none">
              <line x1={x(activeIndex)} x2={x(activeIndex)} y1={PAD.top} y2={PAD.top + plotHeight} stroke="var(--ink-3)" strokeWidth={1} strokeDasharray="3 3" />
              {visibleSeries.map((item) => {
                const point = rendered.get(item.id)?.[activeIndex];
                if (!point || point.top === null) return null;
                return <circle key={`marker-${item.id}`} cx={x(activeIndex)} cy={y(point.top)} r={3.5} fill="var(--panel)" stroke={colorOf.get(item.id)} strokeWidth={2.25} />;
              })}
            </g>
          ) : null}

          {/* 透明命中区 */}
          <rect
            x={PAD.left}
            y={PAD.top}
            width={plotWidth}
            height={plotHeight}
            fill="transparent"
            style={{ cursor: onPointClick ? "pointer" : "crosshair" }}
            onPointerMove={handlePointer}
            onPointerLeave={() => setCursor(null)}
            onClick={() => {
              if (activeIndex !== null && onPointClick) onPointClick(activeIndex);
            }}
          />
        </svg>

        {showTooltip && activeIndex !== null && buckets[activeIndex] ? (
          <ChartTooltip
            open
            x={x(activeIndex)}
            y={8}
            width={width}
            title={`${formatClockSeconds(buckets[activeIndex].startedAt)}${buckets[activeIndex].complete ? "" : " · 累加中"}`}
            note={buckets[activeIndex].complete ? undefined : "当前桶尚未闭合，读数偏低。"}
            rows={visibleSeries.map((item) => {
              const point = rendered.get(item.id)?.[activeIndex];
              if (!point || point.value === null) {
                return { id: item.id, label: item.label, value: "—", color: colorOf.get(item.id) };
              }
              const display = isPercent && variant === "stacked-percent"
                ? formatPercentValue(point.value)
                : format(point.value);
              return { id: item.id, label: item.label, value: display, color: colorOf.get(item.id) };
            })}
          />
        ) : null}
      </div>
    </ChartFrame>
  );
}

function LinePath({
  points,
  x,
  y,
  color,
  partial,
  strokeWidth,
}: {
  points: RenderedPoint[];
  x: (index: number) => number;
  y: (value: number) => number;
  color: string;
  partial: boolean;
  strokeWidth: number;
}) {
  const segments = splitReadable(points);
  return (
    <>
      {segments.map((segment, index) => {
        if (segment.length === 1) {
          const point = segment[0];
          if (point.top === null) return null;
          return <circle key={`single-${index}`} cx={x(point.index)} cy={y(point.top)} r={2} fill={color} />;
        }
        const isTail = partial && segment[segment.length - 1].index === points.length - 1;
        const pointPath = (list: RenderedPoint[]) => list
          .map((point, pointIndex) => `${pointIndex === 0 ? "M" : "L"}${x(point.index).toFixed(2)},${y(point.top as number).toFixed(2)}`)
          .join(" ");
        // 只把「最后一段」画成虚线；整段都虚线会让人误以为整个窗口都未闭合。
        if (isTail && segment.length >= 2) {
          const solid = segment.slice(0, -1);
          return (
            <g key={`line-${index}`}>
              <path d={pointPath(solid)} fill="none" stroke={color} strokeWidth={strokeWidth} strokeLinecap="round" strokeLinejoin="round" />
              <path d={pointPath(segment.slice(-2))} fill="none" stroke={color} strokeWidth={strokeWidth} strokeLinecap="round" strokeLinejoin="round" strokeDasharray="4 4" />
            </g>
          );
        }
        return (
          <path
            key={`line-${index}`}
            d={pointPath(segment)}
            fill="none"
            stroke={color}
            strokeWidth={strokeWidth}
            strokeLinecap="round"
            strokeLinejoin="round"
            strokeDasharray={isTail ? "4 4" : undefined}
          />
        );
      })}
    </>
  );
}

function AreaPath({
  points,
  x,
  y,
  color,
  opacity,
}: {
  points: RenderedPoint[];
  x: (index: number) => number;
  y: (value: number) => number;
  color: string;
  opacity: number;
}) {
  const segments = splitReadable(points);
  return (
    <>
      {segments.map((segment, index) => {
        if (segment.length < 2) return null;
        const top = segment.map((point) => `${x(point.index).toFixed(2)},${y(point.top as number).toFixed(2)}`);
        const bottom = [...segment].reverse().map((point) => `${x(point.index).toFixed(2)},${y(point.bottom ?? 0).toFixed(2)}`);
        return (
          <path
            key={`area-${index}`}
            d={`M${top.join(" L")} L${bottom.join(" L")} Z`}
            fill={color}
            fillOpacity={opacity}
            stroke="none"
          />
        );
      })}
    </>
  );
}

function GapBridges({
  points,
  x,
  y,
  color,
}: {
  points: RenderedPoint[];
  x: (index: number) => number;
  y: (value: number) => number;
  color: string;
}) {
  return (
    <>
      {gapBridges(points.map((point) => ({ value: point.top }))).map((bridge) => {
        const from = points[bridge.from];
        const to = points[bridge.to];
        if (!from || !to || from.top === null || to.top === null) return null;
        return (
          <line
            key={`bridge-${bridge.from}-${bridge.to}`}
            x1={x(from.index)}
            y1={y(from.top)}
            x2={x(to.index)}
            y2={y(to.top)}
            stroke={color}
            strokeWidth={1.5}
            strokeDasharray="3 4"
            opacity={0.55}
          />
        );
      })}
    </>
  );
}

function splitReadable(points: RenderedPoint[]): RenderedPoint[][] {
  const output: RenderedPoint[][] = [];
  let current: RenderedPoint[] = [];
  for (const point of points) {
    if (point.top === null) {
      if (current.length) output.push(current);
      current = [];
    } else {
      current.push(point);
    }
  }
  if (current.length) output.push(current);
  return output;
}

function TimeSeriesTable({
  buckets,
  series,
  rendered,
  format,
  isPercent,
  unit,
}: {
  buckets: TimeBucket[];
  series: ChartSeries[];
  rendered: Map<string, RenderedPoint[]>;
  format: (value: number) => string;
  isPercent: boolean;
  unit?: string;
}) {
  if (!buckets.length) return null;
  return (
    <ChartTable
      caption="时间序列数据表"
      head={
        <tr>
          <th className="whitespace-nowrap px-2 py-1.5 font-medium">时间</th>
          {series.map((item) => (
            <th key={item.id} className="whitespace-nowrap px-2 py-1.5 text-right font-medium">
              {item.label}{unit ? `（${unit}）` : ""}
            </th>
          ))}
        </tr>
      }
    >
      {buckets.map((bucket, index) => (
        <tr key={`${bucket.startedAt}-${index}`} className="border-t border-rule">
          <td className="whitespace-nowrap px-2 py-1 text-ink-3">{formatClockSeconds(bucket.startedAt)}</td>
          {series.map((item) => {
            const point = rendered.get(item.id)?.[index];
            const value = point?.value;
            return (
              <td key={item.id} className="px-2 py-1 text-right tabular-nums">
                {value === null || value === undefined
                  ? "—"
                  : isPercent
                    ? formatPercentValue(value)
                    : format(value)}
              </td>
            );
          })}
        </tr>
      ))}
    </ChartTable>
  );
}
