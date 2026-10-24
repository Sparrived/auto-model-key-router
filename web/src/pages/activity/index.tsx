// 用量统计：回答「用了多少、谁在用」。
//
// 与概览的分工不变：概览看现在，这里看过去。旧版把 Token 环形图、状态码四段条、
// 五张维度表堆在同一页；新版只保留一个主角「拆分趋势」，其余读数都围绕同一组筛选
// 与同一个窗口展开。

import { useEffect, useMemo } from "react";
import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { errorText, formatCount } from "@/lib/format";
import {
  bucketLabel,
  dailyUsage,
  formatCompactNumber,
  pickBucketSeconds,
  windowSums,
  MAX_HISTORY_HOURS,
} from "@/lib/chart-math";
import type { MetricSeriesPoint } from "@/lib/types";
import {
  FILTER_DIMENSIONS,
  rangeLabel,
  rangeToQuery,
  toApiFilters,
} from "@/lib/filters";
import { useMetricFilters } from "@/lib/hooks/useMetricFilters";
import { queryKeys } from "@/lib/query-keys";
import { setParams, useRoute } from "@/app/router";
import { Banner } from "@/components/ui/Banner";
import { FilterBar } from "@/components/filters/FilterBar";
import { BreakdownPanel } from "./BreakdownPanel";
import { KpiBand } from "./KpiBand";
import {
  CumulativePanel,
  DailyUsagePanel,
  PerformancePanel,
  RhythmPanel,
} from "./SecondaryPanels";
import {
  asBreakdownDimension,
  asBreakdownMetric,
  asBreakdownShape,
  defaultBreakdownDimension,
  type BreakdownDimension,
  type BreakdownMetric,
  type BreakdownShape,
} from "./helpers";

const DEFAULT_RANGE = "7d";
const POLL_MS = 30_000;

function seriesSpanHours(points: MetricSeriesPoint[]): number | null {
  if (points.length < 2) return null;
  const first = Date.parse(points[0]?.started_at ?? "");
  const lastPoint = points[points.length - 1];
  const last = Date.parse(lastPoint?.ended_at ?? lastPoint?.started_at ?? "");
  if (!Number.isFinite(first) || !Number.isFinite(last) || last < first) return null;
  return (last - first) / 3_600_000;
}

