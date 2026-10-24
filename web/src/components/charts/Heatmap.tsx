// 热力图：周 × 小时，或日历日。
//
// 顺序蓝阶六档（--seq-1..6），0 单独用 --sunken；窗口未覆盖的格子用 45° 细斜纹
// 区分「没有采样」与「采样到 0」。所有色阶计算复用 chart-math 的 beijingSlot /
// beijingDate / heatmapLevel，避免浏览器时区把矩阵平移。

import { useMemo, useState, type ReactNode } from "react";
import {
  beijingDate,
  beijingSlot,
  formatCompactNumber,
  heatmapLevel,
} from "@/lib/chart-math";
import { ChartFrame } from "./ChartFrame";
import { ChartTooltip } from "./ChartTooltip";
import { ChartTable, CHART_EMPTY_TEXT } from "./chart-utils";
import { useChartSize } from "./useChartSize";

export interface HeatmapPoint {
  started_at: string;
  complete?: boolean;
  requests?: number;
  total_tokens?: number;
  [key: string]: unknown;
}

export interface HeatmapDay {
  date: string;
  value: number;
  buckets?: number;
  partial?: boolean;
  covered?: boolean;
}

export interface HeatmapProps {
  mode: "week" | "calendar";
  points?: HeatmapPoint[];
  days?: HeatmapDay[];
  value?: (point: HeatmapPoint) => number;
  format?: (value: number) => string;
  from?: string | number | Date;
  to?: string | number | Date;
  title?: string;
  ariaLabel?: string;
  height?: number;
  unit?: string;
  busy?: boolean;
  error?: ReactNode;
  emptyText?: string;
  /** 嵌入 Panel 内容区时由 ChartFrame 去掉外框、内边距与标题。 */
  embedded?: boolean;
  className?: string;
}

type WeekCell = { value: number; buckets: number; partial: boolean; covered: boolean };
type CalendarCell = HeatmapDay & { covered: boolean };

const WEEKDAY_LABELS = ["周一", "周二", "周三", "周四", "周五", "周六", "周日"];
const HOURS = Array.from({ length: 24 }, (_, hour) => hour);
const LEVEL_COLORS = ["var(--seq-1)", "var(--seq-2)", "var(--seq-3)", "var(--seq-4)", "var(--seq-5)", "var(--seq-6)"];

function toDate(value: string | number | Date | undefined): Date | null {
  if (value === undefined || value === null || value === "") return null;
  const date = value instanceof Date ? value : new Date(value);
  return Number.isNaN(date.getTime()) ? null : date;
}

function dayKey(date: Date): string {
  return beijingDate(date) ?? "";
}

function keyToUTC(key: string): Date | null {
  const [year, month, day] = key.split("-").map(Number);
  if (![year, month, day].every(Number.isFinite)) return null;
  const date = new Date(Date.UTC(year, month - 1, day));
  return Number.isNaN(date.getTime()) ? null : date;
}

function addDays(key: string, amount: number): string {
  const date = keyToUTC(key);
  if (!date) return key;
  date.setUTCDate(date.getUTCDate() + amount);
  return date.toISOString().slice(0, 10);
}

function weekdayIndex(key: string): number {
  const date = keyToUTC(key);
  return date ? (date.getUTCDay() + 6) % 7 : 0;
}

function coveredWeekCells(points: HeatmapPoint[], from: Date | null, to: Date | null): boolean[][] {
  const covered = Array.from({ length: 7 }, () => Array.from({ length: 24 }, () => false));
  if (!from || !to || to.getTime() <= from.getTime()) {
    // 没有窗口信息时，只把有数据的格子视为已覆盖；这比谎报整周覆盖安全。
    for (const point of points) {
      const slot = beijingSlot(point.started_at);
      if (slot) covered[slot.weekday][Math.floor(slot.slot / 2)] = true;
    }
    return covered;
  }
  // 半小时步进覆盖整个窗口；上限一年，避免异常输入造成长循环。
  const step = 30 * 60 * 1000;
  const end = Math.min(to.getTime(), from.getTime() + 366 * 24 * 60 * 60 * 1000);
  for (let current = from.getTime(); current <= end; current += step) {
    const slot = beijingSlot(new Date(current));
    if (slot) covered[slot.weekday][Math.floor(slot.slot / 2)] = true;
  }
  return covered;
}

