// 拆分趋势：用量页唯一的主角。
//
// 旧版用五张表 + 三张 barList 分别回答「哪个模型 / 哪把 Key / 谁在调用」，同一组
// 事实被拆到不同角落，筛选时还会整块消失。这里收成一张可变维度的堆叠图 + 一张可
// 排序明细：图回答形状，表回答数字，二者共用同一组筛选与桶网格。

import type { ReactNode } from "react";
import { useSelectFilter } from "@/lib/hooks/useMetricFilters";
import type { FilterDimension, UpstreamKeyFilter } from "@/lib/filters";
import type { UsageBreakdownResponse } from "@/lib/types";
import { formatCompactNumber, formatPercentValue } from "@/lib/chart-math";
import { formatCount } from "@/lib/format";
import { useSeriesColors } from "@/lib/series-colors";
import { Badge } from "@/components/ui/Badge";
import { Button } from "@/components/ui/Button";
import { DataTable, type DataTableColumn } from "@/components/ui/DataTable";
import { Empty } from "@/components/ui/Empty";
import { Panel } from "@/components/ui/Panel";
import { Segmented } from "@/components/ui/Segmented";
import { Select } from "@/components/ui/Select";
import { Sparkline } from "@/components/charts/Sparkline";
import { StackedColumns } from "@/components/charts/StackedColumns";
import { TimeSeriesChart } from "@/components/charts/TimeSeriesChart";
import {
  ACTIVITY_BREAKDOWN_DIMENSIONS,
  ACTIVITY_BREAKDOWN_METRICS,
  ACTIVITY_BREAKDOWN_SHAPES,
  averageDuration,
  breakdownChartSeries,
  breakdownRows,
  breakdownTotalRequests,
  breakdownUnit,
  breakdownValueFormat,
  dimensionLabel,
  groupFilterTarget,
  groupMetricValues,
  metricLabel,
  numberField,
  statusTone,
  successRate,
  type BreakdownDimension,
  type BreakdownMetric,
  type BreakdownRow,
  type BreakdownShape,
} from "./helpers";

export interface BreakdownPanelProps {
  data: UsageBreakdownResponse | undefined;
  dimension: BreakdownDimension;
  metric: BreakdownMetric;
  shape: BreakdownShape;
  onDimensionChange: (value: BreakdownDimension) => void;
  onMetricChange: (value: BreakdownMetric) => void;
  onShapeChange: (value: BreakdownShape) => void;
  setDimension: (dimension: FilterDimension, values: string[] | UpstreamKeyFilter[] | null) => void;
  busy: boolean;
  error: ReactNode;
  windowLabel: string;
}

