// 状态监控：哪里出问题？
//
// 矩阵的窗口是固定的（最近 12 小时 / 15 分钟一格），与页头时间窗解耦；页头窗口
// 只服务于仪表带、趋势、错误原因与模型明细。维度切换会重取矩阵，着色口径切换只
// 改一个 prop，绝不重新请求——这两件事在界面上分属两个分段控件。

import { useMemo, useState } from "react";
import { useQuery, keepPreviousData } from "@tanstack/react-query";
import { Activity, CircleAlert, RefreshCw } from "lucide-react";
import { useMetricFilters } from "@/lib/hooks/useMetricFilters";
import { rangeLabel, rangeToQuery, toApiFilters, type UpstreamKeyFilter } from "@/lib/filters";
import { api } from "@/lib/api";
import { queryKeys } from "@/lib/query-keys";
import type {
  BreakdownGroup,
  FilteredMetricsResponse,
  MetricStats,
  RequestItem,
  StatusMatrixRow,
  StatusMonitorResponse,
  UsageBreakdownResponse,
} from "@/lib/types";
import {
  cellMetrics,
  errorReasons,
  GROUP_BY_MODES,
  healthScore,
  HEALTH_MODES,
  rowLabel,
  type HealthMode,
} from "@/lib/status-math";
import { formatCompactNumber, formatDurationValue, formatPercentValue, pickBucketSeconds } from "@/lib/chart-math";
import { formatClockSeconds, formatCompact, formatCount, formatDuration } from "@/lib/format";
import { useSeriesColors } from "@/lib/series-colors";
import { ProviderIcon } from "@/lib/brand-icons";
import { Badge } from "@/components/ui/Badge";
import { Banner } from "@/components/ui/Banner";
import { Button } from "@/components/ui/Button";
import { DataTable } from "@/components/ui/DataTable";
import { Empty } from "@/components/ui/Empty";
import { MetricCell, MetricStrip } from "@/components/ui/MetricStrip";
import { Panel } from "@/components/ui/Panel";
import { ProgressBar } from "@/components/ui/ProgressBar";
import { Segmented } from "@/components/ui/Segmented";
import { Sheet } from "@/components/ui/Sheet";
import { StatusDot } from "@/components/ui/StatusDot";
import { FilterBar } from "@/components/filters/FilterBar";
import { HealthMatrix, type HealthMatrixColumn, type HealthMatrixRow } from "@/components/charts/HealthMatrix";
import { Sparkline, TimeSeriesChart, type ChartSeries, type TimeBucket } from "@/components/charts";

const MATRIX_HOURS = 12;
const MATRIX_BUCKET_SECONDS = 15 * 60;

type GroupBy = "provider" | "provider_model" | "provider_key";
type TrendMode = "success" | "ttft" | "cache";

interface ChannelSelection {
  source: StatusMatrixRow;
  label: string;
  providerId: string | null;
  keyName: string | null;
  groupBy: GroupBy;
}

interface DetailFilters {
  providerIds?: string[];
  upstreamKeys?: UpstreamKeyFilter[];
}

function statsAvgTtft(stats: MetricStats | null | undefined): number | null {
  const requests = Number(stats?.requests || 0);
  if (!(requests > 0)) return null;
  const total = Number(stats?.total_first_token_ms || 0);
  if (total > 0) return total / requests;
  const avg = Number(stats?.avg_first_token_ms || 0);
  return avg > 0 ? avg : null;
}

function statsCacheRate(stats: MetricStats | null | undefined): number | null {
  const prompt = Number(stats?.prompt_tokens || 0);
  return prompt > 0 ? Number(stats?.cached_tokens || 0) / prompt : null;
}

function statsSuccessRate(stats: MetricStats | null | undefined): number | null {
  const requests = Number(stats?.requests || 0);
  return requests > 0 ? Number(stats?.successes || 0) / requests : null;
}