export function Heatmap({
  mode,
  points = [],
  days,
  value = (point) => Number(point.requests ?? 0),
  format,
  from,
  to,
  title,
  ariaLabel = mode === "week" ? "周 × 小时热力图" : "日历热力图",
  unit,
  busy = false,
  error,
  emptyText = CHART_EMPTY_TEXT,
  embedded = false,
  className,
}: HeatmapProps) {
  const { ref, width } = useChartSize();
  const [hover, setHover] = useState<{ x: number; y: number; title: string; value: string; note?: string } | null>(null);
  // 自定义 format 往往已经带单位（如「9 次」）；只有格式化结果确实没有单位时才补，
  // 避免格子 aria/tooltip 出现「9 次 次」。
  const formatValue = format ?? ((number: number) => formatCompactNumber(number));
  const formatCell = (number: number) => {
    const text = formatValue(number);
    return unit && !text.includes(unit) ? `${text} ${unit}` : text;
  };
  const fromDate = toDate(from);
  const toDateValue = toDate(to);

  const week = useMemo(() => {
    if (mode !== "week") return null;
    const cells: WeekCell[][] = Array.from({ length: 7 }, () =>
      Array.from({ length: 24 }, () => ({ value: 0, buckets: 0, partial: false, covered: false })));
    for (const point of points) {
      const slot = beijingSlot(point.started_at);
      if (!slot) continue;
      const cell = cells[slot.weekday][Math.floor(slot.slot / 2)];
      cell.value += value(point);
      cell.buckets += 1;
      if (point.complete === false) cell.partial = true;
    }
    const coverage = coveredWeekCells(points, fromDate, toDateValue);
    for (let row = 0; row < 7; row += 1) {
      for (let hour = 0; hour < 24; hour += 1) cells[row][hour].covered = coverage[row][hour];
    }
    return { cells, max: Math.max(0, ...cells.flat().map((cell) => cell.value)) };
  }, [mode, points, fromDate, toDateValue, value]);

  const calendar = useMemo(() => {
    if (mode !== "calendar") return null;
    const byDate = new Map<string, CalendarCell>();
    if (days?.length) {
      for (const day of days) {
        byDate.set(day.date, { ...day, covered: day.covered ?? true });
      }
    } else {
      for (const point of points) {
        const key = beijingDate(point.started_at);
        if (!key) continue;
        const current = byDate.get(key) ?? { date: key, value: 0, buckets: 0, partial: false, covered: true };
        current.value += value(point);
        current.buckets = (current.buckets ?? 0) + 1;
        current.partial = current.partial || point.complete === false;
        byDate.set(key, current);
      }
    }
    let keys = [...byDate.keys()].sort();
    if (fromDate && toDateValue) {
      const start = dayKey(fromDate);
      const end = dayKey(toDateValue);
      keys = [];
      for (let key = start; key <= end; key = addDays(key, 1)) keys.push(key);
      if (keys.length > 370) keys = keys.slice(-370);
    } else if (keys.length > 1) {
      const start = keys[0];
      const end = keys[keys.length - 1];
      const filled: string[] = [];
      for (let key = start; key <= end; key = addDays(key, 1)) filled.push(key);
      keys = filled;
    }
    if (!keys.length) return { weeks: [] as Array<Array<CalendarCell | null>>, max: 0 };

    // 补齐到周一起始、周日结束，空格画斜纹而不是直接消失。
    const firstWeekday = weekdayIndex(keys[0]);
    const gridKeys: Array<string | null> = [...Array.from({ length: firstWeekday }, () => null), ...keys];
    while (gridKeys.length % 7 !== 0) gridKeys.push(null);
    const weeks: Array<Array<CalendarCell | null>> = [];
    for (let index = 0; index < gridKeys.length; index += 7) {
      weeks.push(gridKeys.slice(index, index + 7).map((key) => {
        if (!key) return null;
        const existing = byDate.get(key);
        return existing ?? { date: key, value: 0, buckets: 0, partial: false, covered: true };
      }));
    }
    const max = Math.max(0, ...[...byDate.values()].map((cell) => cell.value));
    return { weeks, max };
  }, [mode, points, days, fromDate, toDateValue, value]);

  const empty = mode === "week" ? !points.length : !calendar?.weeks.length;

  function showTooltip(event: React.PointerEvent<HTMLElement>, cellTitle: string, cellValue: string, note?: string) {
    const parent = ref.current?.getBoundingClientRect();
    const rect = event.currentTarget.getBoundingClientRect();
    if (!parent) return;
    setHover({ x: rect.left - parent.left + rect.width / 2, y: rect.top - parent.top - 4, title: cellTitle, value: cellValue, note });
  }

  function cellStyle(cell: WeekCell | CalendarCell, levelMax: number): React.CSSProperties {
    if (!cell.covered) {
      return {
        backgroundImage: "repeating-linear-gradient(45deg, transparent 0 3px, var(--rule) 3px 4px)",
        backgroundColor: "var(--sunken)",
      };
    }
    // 累加中的格子加一圈警示描边：颜色深浅只说明量，不能说明桶是否闭合。
    const partial = cell.partial ? { outline: "1px solid var(--warn)", outlineOffset: "-1px" } : {};
    if (!cell.value || levelMax <= 0) return { backgroundColor: "var(--sunken)", boxShadow: "inset 0 0 0 1px var(--rule)", ...partial };
    const level = heatmapLevel(cell.value, levelMax, 6);
    return { backgroundColor: LEVEL_COLORS[Math.max(0, Math.min(5, level - 1))], boxShadow: "inset 0 0 0 1px var(--rule)", ...partial };
  }

  return (
    <ChartFrame
      className={className}
      ariaLabel={ariaLabel}
      title={title}
      busy={busy}
      error={error}
      empty={empty}
      emptyText={emptyText}
      legend={
        <div className="flex items-center gap-1.5 text-[11px] text-ink-3">
          <span>低</span>
          {LEVEL_COLORS.map((color) => <span key={color} aria-hidden className="size-2.5 rounded-[2px]" style={{ backgroundColor: color }} />)}
          <span>高</span>
          <span className="ml-2 inline-flex items-center gap-1"><span aria-hidden className="size-2.5 rounded-[2px] bg-sunken" />无数据</span>
          <span className="inline-flex items-center gap-1"><span aria-hidden className="size-2.5 rounded-[2px]" style={{ backgroundImage: "repeating-linear-gradient(45deg, transparent 0 2px, var(--rule) 2px 3px)", backgroundColor: "var(--sunken)" }} />窗口未覆盖</span>
        </div>
      }
      table={mode === "week"
        ? <WeekTable cells={week?.cells ?? []} format={formatValue} unit={unit} />
        : <CalendarTable days={calendar?.weeks.flat().filter((cell): cell is CalendarCell => Boolean(cell)) ?? []} format={formatValue} unit={unit} />}
      embedded={embedded}
    >
      <div ref={ref} className="relative">
        {mode === "week" && week ? (
          <div
            role="grid"
            aria-label={ariaLabel}
            className="grid gap-[2px]"
            style={{ gridTemplateColumns: "34px repeat(24, minmax(0, 1fr))" }}
          >
            <div role="row" className="contents">
              <span aria-hidden />
              {HOURS.map((hour) => (
                <span key={hour} role="columnheader" className="text-center text-[9px] text-ink-3">{hour % 3 === 0 ? `${String(hour).padStart(2, "0")}` : ""}</span>
              ))}
            </div>
            {week.cells.map((row, rowIndex) => (
              <FragmentRow key={WEEKDAY_LABELS[rowIndex]}>
                <span role="rowheader" className="flex items-center text-[10px] text-ink-3">{WEEKDAY_LABELS[rowIndex]}</span>
                {row.map((cell, hour) => {
                  const label = `${WEEKDAY_LABELS[rowIndex]} ${String(hour).padStart(2, "0")}:00-${String(hour + 1).padStart(2, "0")}:00`;
                  const text = formatCell(cell.value);
                  return (
                    <button
                      key={hour}
                      type="button"
                      role="gridcell"
                      aria-label={`${label}：${cell.covered ? text : "窗口未覆盖"}${cell.partial ? "（累加中）" : ""}`}
                      className="aspect-square min-h-[7px] w-full rounded-[2px] outline-offset-1 hover:outline hover:outline-1 hover:outline-ink-2"
                      style={cellStyle(cell, week.max)}
                      onPointerEnter={(event) => showTooltip(event, label, cell.covered ? text : "窗口未覆盖", cell.partial ? "当前桶累加中，读数偏低。" : undefined)}
                      onPointerLeave={() => setHover(null)}
                    />
                  );
                })}
              </FragmentRow>
            ))}
          </div>
        ) : null}

        {mode === "calendar" && calendar ? (
          <CalendarGrid
            weeks={calendar.weeks}
            max={calendar.max}
            width={width}
            ariaLabel={ariaLabel}
            format={formatCell}
            cellStyle={cellStyle}
            onShowTooltip={showTooltip}
            onHideTooltip={() => setHover(null)}
          />
        ) : null}

        {hover ? (
          <ChartTooltip
            open
            x={hover.x}
            y={hover.y}
            width={width}
            title={hover.title}
            rows={[{ label: "数值", value: hover.value }]}
            note={hover.note}
          />
        ) : null}
      </div>
    </ChartFrame>
  );
}

