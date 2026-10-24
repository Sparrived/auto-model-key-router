// 渠道健康矩阵：一屏 49 列（12 小时 / 15 分钟桶）也不横向滚动。
//
// 布局用 minmax(0,1fr) 让每格随容器压缩；时间标签按可用宽度只取若干个整点日界。
// 每格保留 title 与 aria-label（来自 formatHealthTooltip 的逐行文本），悬停再给
// 一张结构化小卡，避免把整段 tooltip 塞进 title 里只能等系统默认提示。

import { useEffect, useMemo, useState, type ReactNode } from "react";
import {
  cellMetrics,
  formatHealthTooltip,
  healthBand,
  healthBandLabel,
  HEALTH_MODE_MAP,
  HEALTH_THRESHOLDS,
  MIN_HEALTH_SAMPLES,
  type HealthMode,
  type StatusCell,
} from "@/lib/status-math";
import { formatDateTime } from "@/lib/format";
import { formatCompactNumber, formatDurationValue, formatPercentValue, timeAxisTicks } from "@/lib/chart-math";
import { ChartFrame } from "./ChartFrame";
import { ChartTooltip } from "./ChartTooltip";
import { ChartTable } from "./chart-utils";
import { useChartSize } from "./useChartSize";

export type HealthMatrixCell = StatusCell;

export interface HealthMatrixColumn {
  startedAt: string;
  endedAt?: string;
  complete: boolean;
}

export interface HealthMatrixRow {
  id: string;
  label: ReactNode;
  sublabel?: ReactNode;
  icon?: ReactNode;
  /** 左侧行汇总，例如“成功率 98.4%”。 */
  summary?: ReactNode;
  /** 行健康分；提供后表格视图与图形使用同一口径。 */
  score?: number | null;
  cells: HealthMatrixCell[];
}

export interface HealthMatrixProps {
  columns: HealthMatrixColumn[];
  rows: HealthMatrixRow[];
  mode?: HealthMode;
  ariaLabel?: string;
  title?: string;
  subtitle?: ReactNode;
  actions?: ReactNode;
  busy?: boolean;
  error?: ReactNode;
  onRowClick?: (row: HealthMatrixRow) => void;
  emptyText?: string;
  /** 嵌入 Panel 内容区时由 ChartFrame 去掉外框、内边距与标题。 */
  embedded?: boolean;
  className?: string;
}

const STATUS_ORDER: Array<{ band: "good" | "warn" | "bad" | "unknown" | "no-traffic"; label: string }> = [
  { band: "good", label: `健康 ≥${HEALTH_THRESHOLDS.good}` },
  { band: "warn", label: `波动 ${HEALTH_THRESHOLDS.warn}–${HEALTH_THRESHOLDS.good - 1}` },
  { band: "bad", label: `异常 <${HEALTH_THRESHOLDS.warn}` },
  { band: "unknown", label: `样本不足 <${MIN_HEALTH_SAMPLES} 次` },
  { band: "no-traffic", label: "无流量" },
];

function bandStyle(band: ReturnType<typeof healthBand>): React.CSSProperties {
  // 健康格刻意压低饱和度：一屏几十个绿块时，饱和绿会盖过真正需要注意的黄与红。
  if (band === "good") return { backgroundColor: "color-mix(in srgb, var(--good) 55%, var(--panel))" };
  if (band === "warn") return { backgroundColor: "var(--warn)" };
  if (band === "bad") return { backgroundColor: "var(--critical)" };
  if (band === "unknown") {
    return {
      backgroundColor: "var(--sunken)",
      backgroundImage: "repeating-linear-gradient(45deg, transparent 0 5px, color-mix(in srgb, var(--rule-strong) 55%, transparent) 5px 6px)",
    };
  }
  return { backgroundColor: "var(--sunken)" };
}

function rowTitle(row: HealthMatrixRow): string {
  const label = typeof row.label === "string" ? row.label : row.id;
  const summary = typeof row.summary === "string" ? row.summary : "";
  return summary ? `${label}
${summary}` : label;
}

function tooltipLines(cell: StatusCell | undefined, mode: HealthMode): string[] {
  return formatHealthTooltip(cell, mode).split("\n");
}