function windowRpm(data: StatusMonitorResponse | undefined, hours: number): number | null {
  if (!data) return null;
  const total = Number(data.snapshot?.total?.requests || 0);
  const windowHours = Number(data.window?.hours || hours);
  return windowHours > 0 ? total / (windowHours * 60) : null;
}

function breakdownName(key: BreakdownGroup["key"]): string {
  if (typeof key === "string") return key;
  return key.model_id || key.requested_model_id || key.upstream_model_id || key.provider_id || key.key_name || "—";
}

function rowKey(row: StatusMatrixRow): string {
  return `${row.provider_id || "∅"}|${row.upstream_model_id || "∅"}|${row.key_name || "∅"}`;
}

function failureCodesFromRequests(items: RequestItem[]): Record<string, number> {
  const codes: Record<string, number> = {};
  for (const item of items) {
    const key = item.status_code === null || item.status_code === undefined ? "none" : String(item.status_code);
    codes[key] = (codes[key] || 0) + 1;
  }
  return codes;
}

function detailFiltersOf(selection: ChannelSelection): DetailFilters {
  if (selection.groupBy === "provider_key") {
    if (!selection.providerId || !selection.keyName) return {};
    return { upstreamKeys: [{ providerId: selection.providerId, keyName: selection.keyName }] };
  }
  // filtered-metrics 没有“上游模型”筛选参数；provider_model 行只能收窄到供应商，
  // 这一点在抽屉里明说，避免用户把“供应商全部模型”误读成“这一行模型”。
  return selection.providerId ? { providerIds: [selection.providerId] } : {};
}

function ErrorReasonList({
  reasons,
  compact = false,
}: {
  reasons: ReturnType<typeof errorReasons>;
  compact?: boolean;
}) {
  if (!reasons.available) {
    return (
      <Empty
        icon={<Activity aria-hidden />}
        title="服务端尚未提供精确失败分组"
        description="不能从包含成功请求的状态码总量反推失败原因。"
      />
    );
  }
  if (!reasons.failures) {
    return (
      <Empty
        icon={<CircleAlert aria-hidden />}
        title="窗口内没有失败请求"
        description="按实际结果判定成功；HTTP 2xx 也可能因响应异常或下游取消而失败。"
      />
    );
  }
  return (
    <div className={compact ? "space-y-2.5" : "space-y-3"}>
      {reasons.rows.map((row) => {
        const tone = row.tone === "bad" ? "critical" : row.tone === "warn" ? "warn" : "neutral";
        return (
          <div key={row.id} title={row.hint} className="min-w-0">
            <div className="flex items-center justify-between gap-2 text-[12px]">
              <span className="inline-flex min-w-0 items-center gap-1.5">
                <StatusDot tone={tone} label="" aria-label={`${row.label} ${row.count} 次`} />
                <span className="truncate text-ink-2">{row.label}</span>
              </span>
              <span className="shrink-0 tabular-nums text-ink-3">
                <strong className="font-medium text-ink-2">{formatCount(row.count)}</strong>
                <span className="ml-1">· {row.ratio === null ? "—" : formatPercentValue(row.ratio)}</span>
              </span>
            </div>
            <ProgressBar
              className="mt-1"
              value={row.ratio === null ? 0 : row.ratio * 100}
              tone={tone === "critical" ? "critical" : tone === "warn" ? "warn" : "neutral"}
              aria-label={`${row.label} 占失败请求比例`}
            />
          </div>
        );
      })}
    </div>
  );
}

function StatusCodesFooter({ codes }: { codes: Record<string, number> | null | undefined }) {
  const entries = Object.entries(codes || {}).sort((a, b) => {
    if (a[0] === "none") return 1;
    if (b[0] === "none") return -1;
    return Number(a[0]) - Number(b[0]);
  });
  if (!entries.length) return <span>服务端未返回具体的失败状态码分布。</span>;
  return (
    <span>
      具体状态码：{entries.map(([code, count]) => `${code === "none" ? "无响应" : `HTTP ${code}`} ${formatCount(count)} 次`).join(" · ")}
    </span>
  );
}