// 日历热力图：53 列也必须一屏放下。
//
// 宽屏用 14–18px 的固定方格并左对齐（不把格子拉成矩形）；容器窄到 10px 仍放不下
// 53 列时，退化为等分列宽，宁可格子小一点，也不产生横向滚动条。月份标在上方，
// 周一/三/五标在左侧；单元格本身仍用 heatmap 的同一套 covered/partial 口径。
function CalendarGrid({
  weeks,
  max,
  width,
  ariaLabel,
  format,
  cellStyle,
  onShowTooltip,
  onHideTooltip,
}: {
  weeks: Array<Array<CalendarCell | null>>;
  max: number;
  width: number;
  ariaLabel: string;
  format: (value: number) => string;
  cellStyle: (cell: WeekCell | CalendarCell, levelMax: number) => React.CSSProperties;
  onShowTooltip: (event: React.PointerEvent<HTMLElement>, title: string, value: string, note?: string) => void;
  onHideTooltip: () => void;
}) {
  const gap = 2;
  const count = Math.max(1, weeks.length);
  const labelWidth = 18;
  const available = Math.max(1, width - labelWidth - 8);
  // 一年 53 列：先尝试 18→12 之间的整数尺寸，实在放不下才退到 10px，
  // 再放不下就流式铺满；绝不靠横向滚动来容纳日历。
  let fixedSize = 0;
  for (let size = 18; size >= 12; size -= 1) {
    if (size * count + gap * (count - 1) <= available) {
      fixedSize = size;
      break;
    }
  }
  if (!fixedSize && 10 * count + gap * (count - 1) <= available) fixedSize = 10;
  const fluid = fixedSize === 0;
  const columns = fluid ? `repeat(${count}, minmax(0, 1fr))` : `repeat(${count}, ${fixedSize}px)`;
  const rows = fluid ? "repeat(7, minmax(0, 1fr))" : `repeat(7, ${fixedSize}px)`;
  const chartWidth = fluid ? "100%" : `${count * fixedSize + (count - 1) * gap}px`;

  const months = useMemo(() => {
    const output: Array<{ start: number; label: string }> = [];
    let previous = "";
    weeks.forEach((week, index) => {
      const first = week.find((cell): cell is CalendarCell => Boolean(cell));
      const month = first?.date.slice(0, 7) ?? previous;
      if (month && month !== previous) output.push({ start: index, label: `${Number(month.slice(5))}月` });
      if (month) previous = month;
    });
    return output;
  }, [weeks]);

  return (
    <div className="flex min-w-0 items-start gap-2">
      <div className="shrink-0" style={{ width: labelWidth }}>
        <div aria-hidden style={{ height: 16 }} />
        <div className="grid" style={{ gridTemplateRows: rows, gap, height: fluid ? 112 : undefined }}>
          {Array.from({ length: 7 }, (_, index) => (
            <span key={index} className="flex items-center text-[9px] leading-none text-ink-3">
              {index === 0 ? "一" : index === 2 ? "三" : index === 4 ? "五" : ""}
            </span>
          ))}
        </div>
      </div>
      <div className="min-w-0 flex-1 overflow-hidden">
        <div
          aria-hidden
          className="grid text-[9px] leading-none text-ink-3"
          style={{ gridTemplateColumns: columns, columnGap: gap, width: chartWidth, height: 16 }}
        >
          {months.map((month, index) => (
            <span
              key={`${month.start}-${month.label}`}
              className="truncate"
              style={{
                gridColumn: `${month.start + 1} / span ${Math.max(1, (months[index + 1]?.start ?? count) - month.start)}`,
              }}
            >
              {month.label}
            </span>
          ))}
        </div>
        <div
          role="grid"
          aria-label={ariaLabel}
          className="grid"
          style={{
            gridTemplateColumns: columns,
            gridTemplateRows: rows,
            gridAutoFlow: "column",
            columnGap: gap,
            rowGap: gap,
            width: chartWidth,
            ...(fluid ? { height: 112 } : {}),
          }}
        >
          {weeks.map((weekColumn, columnIndex) => (
            <div role="row" className="contents" key={`week-${columnIndex}`}>
              {weekColumn.map((cell, rowIndex) => {
                if (!cell) {
                  return (
                    <span
                      key={`empty-${columnIndex}-${rowIndex}`}
                      aria-hidden
                      className="rounded-[2px]"
                      style={{
                        backgroundColor: "var(--sunken)",
                        backgroundImage: "repeating-linear-gradient(45deg, transparent 0 3px, var(--rule) 3px 4px)",
                        ...(fluid ? {} : { width: fixedSize, height: fixedSize }),
                      }}
                    />
                  );
                }
                const label = cell.date;
                const text = format(cell.value);
                return (
                  <button
                    key={cell.date}
                    type="button"
                    role="gridcell"
                    aria-label={`${label}：${cell.covered ? text : "窗口未覆盖"}${cell.partial ? "（累加中）" : ""}`}
                    className={`min-h-0 rounded-[2px] outline-offset-1 hover:outline hover:outline-1 hover:outline-ink-2 ${fluid ? "aspect-square w-full" : ""}`}
                    style={{ ...cellStyle(cell, max), ...(fluid ? {} : { width: fixedSize, height: fixedSize }) }}
                    onPointerEnter={(event) => onShowTooltip(event, label, cell.covered ? text : "窗口未覆盖", cell.partial ? "当前桶累加中，读数偏低。" : undefined)}
                    onPointerLeave={onHideTooltip}
                  />
                );
              })}
            </div>
          ))}
        </div>
      </div>
    </div>
  );
}