export function BreakdownPanel({
  data,
  dimension,
  metric,
  shape,
  onDimensionChange,
  onMetricChange,
  onShapeChange,
  setDimension,
  busy,
  error,
  windowLabel,
}: BreakdownPanelProps) {
  const rows = breakdownRows(data, dimension);
  const colorOf = useSeriesColors(dimension, rows.filter((row) => !row.isOther).map((row) => row.colorKey));
  const chartSeries = breakdownChartSeries(data, dimension, metric, colorOf);
  const buckets = (data?.buckets ?? []).map((bucket) => ({
    startedAt: bucket.started_at,
    complete: bucket.complete,
  }));
  const totalRequests = breakdownTotalRequests(data);
  const format = breakdownValueFormat(metric);
  const unit = breakdownUnit(metric);
  const selectFilter = useSelectFilter();
  const selectable = rows.some((row) => groupFilterTarget(dimension, row.group.key));

  function applyRowFilter(row: BreakdownRow) {
    const target = groupFilterTarget(dimension, row.group.key);
    if (!target) return;
    if (target.dimension === "uk") {
      setDimension("uk", [target.value as UpstreamKeyFilter]);
      return;
    }
    selectFilter(target.dimension, String(target.value));
  }

  const columns: DataTableColumn<BreakdownRow>[] = [
    {
      key: "name",
      header: "名称",
      cell: (row) => (
        <NameCell
          row={row}
          color={row.isOther ? "var(--series-other)" : colorOf(row.colorKey)}
          mobileDetail={totalRequests > 0
            ? `${formatCount(numberField(row.group.stats, "requests"))} 次 · ${formatPercentValue(numberField(row.group.stats, "requests") / totalRequests)}`
            : undefined}
          mobileAction={groupFilterTarget(dimension, row.group.key) ? () => applyRowFilter(row) : undefined}
        />
      ),
      sortValue: (row) => row.display.primary,
      width: "22%",
    },
    {
      key: "requests",
      header: "请求",
      align: "right",
      cell: (row) => <span className="tabular-nums">{formatCount(numberField(row.group.stats, "requests"))}</span>,
      sortValue: (row) => numberField(row.group.stats, "requests"),
      width: 74,
      tabular: true,
    },
    {
      key: "share",
      header: "占比",
      cell: (row) => {
        const share = totalRequests > 0 ? numberField(row.group.stats, "requests") / totalRequests : 0;
        return (
          <div className="flex min-w-[64px] items-center gap-1.5">
            <span className="h-1.5 min-w-0 flex-1 overflow-hidden rounded-full bg-sunken" aria-hidden>
              <span
                className="block h-full rounded-full"
                style={{ width: `${Math.min(100, share * 100)}%`, backgroundColor: row.isOther ? "var(--series-other)" : colorOf(row.colorKey) }}
              />
            </span>
            <span className="w-10 shrink-0 text-right text-[11px] tabular-nums text-ink-3">{formatPercentValue(share)}</span>
          </div>
        );
      },
      sortValue: (row) => (totalRequests > 0 ? numberField(row.group.stats, "requests") / totalRequests : 0),
      width: 104,
    },
    {
      key: "tokens",
      header: "Token",
      align: "right",
      cell: (row) => <span className="tabular-nums">{formatCompactNumber(numberField(row.group.stats, "total_tokens"))}</span>,
      sortValue: (row) => numberField(row.group.stats, "total_tokens"),
      width: 88,
      tabular: true,
    },
    {
      key: "success",
      header: "成功率",
      align: "right",
      cell: (row) => <span className="tabular-nums">{successRate(row.group.stats)}</span>,
      sortValue: (row) => {
        const requests = numberField(row.group.stats, "requests");
        return requests > 0 ? numberField(row.group.stats, "successes") / requests : null;
      },
      width: 74,
      tabular: true,
    },
    {
      key: "duration",
      header: "平均耗时",
      align: "right",
      cell: (row) => <span className="tabular-nums">{averageDuration(row.group.stats)}</span>,
      sortValue: (row) => {
        const requests = numberField(row.group.stats, "requests");
        return requests > 0 ? numberField(row.group.stats, "total_duration_ms") / requests : null;
      },
      width: 86,
      tabular: true,
    },
    {
      key: "spark",
      header: "趋势",
      cell: (row) => (
        <Sparkline
          values={groupMetricValues(row.group, metric)}
          color={row.isOther ? "var(--series-other)" : colorOf(row.colorKey)}
          height={26}
          ariaLabel={`${row.display.primary} ${metricLabel(metric)}趋势`}
          format={format}
          timestamps={data?.buckets.map((bucket) => bucket.started_at)}
        />
      ),
      width: 106,
    },
  ];

  const footer = data ? (
    <div className="flex flex-wrap items-center justify-between gap-2 text-meta text-ink-3">
      <span>
        合计 {formatCount(totalRequests)} 次；当前显示 {data.groups.length} 组，窗口共 {data.total_groups} 组。
      </span>
      <span>
        {selectable
          ? "「仅看此项」会把该值加入全局筛选；图表与表格始终跟随同一组筛选。"
          : `${dimensionLabel(dimension)}不是全局筛选维度，因此不提供「仅看此项」。`}
      </span>
    </div>
  ) : null;

  return (
    <Panel
      title="拆分趋势"
      description={`按${dimensionLabel(dimension)}把「用了多少、谁在用」拆开；前 8 组之外并入「其他」。`}
      busy={busy}
      padding={false}
      footer={footer}
      className="min-w-0 overflow-hidden"
      toolbar={(
        <div className="flex flex-wrap items-end gap-x-4 gap-y-2">
          <label className="flex min-w-0 flex-col gap-1 text-[11px] text-ink-3">
            拆分维度
            <Select
              aria-label="拆分维度"
              value={dimension}
              onChange={(event) => onDimensionChange(event.target.value as BreakdownDimension)}
              options={ACTIVITY_BREAKDOWN_DIMENSIONS.map((item) => ({ value: item.value, label: item.label }))}
              className="w-full sm:w-40"
            />
          </label>
          <div className="flex min-w-0 flex-col gap-1 text-[11px] text-ink-3">
            指标
            <Segmented
              aria-label="拆分指标"
              value={metric}
              onValueChange={(value) => onMetricChange(value as BreakdownMetric)}
              options={ACTIVITY_BREAKDOWN_METRICS.map((item) => ({ value: item.value, label: item.label }))}
              className="max-w-full flex-wrap"
            />
          </div>
          <div className="flex min-w-0 flex-col gap-1 text-[11px] text-ink-3">
            形态
            <Segmented
              aria-label="拆分形态"
              value={shape}
              onValueChange={(value) => onShapeChange(value as BreakdownShape)}
              options={ACTIVITY_BREAKDOWN_SHAPES.map((item) => ({ value: item.value, label: item.label }))}
              className="max-w-full flex-wrap"
            />
          </div>
          <Badge tone="neutral">{windowLabel}</Badge>
        </div>
      )}
    >
      <div className="px-4 pb-2 pt-3">
        {shape === "columns" ? (
          <StackedColumns
            buckets={buckets}
            series={chartSeries}
            format={format}
            height={290}
            ariaLabel={`按${dimensionLabel(dimension)}拆分的${metricLabel(metric)}趋势`}
            tableUnit={unit}
            busy={busy}
            error={data ? undefined : (error ?? undefined)}
            emptyText="当前筛选下没有可拆分的用量"
            embedded
          />
        ) : (
          <TimeSeriesChart
            buckets={buckets}
            series={chartSeries}
            format={format}
            height={290}
            variant={shape === "percent" ? "stacked-percent" : "stacked-area"}
            ariaLabel={`按${dimensionLabel(dimension)}拆分的${metricLabel(metric)}趋势`}
            tableUnit={unit}
            busy={busy}
            error={data ? undefined : (error ?? undefined)}
            emptyText="当前筛选下没有可拆分的用量"
            embedded
          />
        )}
      </div>
      <div className="border-t border-rule">
        <DataTable
          aria-label="拆分明细"
          className="activity-breakdown-table"
          columns={columns}
          rows={rows}
          rowKey={(row) => row.id}
          dense
          loading={busy && !data}
          loadingRows={6}
          emptyState={(
            <Empty
              title="当前筛选下没有拆分数据"
              description="换一个时间窗或筛选条件；没有流量的分组不会出现在表里。"
            />
          )}
          rowActions={(row) => {
            const target = groupFilterTarget(dimension, row.group.key);
            if (!target) return null;
            return (
              <Button
                size="sm"
                variant="ghost"
                onClick={() => applyRowFilter(row)}
                aria-label={`仅看此项：${row.display.primary}`}
              >
                仅看此项
              </Button>
            );
          }}
        />
      </div>
    </Panel>
  );
}

