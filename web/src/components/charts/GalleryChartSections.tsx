// 陈列页的 T2 图表区：所有图都接真实 API，不用手写假数据。
//
// 这一文件故意放在 charts/ 下：它是组件库的“样板页”，不承担业务页面的状态与路由。

import { useQuery } from "@tanstack/react-query";
import { Activity, ArrowRight, Database, Gauge, Waypoints } from "lucide-react";
import { toast } from "sonner";
import { Panel } from "@/components/ui/Panel";
import { Skeleton } from "@/components/ui/Skeleton";
import { FilterBar } from "@/components/filters/FilterBar";
import { useMetricFilters, useSelectFilter } from "@/lib/hooks/useMetricFilters";
import { rangeLabel, toApiFilters, rangeToQuery } from "@/lib/filters";
import { api } from "@/lib/api";
import { queryKeys } from "@/lib/query-keys";
import { errorText, formatDateTime } from "@/lib/format";
import { formatCompactNumber, formatDurationValue, formatPercentValue } from "@/lib/chart-math";
import { rowLabel } from "@/lib/status-math";
import type { BreakdownGroup, BreakdownKey, StatusMatrixRow } from "@/lib/types";
import { HealthMatrix } from "./HealthMatrix";
import { Heatmap } from "./Heatmap";
import { Meter } from "./Meter";
import { RankBars } from "./RankBars";
import { ShareBar } from "./ShareBar";
import { SmallMultiples } from "./SmallMultiples";
import { Sparkline } from "./Sparkline";
import { StackedColumns } from "./StackedColumns";
import { TimeSeriesChart } from "./TimeSeriesChart";
import { FlowMap } from "./FlowMap";

function Section({ title, description, children }: { title: string; description?: string; children: React.ReactNode }) {
  return (
    <section className="space-y-2">
      <div>
        <h2 className="text-[15px] font-semibold text-ink">{title}</h2>
        {description ? <p className="mt-0.5 text-meta text-ink-3">{description}</p> : null}
      </div>
      {children}
    </section>
  );
}

export function GalleryFilterSection() {
  return (
    <Section title="筛选" description="时间窗 + 维度筛选走 URL；筛选条是监控页共用的一组控件。">
      <Panel>
        <FilterBar />
      </Panel>
    </Section>
  );
}

function breakdownName(key: BreakdownKey | null | undefined): string {
  // 拆分接口的 other 是没有 key 的聚合组；这里统一显示为「其他」。
  if (key === null || key === undefined) return "其他";
  if (typeof key === "string") return key;
  return String(
    key.model_id
    ?? key.requested_model_id
    ?? key.provider_id
    ?? key.access_key_name
    ?? key.access_key_id
    ?? key.workspace
    ?? key.caller_type
    ?? key.upstream_model_id
    ?? "未归属",
  );
}

function groupsOf(groups: BreakdownGroup[] | undefined, other: BreakdownGroup | null | undefined): BreakdownGroup[] {
  return [...(groups ?? []), ...(other ? [other] : [])];
}

function align(values: number[] | undefined, length: number): Array<number | null> {
  return Array.from({ length }, (_, index) => values?.[index] ?? null);
}

function cellTotal(rows: StatusMatrixRow[]): { requests: number; successes: number; failures: number } {
  return rows.reduce((sum, row) => {
    const requests = row.cells.reduce((value, cell) => value + (Number(cell.requests) || 0), 0);
    const successes = row.cells.reduce((value, cell) => value + (Number(cell.successes) || 0), 0);
    const failures = row.cells.reduce((value, cell) => value + (Number(cell.failures) || 0), 0);
    return { requests: sum.requests + requests, successes: sum.successes + successes, failures: sum.failures + failures };
  }, { requests: 0, successes: 0, failures: 0 });
}