// 用 Fragment 让周热力图的 grid 子元素保持扁平；单独包组件比 fragment 可读。
function FragmentRow({ children }: { children: React.ReactNode }) {
  return <div role="row" className="contents">{children}</div>;
}

function WeekTable({ cells, format, unit }: { cells: WeekCell[][]; format: (value: number) => string; unit?: string }) {
  if (!cells.length) return null;
  return (
    <ChartTable
      caption="周热力图数据表"
      head={
        <tr>
          <th className="px-2 py-1.5 font-medium">星期</th>
          <th className="px-2 py-1.5 text-right font-medium">合计{unit ? `（${unit}）` : ""}</th>
          <th className="px-2 py-1.5 text-right font-medium">有数据小时</th>
        </tr>
      }
    >
      {cells.map((row, index) => (
        <tr key={WEEKDAY_LABELS[index]} className="border-t border-rule">
          <td className="px-2 py-1 text-ink-2">{WEEKDAY_LABELS[index]}</td>
          <td className="px-2 py-1 text-right tabular-nums">{format(row.reduce((sum, cell) => sum + cell.value, 0))}</td>
          <td className="px-2 py-1 text-right tabular-nums text-ink-3">{row.filter((cell) => cell.buckets > 0).length} / 24</td>
        </tr>
      ))}
    </ChartTable>
  );
}

function CalendarTable({ days, format, unit }: { days: CalendarCell[]; format: (value: number) => string; unit?: string }) {
  return (
    <ChartTable
      caption="日历热力图数据表"
      head={
        <tr>
          <th className="px-2 py-1.5 font-medium">日期</th>
          <th className="px-2 py-1.5 text-right font-medium">值{unit ? `（${unit}）` : ""}</th>
        </tr>
      }
    >
      {days.map((day) => (
        <tr key={day.date} className="border-t border-rule">
          <td className="px-2 py-1 text-ink-2">{day.date}</td>
          <td className="px-2 py-1 text-right tabular-nums">{day.covered ? format(day.value) : "窗口未覆盖"}</td>
        </tr>
      ))}
    </ChartTable>
  );
}