function ChannelDetailSheet({
  selection,
  onClose,
  rangeQuery,
  apiFilters,
  hours,
  bucketSeconds,
  onOnly,
}: {
  selection: ChannelSelection;
  onClose: () => void;
  rangeQuery: ReturnType<typeof rangeToQuery>;
  apiFilters: ReturnType<typeof toApiFilters>;
  hours: number;
  bucketSeconds: number;
  onOnly: (selection: ChannelSelection) => void;
}) {
  const detailFilters = detailFiltersOf(selection);
  const trendOptions = { ...rangeQuery, bucketSeconds, limit: 0, ...apiFilters, ...detailFilters };
  // 失败原因需要覆盖全窗口；最近失败列表只截取前 20 条展示。这里一次取到
  // 服务端上限，避免“分子 20 条、分母全窗口”的统计口径错位。
  const failureOptions = { ...rangeQuery, bucketSeconds, limit: 200, ...apiFilters, ...detailFilters, success: false };

  const trendQuery = useQuery<FilteredMetricsResponse>({
    queryKey: queryKeys.filteredMetrics(trendOptions),
    queryFn: () => api.filteredMetrics(trendOptions),
    placeholderData: keepPreviousData,
    enabled: true,
  });
  const failureQuery = useQuery<FilteredMetricsResponse>({
    queryKey: queryKeys.filteredMetrics(failureOptions),
    queryFn: () => api.filteredMetrics(failureOptions),
    placeholderData: keepPreviousData,
    enabled: true,
  });

  const points = trendQuery.data?.series.points ?? [];
  const buckets: TimeBucket[] = points.map((point) => ({ startedAt: point.started_at, complete: point.complete }));
  const successSeries: ChartSeries[] = [{
    id: "success",
    label: "成功率",
    color: "var(--good)",
    values: points.map((point) => {
      const requests = Number(point.requests || 0);
      return requests > 0 ? Number(point.successes || 0) / requests : null;
    }),
  }];
  const ttftSeries: ChartSeries[] = [{
    id: "ttft",
    label: "平均首字",
    color: "var(--series-2)",
    values: points.map((point) => {
      const requests = Number(point.requests || 0);
      return requests > 0 && Number(point.total_first_token_ms || 0) > 0 ? Number(point.total_first_token_ms) / requests : null;
    }),
  }];
  const failureItems = [...(failureQuery.data?.requests?.items ?? [])].sort(
    (a, b) => Date.parse(b.created_at) - Date.parse(a.created_at),
  );
  const failures = failureItems.slice(0, 20);
  const failureSample = failureItems.length;
  const totalFailures = Number(failureQuery.data?.snapshot?.total?.failures ?? failureSample);
  const localReasons = errorReasons(
    { failures: failureSample },
    failureCodesFromRequests(failureItems),
  );
  const onlyLabel = "仅看此渠道";

  return (
    <Sheet
      open
      onOpenChange={(open) => { if (!open) onClose(); }}
      title="渠道详情"
      subtitle={`${selection.label} · ${rangeLabel(rangeQuery.allHistory ? "all" : `${hours}h`)}`}
      width="xl"
    >
      <div className="space-y-5">
        <section>
          <div className="mb-2 flex items-center justify-between gap-2">
            <h3 className="text-[12px] font-semibold text-ink">成功率趋势</h3>
            <Badge tone="neutral">{rangeLabel(rangeQuery.allHistory ? "all" : `${hours}h`)}</Badge>
          </div>
          <TimeSeriesChart
            buckets={buckets}
            series={successSeries}
            format={(value) => formatPercentValue(value)}
            percent
            referenceLines={[{ value: 0.95, label: "95%", color: "var(--warn)" }]}
            height={170}
            ariaLabel={`${selection.label} 成功率趋势`}
            emptyText="当前窗口内没有成功率读数"
          />
        </section>

        <section>
          <div className="mb-2 flex items-center justify-between gap-2">
            <h3 className="text-[12px] font-semibold text-ink">首字延迟趋势</h3>
            <Badge tone="neutral">按请求数加权</Badge>
          </div>
          <TimeSeriesChart
            buckets={buckets}
            series={ttftSeries}
            format={(value) => formatDurationValue(value)}
            height={140}
            ariaLabel={`${selection.label} 平均首字趋势`}
            tableUnit="ms"
            emptyText="当前窗口内没有首字读数"
          />
        </section>

        {selection.groupBy === "provider_model" ? (
          <Banner tone="warn" title="筛选范围说明">
            当前筛选接口不支持按“上游模型”收窄；这里的趋势是供应商整体，不是单行模型的独立读数。
          </Banner>
        ) : null}

        <section>
          <div className="mb-2 flex items-center justify-between gap-2">
            <h3 className="text-[12px] font-semibold text-ink">失败原因</h3>
            <Badge tone={totalFailures ? "critical" : "good"}>{totalFailures ? `${formatCount(totalFailures)} 次失败` : "无失败"}</Badge>
          </div>
          <ErrorReasonList reasons={localReasons} compact />
          {failureSample < totalFailures ? (
            <p className="mt-2 text-[11px] leading-5 text-ink-3">
              以上原因按最近 {formatCount(failureSample)} 条失败统计，覆盖全窗口 {formatCount(totalFailures)} 次失败中的样本。
            </p>
          ) : null}
        </section>

        <section>
          <div className="mb-2 flex items-center justify-between gap-2">
            <h3 className="text-[12px] font-semibold text-ink">最近失败</h3>
            <Badge tone="neutral">{failures.length} 条</Badge>
          </div>
          {failures.length ? (
            <div className="overflow-hidden rounded-control border border-rule">
              <div className="grid grid-cols-[76px_minmax(0,1fr)_64px_62px] gap-2 border-b border-rule bg-sunken px-3 py-1.5 text-[11px] text-ink-3">
                <span>时间</span><span>请求 / 渠道</span><span className="text-right">状态</span><span className="text-right">耗时</span>
              </div>
              {failures.map((item) => (
                <div key={item.id} className="grid grid-cols-[76px_minmax(0,1fr)_64px_62px] gap-2 border-t border-rule px-3 py-1.5 text-[11px] first:border-t-0">
                  <span className="tabular-nums text-ink-3">{formatClockSeconds(item.created_at)}</span>
                  <span className="min-w-0 truncate text-ink-2" title={[item.requested_model_id, item.provider_id, item.key_name].filter(Boolean).join(" / ")}>
                    {[item.requested_model_id || item.model_id, item.provider_id, item.key_name].filter(Boolean).join(" / ")}
                  </span>
                  <span className="text-right tabular-nums text-critical-ink">{item.status_code ?? "无响应"}</span>
                  <span className="text-right tabular-nums text-ink-3">{item.duration_ms === null || item.duration_ms === undefined ? "—" : formatDuration(Number(item.duration_ms))}</span>
                </div>
              ))}
            </div>
          ) : (
            <Empty title="最近没有失败请求" description="当前筛选与窗口内没有可展示的失败明细。" />
          )}
        </section>

        <div className="flex flex-wrap items-center justify-between gap-2 border-t border-rule pt-4">
          <p className="text-[11px] text-ink-3">
            {selection.groupBy === "provider_model"
              ? "“仅看此供应商”会写入供应商筛选；上游模型维度暂不写入 URL。"
              : "把当前渠道写入全局筛选，返回矩阵后继续查看其它面板。"}
          </p>
          <Button variant="primary" onClick={() => onOnly(selection)}>{onlyLabel}</Button>
        </div>
      </div>
    </Sheet>
  );
}