export function HealthMatrix({
  columns,
  rows,
  mode = "overall",
  ariaLabel = "渠道健康矩阵",
  title,
  subtitle,
  actions,
  busy = false,
  error,
  onRowClick,
  emptyText = "当前时间窗口内没有渠道健康数据",
  embedded = false,
  className,
}: HealthMatrixProps) {
  const { ref, setRef, width } = useChartSize();
  const [compact, setCompact] = useState(() => typeof window !== "undefined" && typeof window.matchMedia === "function" && window.matchMedia("(max-width: 639px)").matches);
  const [hover, setHover] = useState<{ x: number; y: number; label: string; lines: string[] } | null>(null);

  // 49 列矩阵在手机上即使每格 1px 也读不出来；默认切到同一份数据的表格视图，
  // 用户仍可通过右上角按钮切回图形。
  useEffect(() => {
    if (typeof window === "undefined" || typeof window.matchMedia !== "function") return;
    const media = window.matchMedia("(max-width: 639px)");
    const update = () => setCompact(media.matches);
    update();
    media.addEventListener?.("change", update);
    return () => media.removeEventListener?.("change", update);
  }, []);
  const labels = useMemo(() => {
    if (!columns.length) return [] as Array<{ index: number; span: number; text: string }>;
    // 健康矩阵的列就是时间桶：用同一套日历边界取表头，粗桶时退化为按列抽稀。
    const plotWidth = Math.max(1, width - 200);
    const columnWidth = plotWidth / columns.length;
    const ticks = timeAxisTicks(
      columns.map((column) => ({ started_at: column.startedAt })),
      {
        plotWidth,
        mode: "band",
        maxTicks: Math.max(2, width < 560 ? 4 : 7),
        x: (point) => point.index * columnWidth,
      },
    );
    return ticks.map((tick, index) => {
      const next = ticks[index + 1];
      return {
        index: tick.index,
        span: Math.max(1, (next?.index ?? columns.length) - tick.index),
        text: tick.label,
      };
    });
  }, [columns, width]);

  function showTooltip(event: React.PointerEvent<HTMLElement>, row: HealthMatrixRow, columnIndex: number) {
    const parent = ref.current?.getBoundingClientRect();
    const rect = event.currentTarget.getBoundingClientRect();
    if (!parent) return;
    const cell = row.cells[columnIndex];
    setHover({
      x: rect.left - parent.left + rect.width / 2,
      y: rect.top - parent.top - 4,
      label: `${typeof row.label === "string" ? row.label : row.id} · ${formatDateTime(columns[columnIndex]?.startedAt)}`,
      lines: tooltipLines(cell, mode),
    });
  }

  const gridColumns = `clamp(180px, 22%, 220px) repeat(${Math.max(1, columns.length)}, minmax(0, 1fr))`;

  return (
    <ChartFrame
      className={className}
      ariaLabel={ariaLabel}
      title={title ?? ariaLabel}
      subtitle={subtitle}
      actions={actions}
      busy={busy}
      error={error}
      empty={!rows.length || !columns.length}
      emptyText={emptyText}
      legend={
        <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-[11px] text-ink-3">
          {STATUS_ORDER.map((item) => (
            <span key={item.band} className="inline-flex items-center gap-1">
              <span aria-hidden className="size-2.5 rounded-[2px]" style={bandStyle(item.band)} />
              {item.label}
            </span>
          ))}
          <span className="ml-auto">着色口径：{HEALTH_MODE_MAP[mode].label}</span>
        </div>
      }
      table={<MatrixTable columns={columns} rows={rows} mode={mode} onRowClick={onRowClick} />}
      defaultView={compact ? "table" : "chart"}
      embedded={embedded}
    >
      <div ref={setRef} className="relative min-w-0 overflow-hidden">
        <div role="grid" aria-label={ariaLabel} className="min-w-0 gap-[2px]" style={{ display: "grid", gridTemplateColumns: gridColumns }}>
          <div role="columnheader" className="pb-1 text-[10px] text-ink-3">{rows.length} 个渠道</div>
          {labels[0]?.index > 0 ? (
            <div aria-hidden className="pb-1" style={{ gridColumn: `span ${labels[0].index}` }} />
          ) : null}
          {labels.map((label) => (
            <div
              key={`label-${label.index}`}
              role="columnheader"
              className="pb-1 pl-0.5 text-[9px] leading-none text-ink-3 tabular-nums"
              style={{ gridColumn: `span ${label.span}` }}
            >
              {label.text}
            </div>
          ))}

          {rows.map((row) => (
            <div role="row" key={row.id} className="contents">
              <div
                role="rowheader"
                onClick={onRowClick ? () => onRowClick(row) : undefined}
                className={`flex min-w-0 items-center gap-1.5 py-1 pr-1.5 ${onRowClick ? "cursor-pointer hover:text-ink" : ""}`}
                title={rowTitle(row)}
              >
                {row.icon ? <span aria-hidden className="shrink-0 text-ink-3 [&_svg]:size-3.5">{row.icon}</span> : null}
                <span className="min-w-0 flex-1 truncate text-[11px] text-ink-2">{row.label}</span>
                {row.summary ? <span className="shrink-0 text-right text-[10px] tabular-nums text-ink-3">{row.summary}</span> : null}
              </div>
              {columns.map((column, columnIndex) => {
                const cell = row.cells[columnIndex];
                const band = healthBand(cell, mode);
                const titleText = formatHealthTooltip(cell, mode);
                return (
                  <button
                    key={`${row.id}-${columnIndex}`}
                    type="button"
                    role="gridcell"
                    aria-label={`${typeof row.label === "string" ? row.label : row.id} ${formatDateTime(column.startedAt)}：${titleText.replace(/\n/g, "，")}`}
                    title={titleText}
                    className="min-h-[18px] min-w-0 rounded-[3px] outline-offset-0 hover:outline hover:outline-1 hover:outline-ink-2 focus-visible:z-[1] focus-visible:outline-2 focus-visible:outline-focus"
                    style={bandStyle(band)}
                    onPointerEnter={(event) => showTooltip(event, row, columnIndex)}
                    onPointerLeave={() => setHover(null)}
                    onFocus={(event) => {
                      const parent = ref.current?.getBoundingClientRect();
                      const rect = event.currentTarget.getBoundingClientRect();
                      if (!parent) return;
                      setHover({
                        x: rect.left - parent.left + rect.width / 2,
                        y: rect.top - parent.top - 4,
                        label: `${typeof row.label === "string" ? row.label : row.id} · ${formatDateTime(column.startedAt)}`,
                        lines: tooltipLines(cell, mode),
                      });
                    }}
                    onBlur={() => setHover(null)}
                  />
                );
              })}
            </div>
          ))}
        </div>

        {hover ? (
          <ChartTooltip open x={hover.x} y={hover.y} width={width} title={hover.label}>
            <div className="space-y-0.5 text-[11px] leading-relaxed">
              {hover.lines.map((line, index) => (
                <div key={index} className={index === 0 ? "text-ink-3" : "text-ink-2"}>{line}</div>
              ))}
            </div>
          </ChartTooltip>
        ) : null}
      </div>
    </ChartFrame>
  );
}