export default function ActivityPage() {
  const filters = useMetricFilters(DEFAULT_RANGE);
  const route = useRoute();
  const rangeQuery = rangeToQuery(filters.state.range);
  const bucketSeconds = pickBucketSeconds(rangeQuery.hours ?? MAX_HISTORY_HOURS);
  const filterArgs = toApiFilters(filters.state);

  const filteredOptions = {
    ...rangeQuery,
    bucketSeconds,
    limit: 0,
    ...filterArgs,
  };
  const filteredQuery = useQuery({
    queryKey: queryKeys.filteredMetrics(filteredOptions),
    queryFn: () => api.filteredMetrics(filteredOptions),
    placeholderData: keepPreviousData,
    refetchInterval: POLL_MS,
    refetchIntervalInBackground: false,
  });

  const data = filteredQuery.data;
  const points = data?.series.points ?? [];
  const seriesSums = useMemo(() => windowSums(points), [points]);
  const spanHours = seriesSpanHours(points);
  const allHistoryTruncated = Boolean(
    rangeQuery.allHistory
    && spanHours !== null
    && spanHours > MAX_HISTORY_HOURS,
  );
  // 常规窗口的整页读数与拆分表、图表共用序列和桶网格，避免在窗口首桶边界上
  // 互相打架；全部历史被服务端截断时，顶部仍以快照为准，由横幅说明口径差异。
  const totals = points.length && !allHistoryTruncated
    ? { ...seriesSums, cache_read_tokens: seriesSums.cached_tokens }
    : data?.snapshot.total;
  const windowHours = rangeQuery.hours ?? spanHours;
  const windowHint = allHistoryTruncated
    ? "全部历史（仅覆盖最近 1 年）"
    : rangeLabel(filters.state.range);
  const days = useMemo(() => dailyUsage(points), [points]);
  const windowDays = Math.max(1, Math.ceil((windowHours ?? 1) / 24));

  const rawBy = route.params.get("by");
  const rawMetric = route.params.get("metric");
  const rawShape = route.params.get("shape");
  const dimension: BreakdownDimension = asBreakdownDimension(rawBy) ?? defaultBreakdownDimension(filters.state);
  const metric: BreakdownMetric = asBreakdownMetric(rawMetric) ?? "requests";
  const shape: BreakdownShape = asBreakdownShape(rawShape) ?? "area";

  // 三个控件状态必须写进 URL：刷新、前进后退、从概览切回来都靠它恢复。
  useEffect(() => {
    if (rawBy === dimension && rawMetric === metric && rawShape === shape) return;
    setParams((params) => {
      params.set("by", dimension);
      params.set("metric", metric);
      params.set("shape", shape);
      return params;
    }, { replace: true });
  }, [rawBy, rawMetric, rawShape, dimension, metric, shape]);

  const breakdownOptions = {
    groupBy: dimension,
    rankBy: metric === "tokens" ? "tokens" as const : "requests" as const,
    limit: 8,
    ...rangeQuery,
    bucketSeconds,
    ...filterArgs,
  };
  const breakdownQuery = useQuery({
    queryKey: queryKeys.usageBreakdown(breakdownOptions),
    queryFn: () => api.usageBreakdown(breakdownOptions),
    placeholderData: keepPreviousData,
    refetchInterval: POLL_MS,
    refetchIntervalInBackground: false,
  });
  const breakdownData = breakdownQuery.data;

  const heatmapSpan = windowHours ?? 24;
  const rhythmMode: "week" | "calendar" = heatmapSpan <= 30 * 24 ? "week" : "calendar";
  const activeBucket = data?.series.bucket_seconds ?? bucketSeconds;
  // 7/30 天档的自动桶可能已经宽于 1 小时；热力格最好是小时。只有能在 500 点上限内
  // 取到 3600 秒桶时才单独请求，否则退回当前桶宽并在图下说明。
  const needsHourlyHeatmap = rhythmMode === "week"
    && typeof rangeQuery.hours === "number"
    && rangeQuery.hours <= 499
    && activeBucket > 3600;
  const heatmapOptions = {
    ...rangeQuery,
    bucketSeconds: 3600,
    ...filterArgs,
  };
  const heatmapQuery = useQuery({
    queryKey: queryKeys.filteredMetrics({ ...heatmapOptions, limit: 0 }),
    queryFn: () => api.filteredMetrics({ ...heatmapOptions, limit: 0 }),
    enabled: needsHourlyHeatmap,
    placeholderData: keepPreviousData,
    refetchInterval: POLL_MS,
    refetchIntervalInBackground: false,
  });
  const rhythmPoints = needsHourlyHeatmap && heatmapQuery.data
    ? heatmapQuery.data.series.points
    : points;
  const rhythmBucket = needsHourlyHeatmap && heatmapQuery.data
    ? heatmapQuery.data.series.bucket_seconds
    : activeBucket;

  function changeBreakdown(key: "by" | "metric" | "shape", value: string) {
    setParams((params) => {
      params.set(key, value);
      return params;
    }, { replace: true });
  }

  return (
    <div className="min-w-0 space-y-3">
      <style>{`
        @media (max-width: 767px) {
          .activity-breakdown-table thead tr > th:nth-child(3),
          .activity-breakdown-table tbody tr > td:nth-child(3),
          .activity-breakdown-table thead tr > th:nth-child(6),
          .activity-breakdown-table tbody tr > td:nth-child(6),
          .activity-breakdown-table thead tr > th:nth-child(7),
          .activity-breakdown-table tbody tr > td:nth-child(7) { display: none; }
        }
        @media (max-width: 479px) {
          .activity-breakdown-table thead tr > th:nth-child(2),
          .activity-breakdown-table tbody tr > td:nth-child(2),
          .activity-breakdown-table thead tr > th:nth-child(5),
          .activity-breakdown-table tbody tr > td:nth-child(5),
          .activity-breakdown-table thead tr > th:nth-child(8),
          .activity-breakdown-table tbody tr > td:nth-child(8) { display: none; }
        }
      `}</style>

      <FilterBar defaultRange={DEFAULT_RANGE} supported={FILTER_DIMENSIONS} />

      {allHistoryTruncated ? (
        <Banner tone="warn" title="全部历史仅覆盖最近 1 年">
          历史跨度超过 8760 小时；趋势与节律按 1 年上限展示，顶部读数仍以服务端返回的窗口为准。
        </Banner>
      ) : null}

      {filteredQuery.error ? (
        <Banner tone="critical" title="无法读取整页用量读数">
          {errorText(filteredQuery.error) ?? undefined}
        </Banner>
      ) : null}

      <KpiBand
        totals={totals}
        days={days}
        windowDays={windowDays}
        loading={filteredQuery.isPending || Boolean(filteredQuery.error)}
      />

      <BreakdownPanel
        data={breakdownData}
        dimension={dimension}
        metric={metric}
        shape={shape}
        onDimensionChange={(value) => changeBreakdown("by", value)}
        onMetricChange={(value) => changeBreakdown("metric", value)}
        onShapeChange={(value) => changeBreakdown("shape", value)}
        setDimension={filters.setDimension}
        busy={breakdownQuery.isFetching && !breakdownQuery.isPending}
        error={breakdownQuery.error ? errorText(breakdownQuery.error) : undefined}
        windowLabel={windowHint}
      />

      <div className="grid min-w-0 gap-3 xl:grid-cols-12">
        <div className="min-w-0 xl:col-span-6">
          <CumulativePanel
            points={points}
            totalRequests={totals?.requests}
            totalTokens={Number(totals?.total_tokens) || 0}
            busy={filteredQuery.isFetching && !filteredQuery.isPending}
            error={filteredQuery.error ? errorText(filteredQuery.error) : undefined}
            windowLabel={windowHint}
          />
        </div>
        <div className="min-w-0 xl:col-span-6">
          <RhythmPanel
            points={rhythmPoints}
            bucketSeconds={rhythmBucket}
            mode={rhythmMode}
            from={data?.window.from ?? points[0]?.started_at}
            to={data?.window.to ?? points[points.length - 1]?.ended_at}
            busy={filteredQuery.isFetching && !filteredQuery.isPending}
            error={heatmapQuery.error || filteredQuery.error ? errorText(heatmapQuery.error ?? filteredQuery.error) : undefined}
            windowLabel={windowHint}
          />
        </div>
      </div>

      <DailyUsagePanel
        points={points}
        hours={rangeQuery.hours ?? null}
        bucketSeconds={activeBucket}
        busy={filteredQuery.isFetching && !filteredQuery.isPending}
        error={filteredQuery.error ? errorText(filteredQuery.error) : undefined}
        windowLabel={windowHint}
      />

      <PerformancePanel
        points={points}
        busy={filteredQuery.isFetching && !filteredQuery.isPending}
        error={filteredQuery.error ? errorText(filteredQuery.error) : undefined}
        windowLabel={windowHint}
      />

      {data ? (
        <div className="flex flex-wrap items-center gap-x-3 gap-y-1 px-1 text-[11px] text-ink-3">
          <span>计数口径：{data.count_semantics === "upstream_attempt" ? "上游尝试" : data.count_semantics || "未知"}</span>
          <span>{formatCount(totals?.requests ?? 0)} 次请求</span>
          <span>{formatCompactNumber(Number(totals?.total_tokens) || 0)} Token</span>
          <span>桶宽 {bucketLabel(data.series.bucket_seconds)}</span>
        </div>
      ) : null}
    </div>
  );
}