function NameCell({ row, color, mobileDetail, mobileAction }: {
  row: BreakdownRow;
  color: string;
  mobileDetail?: string;
  mobileAction?: () => void;
}) {
  return (
    <span className="flex min-w-0 items-center gap-2">
      <span aria-hidden className="size-2.5 shrink-0 rounded-[2px]" style={{ backgroundColor: color }} />
      <span className="min-w-0">
        <span className="flex min-w-0 items-center gap-1.5">
          {row.display.statusCode !== null ? (
            <Badge tone={statusTone(row.display.statusCode)}>{row.display.primary}</Badge>
          ) : (
            <span className="truncate font-medium text-ink" title={row.display.primary}>{row.display.primary}</span>
          )}
          {row.isOther && row.otherCount > 0 ? <span className="text-[11px] text-ink-3">共 {row.otherCount} 组</span> : null}
        </span>
        {row.display.secondary ? (
          <span className="block truncate font-mono text-[11px] text-ink-3" title={row.display.secondary}>
            {row.display.secondary}
          </span>
        ) : null}
        {mobileDetail ? (
          <span className="block text-[11px] tabular-nums text-ink-3 sm:hidden">{mobileDetail}</span>
        ) : null}
        {mobileAction ? (
          <Button size="sm" variant="ghost" className="mt-0.5 px-0 sm:hidden" onClick={mobileAction}>
            仅看此项
          </Button>
        ) : null}
      </span>
    </span>
  );
}