function StatusPage() {
  const { state, setDimension } = useMetricFilters("24h");
  const filters = toApiFilters(state);
  const rangeQuery = rangeToQuery(state.range);
  const hours = rangeQuery.hours ?? 8760;
  const bucketSeconds = pickBucketSeconds(hours);
  const [groupBy, setGroupBy] = useState<GroupBy>("provider");
  const [healthMode, setHealthMode] = useState<HealthMode>("overall");
  const [trendMode, setTrendMode] = useState<TrendMode>("success");
  const [selection, setSelection] = useState<ChannelSelection | null>(null);

  const pageOptions = { ...rangeQuery, groupBy, ...filters };
  const matrixOptions = { hours: MATRIX_HOURS, bucketSeconds: MATRIX_BUCKET_SECONDS, groupBy, ...filters };
  const modelOptions = { ...rangeQuery, groupBy: "model" as const, rankBy: "requests" as const, limit: 50, bucketSeconds, ...filters };

  const pageQuery = useQuery<StatusMonitorResponse>({
    queryKey: queryKeys.statusMonitor(pageOptions),
    queryFn: () => api.statusMonitor(pageOptions),
    placeholderData: keepPreviousData,
    refetchInterval: 15000,
    refetchIntervalInBackground: false,
  });
  const matrixQuery = useQuery<StatusMonitorResponse>({
    queryKey: queryKeys.statusMonitor(matrixOptions),
    queryFn: () => api.statusMonitor(matrixOptions),
    placeholderData: keepPreviousData,
    refetchInterval: 15000,
    refetchIntervalInBackground: false,
  });
  const modelQuery = useQuery<UsageBreakdownResponse>({
    queryKey: queryKeys.usageBreakdown(modelOptions),
    queryFn: () => api.usageBreakdown(modelOptions),
    placeholderData: keepPreviousData,
    refetchInterval: 15000,
    refetchIntervalInBackground: false,
  });
  const providersQuery = useQuery({
    queryKey: queryKeys.providers(),
    queryFn: api.providers,
    staleTime: 60000,
  });

  const providerBaseUrls = useMemo(() => {
    const map = new Map<string, string>();
    for (const provider of providersQuery.data?.providers ?? []) map.set(provider.id, provider.base_url);
    return map;
  }, [providersQuery.data]);

  const total = pageQuery.data?.snapshot?.total;
  const successRate = statsSuccessRate(total);
  const avgTtft = statsAvgTtft(total);
  const cacheRate = statsCacheRate(total);
  const rpm = windowRpm(pageQuery.data, hours);
  const failures = Number(total?.failures || 0);

  const matrixColumns: HealthMatrixColumn[] = useMemo(() => {
    const points = matrixQuery.data?.series.points ?? [];
    if (points.length) {
      return points.map((point) => ({ startedAt: point.started_at, endedAt: point.ended_at, complete: point.complete }));
    }
    const first = matrixQuery.data?.matrix.rows[0];
    return (first?.cells ?? []).map((cell) => ({
      startedAt: cell.started_at || "",
      endedAt: cell.ended_at,
      complete: cell.complete !== false,
    }));
  }, [matrixQuery.data]);

  const matrixRows = useMemo<Array<HealthMatrixRow & { source: StatusMatrixRow }>>(() => {
    return (matrixQuery.data?.matrix.rows ?? []).map((source) => {
      const aggregate = cellMetrics({
        requests: source.cells.reduce((sum, cell) => sum + Number(cell.requests || 0), 0),
        successes: source.cells.reduce((sum, cell) => sum + Number(cell.successes || 0), 0),
        failures: source.cells.reduce((sum, cell) => sum + Number(cell.failures || 0), 0),
        prompt_tokens: source.cells.reduce((sum, cell) => sum + Number(cell.prompt_tokens || 0), 0),
        cached_tokens: source.cells.reduce((sum, cell) => sum + Number(cell.cached_tokens || 0), 0),
        total_first_token_ms: source.cells.reduce((sum, cell) => sum + Number(cell.total_first_token_ms || 0), 0),
      });
      const score = healthScore(aggregate, "overall");
      const label = rowLabel(source, groupBy);
      const summary = aggregate.requests > 0
        ? `${formatCount(aggregate.requests)} 次 · ${aggregate.successRate === null ? "—" : formatPercentValue(aggregate.successRate)} · ${score === null ? "样本不足" : `健康分 ${score}`}`
        : "窗口内无请求";
      const providerId = source.provider_id || null;
      return {
        id: rowKey(source),
        label: label.label,
        summary,
        score,
        icon: providerId ? <ProviderIcon id={providerId} baseUrl={providerBaseUrls.get(providerId) || null} size={14} /> : undefined,
        cells: source.cells,
        source,
      };
    }).sort((a, b) => {
      // 健康分升序把最需要关注的渠道放在最上面；样本不足/无评分放最后。
      return (a.score ?? 101) - (b.score ?? 101)
        || String(a.label).localeCompare(String(b.label), "zh-CN");
    });
  }, [matrixQuery.data, groupBy, providerBaseUrls]);

  const trendPoints = pageQuery.data?.series.points ?? [];
  const trendBuckets: TimeBucket[] = trendPoints.map((point) => ({ startedAt: point.started_at, complete: point.complete }));
  const trendSeries: ChartSeries[] = useMemo(() => [{
    id: trendMode,
    label: trendMode === "success" ? "成功率" : trendMode === "ttft" ? "平均首字" : "缓存命中率",
    color: trendMode === "success" ? "var(--good)" : trendMode === "ttft" ? "var(--series-2)" : "var(--series-3)",
    values: trendPoints.map((point) => {
      const requests = Number(point.requests || 0);
      if (trendMode === "success") return requests > 0 ? Number(point.successes || 0) / requests : null;
      if (trendMode === "ttft") return requests > 0 && Number(point.total_first_token_ms || 0) > 0 ? Number(point.total_first_token_ms) / requests : null;
      const prompt = Number(point.prompt_tokens || 0);
      return prompt > 0 ? Number(point.cached_tokens || 0) / prompt : null;
    }),
  }], [trendPoints, trendMode]);

  const reasons = useMemo(
    () => pageQuery.data ? errorReasons(pageQuery.data.snapshot?.total, pageQuery.data.failure_status_codes) : errorReasons(undefined, null),
    [pageQuery.data],
  );

  const modelGroups = modelQuery.data?.groups ?? [];
  const modelBuckets = modelQuery.data?.buckets ?? [];
  const modelNames = useMemo(() => modelGroups.map((group) => breakdownName(group.key)), [modelGroups]);
  const modelColor = useSeriesColors("model", modelNames);

  function openRow(row: HealthMatrixRow) {
    const source = (row as HealthMatrixRow & { source?: StatusMatrixRow }).source;
    if (!source) return;
    const label = rowLabel(source, groupBy);
    setSelection({
      source,
      label: label.label,
      providerId: source.provider_id || null,
      keyName: source.key_name || null,
      groupBy,
    });
  }

  function onlySelection(item: ChannelSelection) {
    if (item.groupBy === "provider_key" && item.providerId && item.keyName) {
      setDimension("uk", [{ providerId: item.providerId, keyName: item.keyName }]);
    } else if (item.providerId) {
      setDimension("provider", [item.providerId]);
    }
    setSelection(null);
  }

  const groupByOptions = GROUP_BY_MODES.map((mode) => ({ value: mode.id, label: mode.label }));
  const healthOptions = HEALTH_MODES.map((mode) => ({ value: mode.id, label: mode.label }));

  return (
    <div className="mx-auto flex w-full max-w-[1560px] flex-col gap-4 pb-8">
      <FilterBar defaultRange="24h" />

      {pageQuery.isError ? (
        <Banner tone="critical" title="状态数据读取失败">{String((pageQuery.error as Error)?.message || pageQuery.error)}</Banner>
      ) : null}
      {matrixQuery.isError ? <Banner tone="warn">渠道健康矩阵读取失败：{String((matrixQuery.error as Error)?.message || matrixQuery.error)}</Banner> : null}

      <MetricStrip>
        <MetricCell
          label="成功率"
          value={successRate === null ? "—" : formatPercentValue(successRate)}
          tone={successRate === null ? undefined : successRate < 0.95 ? "critical" : successRate >= 0.99 ? "good" : undefined}
          hint={`${formatCount(total?.successes ?? 0)} 成功 · ${formatCount(total?.failures ?? 0)} 失败`}
        />
        <MetricCell label="失败次数" value={formatCount(failures)} tone={failures ? "critical" : "good"} hint={`窗口 ${rangeLabel(state.range)}`} />
        <MetricCell label="平均首字" value={avgTtft === null ? "—" : formatDurationValue(avgTtft)} hint="按请求数加权" />
        <MetricCell label="缓存命中率" value={cacheRate === null ? "—" : formatPercentValue(cacheRate)} tone={cacheRate !== null && cacheRate >= 0.3 ? "good" : undefined} hint={`缓存 ${formatCompact(total?.cached_tokens ?? 0)} Token`} />
        <MetricCell label="窗口平均 RPM" value={rpm === null ? "—" : formatCompactNumber(rpm)} unit="次/分" hint={rangeLabel(state.range)} />
      </MetricStrip>

      <HealthMatrix
        columns={matrixColumns}
        rows={matrixRows}
        mode={healthMode}
        ariaLabel="渠道健康矩阵"
        title="渠道健康矩阵"
        subtitle={`最近 ${MATRIX_HOURS} 小时 · 15 分钟一格`}
        actions={
          <div className="flex flex-wrap items-center justify-end gap-2">
            <Segmented
              options={groupByOptions}
              value={groupBy}
              onValueChange={(value) => setGroupBy(value as GroupBy)}
              aria-label="健康矩阵分组维度"
            />
            <Segmented
              options={healthOptions}
              value={healthMode}
              onValueChange={(value) => setHealthMode(value as HealthMode)}
              aria-label="健康矩阵着色口径"
            />
            <Button
              variant="ghost"
              size="sm"
              aria-label="刷新健康矩阵"
              leadingIcon={<RefreshCw aria-hidden className="size-3.5" />}
              onClick={() => { void matrixQuery.refetch(); }}
            >
              刷新
            </Button>
          </div>
        }
        busy={matrixQuery.isFetching}
        onRowClick={openRow}
      />

      <div className="grid min-w-0 grid-cols-12 gap-4">
        <Panel
          className="col-span-12 min-w-0 lg:col-span-7"
          title="健康趋势"
          description={`${rangeLabel(state.range)} · 尾桶未闭合时会用虚线标出`}
          actions={
            <Segmented
              options={[
                { value: "success", label: "成功率" },
                { value: "ttft", label: "首字" },
                { value: "cache", label: "缓存率" },
              ]}
              value={trendMode}
              onValueChange={(value) => setTrendMode(value as TrendMode)}
              aria-label="健康趋势指标"
            />
          }
          busy={pageQuery.isFetching}
        >
          <TimeSeriesChart
            buckets={trendBuckets}
            series={trendSeries}
            format={(value) => trendMode === "ttft" ? formatDurationValue(value) : formatPercentValue(value)}
            percent={trendMode !== "ttft"}
            referenceLines={trendMode === "success" ? [{ value: 0.95, label: "95%", color: "var(--warn)" }] : []}
            height={235}
            embedded
            ariaLabel={`健康趋势 · ${HEALTH_MODES.find((mode) => mode.id === healthMode)?.label || "综合"}`}
            tableUnit={trendMode === "ttft" ? "ms" : undefined}
            emptyText="当前窗口内没有可绘制的健康趋势"
          />
        </Panel>

        <Panel
          className="col-span-12 min-w-0 lg:col-span-5"
          title="失败原因"
          description={`${rangeLabel(state.range)} · 固定七档，不按次数重排`}
          actions={<Badge tone={reasons.failures ? "critical" : "good"}>{reasons.failures ? `${formatCount(reasons.failures)} 次失败` : "无失败"}</Badge>}
          busy={pageQuery.isFetching}
        >
          <ErrorReasonList reasons={reasons} />
          <div className="mt-3 border-t border-rule pt-2 text-[11px] leading-5 text-ink-3">
            <StatusCodesFooter codes={pageQuery.data?.failure_status_codes} />
            <div className="mt-1">失败合计 {formatCount(reasons.failures)} 次；其中 {formatCount(reasons.coded)} 次收到上游状态码，{formatCount(reasons.noStatus)} 次未收到。HTTP 状态与实际结果分别统计。</div>
          </div>
        </Panel>
      </div>

      <Panel
        title="模型明细"
        description={`${rangeLabel(state.range)} · 行内趋势来自 usage-breakdown`}
        actions={<Badge tone="neutral">{formatCount(modelGroups.length)} 个模型</Badge>}
        busy={modelQuery.isFetching}
        padding={false}
      >
        <DataTable
          aria-label="模型明细"
          caption="模型请求成功率、首字、耗时、缓存与 RPM"
          rows={modelGroups}
          rowKey={(group) => breakdownName(group.key)}
          dense
          emptyState={<Empty title="窗口内没有模型调用记录" description="调整时间窗或筛选后再看。" />}
          columns={[
            {
              key: "model",
              header: "模型",
              cell: (group) => {
                const name = breakdownName(group.key);
                return <span className="font-mono text-[12px] text-ink" title={name}>{name}</span>;
              },
              sortValue: (group) => breakdownName(group.key),
            },
            {
              key: "requests",
              header: "请求",
              align: "right",
              tabular: true,
              cell: (group) => formatCount(group.stats.requests),
              sortValue: (group) => Number(group.stats.requests || 0),
            },
            {
              key: "success",
              header: "成功率",
              align: "right",
              tabular: true,
              cell: (group) => statsSuccessRate(group.stats) === null ? "—" : formatPercentValue(statsSuccessRate(group.stats) as number),
              sortValue: (group) => statsSuccessRate(group.stats) ?? -1,
            },
            {
              key: "ttft",
              header: "平均首字",
              align: "right",
              tabular: true,
              cell: (group) => statsAvgTtft(group.stats) === null ? "—" : formatDurationValue(statsAvgTtft(group.stats) as number),
              sortValue: (group) => statsAvgTtft(group.stats) ?? -1,
            },
            {
              key: "latency",
              header: "平均耗时",
              align: "right",
              tabular: true,
              cell: (group) => formatDurationValue(Number(group.stats.avg_duration_ms || 0)),
              sortValue: (group) => Number(group.stats.avg_duration_ms || 0),
            },
            {
              key: "cache",
              header: "缓存率",
              align: "right",
              tabular: true,
              cell: (group) => statsCacheRate(group.stats) === null ? "—" : formatPercentValue(statsCacheRate(group.stats) as number),
              sortValue: (group) => statsCacheRate(group.stats) ?? -1,
            },
            {
              key: "rpm",
              header: "RPM",
              align: "right",
              tabular: true,
              cell: (group) => formatCompactNumber(Number(group.stats.requests || 0) / Math.max(1, hours * 60)),
              sortValue: (group) => Number(group.stats.requests || 0),
            },
            {
              key: "spark",
              header: "请求趋势",
              width: 150,
              cell: (group) => (
                <Sparkline
                  values={group.series.requests}
                  timestamps={modelBuckets.map((bucket) => bucket.started_at)}
                  color={modelColor(breakdownName(group.key))}
                  height={28}
                  ariaLabel={`${breakdownName(group.key)} 请求趋势`}
                  format={(value) => formatCompact(value)}
                />
              ),
            },
          ]}
          rowActions={(group) => (
            <Button variant="ghost" size="sm" onClick={() => setDimension("model", [breakdownName(group.key)])}>
              仅看此模型
            </Button>
          )}
        />
      </Panel>

      {selection ? (
        <ChannelDetailSheet
          selection={selection}
          onClose={() => setSelection(null)}
          rangeQuery={rangeQuery}
          apiFilters={filters}
          hours={hours}
          bucketSeconds={bucketSeconds}
          onOnly={onlySelection}
        />
      ) : null}
    </div>
  );
}


export default StatusPage;