export function GalleryChartSections() {
  const { state } = useMetricFilters();
  const select = useSelectFilter();
  const range = rangeToQuery(state.range);
  const apiFilters = toApiFilters(state);
  const rangeLabelText = rangeLabel(state.range);

  const breakdownOptions = {
    groupBy: "model" as const,
    rankBy: "requests" as const,
    limit: 8,
    bucketSeconds: 1800,
    ...range,
    ...apiFilters,
  };
  const breakdownQuery = useQuery({
    queryKey: queryKeys.usageBreakdown(breakdownOptions),
    queryFn: () => api.usageBreakdown(breakdownOptions),
  });
  const statusOptions = {
    groupBy: "provider_key" as const,
    bucketSeconds: 1800,
    hours: range.hours ?? 8760,
    ...apiFilters,
  };
  const statusQuery = useQuery({
    queryKey: queryKeys.statusMonitor(statusOptions),
    queryFn: () => api.statusMonitor(statusOptions),
  });
  const workspaceQuery = useQuery({
    queryKey: queryKeys.workspaceUsage({ hours: range.hours ?? 24, allHistory: range.allHistory }),
    queryFn: () => api.workspaceUsage({ hours: range.hours ?? 24, allHistory: range.allHistory }),
  });
  const weekQuery = useQuery({
    queryKey: queryKeys.series(168, 1800),
    queryFn: () => api.series(168, 1800),
  });
  const calendarQuery = useQuery({
    queryKey: queryKeys.series(720, 86400),
    queryFn: () => api.series(720, 86400),
  });

  const breakdown = breakdownQuery.data;
  const modelBuckets = (breakdown?.buckets ?? []).map((bucket) => ({ startedAt: bucket.started_at, complete: bucket.complete }));
  const modelGroups = groupsOf(breakdown?.groups, breakdown?.other);
  const modelSeries = modelGroups.map((group) => {
    const label = breakdownName(group.key);
    return {
      id: label,
      label,
      values: align(group.series.requests, modelBuckets.length),
    };
  });
  const rankItems = modelGroups
    .map((group) => {
      const label = breakdownName(group.key);
      return {
        id: label,
        name: label,
        label,
        value: Number(group.stats.requests) || 0,
        isOther: breakdown?.other === group,
      };
    })
    .sort((a, b) => b.value - a.value);
  const tokenItems = modelGroups
    .map((group) => ({ id: breakdownName(group.key), label: breakdownName(group.key), value: Number(group.stats.total_tokens) || 0 }))
    .sort((a, b) => b.value - a.value);
  const topGroup = modelGroups[0];
  const topSeries = topGroup ? align(topGroup.series.requests, modelBuckets.length) : [];

  const status = statusQuery.data;
  const statusBuckets = (status?.series.points ?? []).map((point) => ({ startedAt: point.started_at, complete: point.complete }));
  const successSeries = [{
    id: "success",
    label: "成功率",
    values: (status?.series.points ?? []).map((point) => {
      const requests = Number(point.requests) || 0;
      return requests > 0 ? (Number(point.successes) || 0) / requests : null;
    }),
  }];
  const latencySeries = [{
    id: "latency",
    label: "平均耗时",
    values: (status?.series.points ?? []).map((point) => Number(point.requests) > 0 ? Number(point.avg_duration_ms) || 0 : null),
  }];
  const matrixRows = status?.matrix.rows ?? [];
  const total = status?.snapshot.total;
  const successRatio = total && Number(total.requests) > 0 ? (Number(total.successes) || 0) / Number(total.requests) : 0;
  const cacheRatio = total && Number(total.prompt_tokens) > 0 ? (Number(total.cached_tokens) || 0) / Number(total.prompt_tokens) : 0;
  const matrixTotal = cellTotal(matrixRows);

  const weekPoints = weekQuery.data?.points ?? [];
  const calendarPoints = calendarQuery.data?.points ?? [];
  const workspaceLinks = workspaceQuery.data?.links ?? [];
  const rawLayerLabels = Array.isArray(workspaceQuery.data?.layers)
    ? (workspaceQuery.data?.layers as unknown as string[])
    : [];
  const LAYER_TEXT: Record<string, string> = {
    workspace: "工作空间",
    task: "任务",
    requested_model_id: "请求模型",
    model_id: "实际模型",
    provider_id: "供应商",
    key_name: "上游 Key",
    upstream_model_id: "上游模型",
  };
  const layerLabels = rawLayerLabels.map((label) => LAYER_TEXT[label] ?? label);

  return (
    <>
      <Section title="图表 / 时间趋势" description={`筛选：${rangeLabelText}。多系列折线、堆叠柱与小倍数共用同一批真实桶。`}>
        <div className="grid gap-3 xl:grid-cols-2">
          <TimeSeriesChart
            buckets={modelBuckets}
            series={modelSeries}
            format={(value) => `${formatCompactNumber(value)} 次`}
            variant="stacked-area"
            height={220}
            ariaLabel="模型请求趋势"
            tableUnit="次"
            busy={breakdownQuery.isFetching}
            error={breakdownQuery.error ? errorText(breakdownQuery.error) : undefined}
          />
          <StackedColumns
            buckets={modelBuckets}
            series={modelSeries}
            format={(value) => `${formatCompactNumber(value)} 次`}
            height={220}
            ariaLabel="模型请求堆叠"
            tableUnit="次"
            busy={breakdownQuery.isFetching}
            error={breakdownQuery.error ? errorText(breakdownQuery.error) : undefined}
          />
          <TimeSeriesChart
            buckets={statusBuckets}
            series={successSeries}
            format={formatPercentValue}
            percent
            height={190}
            ariaLabel="请求成功率"
            tableUnit="%"
            referenceLines={[{ value: 0.95, label: "95% 参考" }]}
            busy={statusQuery.isFetching}
            error={statusQuery.error ? errorText(statusQuery.error) : undefined}
          />
          <TimeSeriesChart
            buckets={statusBuckets}
            series={latencySeries}
            format={formatDurationValue}
            height={190}
            ariaLabel="平均耗时趋势"
            tableUnit="ms"
            busy={statusQuery.isFetching}
            error={statusQuery.error ? errorText(statusQuery.error) : undefined}
          />
        </div>
        {modelGroups.length ? (
          <SmallMultiples
            buckets={modelBuckets}
            panels={modelGroups.slice(0, 4).map((group) => ({
              id: breakdownName(group.key),
              label: breakdownName(group.key),
              series: [{ id: breakdownName(group.key), label: breakdownName(group.key), values: align(group.series.requests, modelBuckets.length) }],
              format: (value: number) => `${formatCompactNumber(value)} 次`,
              tableUnit: "次",
            }))}
            format={(value) => `${formatCompactNumber(value)} 次`}
            ariaLabel="模型请求小倍数"
          />
        ) : null}
      </Section>

      <Section title="图表 / 排行与构成" description="排行行悬停可“仅看此项”，会把筛选写进 URL；构成条与迷你趋势都来自同一份拆分。">
        <div className="grid gap-3 lg:grid-cols-2">
          <RankBars
            items={rankItems}
            format={(value) => `${formatCompactNumber(value)} 次`}
            title="模型请求排行"
            ariaLabel="模型请求排行"
            colorScope="gallery:model_requests"
            unit="次"
            busy={breakdownQuery.isFetching}
            error={breakdownQuery.error ? errorText(breakdownQuery.error) : undefined}
            onSelect={(item) => select("model", item.id)}
          />
          <ShareBar
            items={tokenItems}
            format={(value) => `${formatCompactNumber(value)} Token`}
            title="模型 Token 构成"
            ariaLabel="模型 Token 构成"
            unit="Token"
            busy={breakdownQuery.isFetching}
            error={breakdownQuery.error ? errorText(breakdownQuery.error) : undefined}
          />
        </div>
        <Panel>
          <div className="flex flex-wrap items-center justify-between gap-3">
            <div>
              <div className="text-meta font-medium text-ink-2">{topGroup ? breakdownName(topGroup.key) : "模型"} · 请求迷你趋势</div>
              <div className="mt-0.5 text-meta text-ink-3">Sparkline 只表示方向；逐点读数与表格视图请看上方完整趋势图。</div>
            </div>
            <div className="min-w-0 flex-1 sm:max-w-[520px]">
              {weekQuery.isLoading ? <Skeleton className="h-7" /> : (
                <Sparkline
                  values={topSeries}
                  timestamps={modelBuckets.map((bucket) => bucket.startedAt)}
                  format={(value) => `${formatCompactNumber(value)} 次`}
                  ariaLabel="模型请求迷你趋势"
                  height={30}
                />
              )}
            </div>
          </div>
        </Panel>
      </Section>

      <Section title="图表 / 热力与健康" description="周热力图 7×24 格不横滚；健康矩阵 49 列也不横滚，窗口未覆盖与累加中都有独立纹理。">
        <div className="grid gap-3 xl:grid-cols-2">
          <Heatmap
            mode="week"
            points={weekPoints}
            from={weekPoints[0]?.started_at}
            to={weekPoints[weekPoints.length - 1]?.ended_at}
            format={(value) => formatCompactNumber(value)}
            title="周 × 小时请求热力"
            ariaLabel="周 × 小时请求热力"
            unit="次"
            busy={weekQuery.isFetching}
            error={weekQuery.error ? errorText(weekQuery.error) : undefined}
          />
          <Heatmap
            mode="calendar"
            points={calendarPoints}
            from={calendarPoints[0]?.started_at}
            to={calendarPoints[calendarPoints.length - 1]?.ended_at}
            format={(value) => formatCompactNumber(value)}
            title="最近 30 天请求日历"
            ariaLabel="最近 30 天请求日历"
            unit="次"
            busy={calendarQuery.isFetching}
            error={calendarQuery.error ? errorText(calendarQuery.error) : undefined}
          />
        </div>
        <HealthMatrix
          columns={statusBuckets.map((bucket) => ({ startedAt: bucket.startedAt, endedAt: status?.series.points.find((point) => point.started_at === bucket.startedAt)?.ended_at, complete: bucket.complete }))}
          rows={matrixRows.map((row, index) => {
            const labels = rowLabel(row, status?.group_by ?? "provider_key");
            const requests = row.cells.reduce((sum, cell) => sum + (Number(cell.requests) || 0), 0);
            const successes = row.cells.reduce((sum, cell) => sum + (Number(cell.successes) || 0), 0);
            return {
              id: `${row.provider_id ?? "none"}-${row.upstream_model_id ?? ""}-${row.key_name ?? ""}-${index}`,
              label: labels.label,
              sublabel: labels.detail,
              summary: requests ? formatPercentValue(successes / requests) : "无流量",
              cells: row.cells,
            };
          })}
          mode="overall"
          title="渠道健康矩阵"
          ariaLabel="渠道健康矩阵"
          busy={statusQuery.isFetching}
          error={statusQuery.error ? errorText(statusQuery.error) : undefined}
        />
      </Section>

      <Section title="图表 / 流向与仪表" description="桑基流向从 workspace-usage 真实连边绘制；仪表只读当前窗口的快照比率。">
        <div className="grid gap-3 xl:grid-cols-[minmax(0,1.8fr)_minmax(220px,1fr)]">
          <FlowMap
            links={workspaceLinks}
            layerLabels={layerLabels}
            metric="requests"
            format={(value) => `${formatCompactNumber(value)} 次`}
            height={360}
            ariaLabel="工作空间流向"
            busy={workspaceQuery.isFetching}
            error={workspaceQuery.error ? errorText(workspaceQuery.error) : undefined}
            onNodeClick={(node) => toast.message(`${node.name} · ${formatCompactNumber(node.value)} 次`)}
          />
          <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-1">
            <Meter value={successRatio} label="窗口成功率" tone={successRatio >= 0.95 ? "good" : "warn"} format={formatPercentValue} ariaLabel="窗口成功率" />
            <Meter value={cacheRatio} label="Token 缓存率" tone="accent" format={formatPercentValue} ariaLabel="Token 缓存率" hint="cached / prompt" />
          </div>
        </div>
      </Section>

      <Section title="图表 / 快照读数" description="健康快照的实际总量，用于和上方趋势交叉核对。">
        <div className="grid gap-3 sm:grid-cols-2">
          <Panel title="渠道矩阵合计">
            <div className="flex flex-wrap items-center gap-x-5 gap-y-2 text-meta">
              <span className="inline-flex items-center gap-1.5 text-ink-2"><Database aria-hidden className="size-4 text-ink-3" />请求 <strong className="tabular-nums">{formatCompactNumber(matrixTotal.requests)}</strong></span>
              <span className="inline-flex items-center gap-1.5 text-ink-2"><Activity aria-hidden className="size-4 text-good" />成功 <strong className="tabular-nums">{formatCompactNumber(matrixTotal.successes)}</strong></span>
              <span className="inline-flex items-center gap-1.5 text-ink-2"><Gauge aria-hidden className="size-4 text-warn" />失败 <strong className="tabular-nums">{formatCompactNumber(matrixTotal.failures)}</strong></span>
            </div>
          </Panel>
          <Panel title="窗口边界">
            <div className="flex flex-wrap items-center gap-2 text-meta text-ink-3">
              <Waypoints aria-hidden className="size-4" />
              {status?.window.from ? formatDateTime(status.window.from) : "—"} <ArrowRight aria-hidden className="size-3.5" /> {status?.window.to ? formatDateTime(status.window.to) : "—"}
            </div>
          </Panel>
        </div>
      </Section>
    </>
  );
}