function MatrixTable({
  columns,
  rows,
  mode,
  onRowClick,
}: {
  columns: HealthMatrixColumn[];
  rows: HealthMatrixRow[];
  mode: HealthMode;
  onRowClick?: (row: HealthMatrixRow) => void;
}) {
  return (
    <ChartTable
      caption="健康矩阵数据表"
      head={
        <tr>
          <th className="whitespace-nowrap px-2 py-1.5 font-medium">渠道</th>
          <th className="whitespace-nowrap px-2 py-1.5 text-right font-medium">请求</th>
          <th className="whitespace-nowrap px-2 py-1.5 text-right font-medium">成功率</th>
          <th className="whitespace-nowrap px-2 py-1.5 text-right font-medium">平均首字</th>
          <th className="whitespace-nowrap px-2 py-1.5 text-right font-medium">健康档</th>
          <th className="whitespace-nowrap px-2 py-1.5 text-right font-medium">时段数</th>
        </tr>
      }
    >
      {rows.map((row) => {
        const total = row.cells.reduce<StatusCell>((acc, cell) => {
          const next = cellMetrics(cell);
          return {
            requests: (acc.requests ?? 0) + next.requests,
            successes: (acc.successes ?? 0) + next.successes,
            failures: (acc.failures ?? 0) + next.failures,
            total_first_token_ms: (acc.total_first_token_ms ?? 0) + next.totalFirstTokenMS,
            prompt_tokens: (acc.prompt_tokens ?? 0) + next.promptTokens,
            cached_tokens: (acc.cached_tokens ?? 0) + next.cachedTokens,
          };
        }, {});
        const totalMetrics = cellMetrics(total);
        return (
          <tr
            key={row.id}
            className={onRowClick ? "cursor-pointer border-t border-rule hover:bg-hover" : "border-t border-rule"}
            tabIndex={onRowClick ? 0 : undefined}
            onClick={onRowClick ? () => onRowClick(row) : undefined}
            onKeyDown={onRowClick ? (event) => {
              if (event.key === "Enter" || event.key === " ") {
                event.preventDefault();
                onRowClick(row);
              }
            } : undefined}
          >
            <td className="whitespace-nowrap px-2 py-1 text-ink-2">{row.label}</td>
            <td className="px-2 py-1 text-right tabular-nums">{formatCompactNumber(totalMetrics.requests)}</td>
            <td className="px-2 py-1 text-right tabular-nums">{totalMetrics.successRate === null ? "—" : formatPercentValue(totalMetrics.successRate)}</td>
            <td className="px-2 py-1 text-right tabular-nums">{totalMetrics.hasFirstToken ? formatDurationValue(totalMetrics.avgTtft ?? 0) : "—"}</td>
            <td className="px-2 py-1 text-right">
              {healthBandLabel(
                row.score == null
                  ? healthBand(total, mode)
                  : row.score >= HEALTH_THRESHOLDS.good
                    ? "good"
                    : row.score >= HEALTH_THRESHOLDS.warn
                      ? "warn"
                      : "bad",
              )}
            </td>
            <td className="whitespace-nowrap px-2 py-1 text-right tabular-nums text-ink-3">{row.cells.length}/{columns.length}</td>
          </tr>
        );
      })}
    </ChartTable>
  );
}
