// 工作空间：只看时间窗的用量看板。
//
// 这里回答“谁在用、用在哪”：KPI 是归属工作空间的汇总，流向图展开
// workspace → 请求模型 → 实际模型 → 供应商 → 上游 Key → 上游模型 六层，
// 明细表再把每个空间的请求、Token 与占比摊开。数据来自本项目的
// /ui/workspace-usage.json，和「用量统计」共用同一条读数口径。

import { useMemo, useState } from "react";
import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { useMetricFilters } from "@/lib/hooks/useMetricFilters";
import { rangeLabel, rangeToQuery } from "@/lib/filters";
import { api } from "@/lib/api";
import { errorText, formatCompact, formatCount, formatPercent } from "@/lib/format";
import { queryKeys } from "@/lib/query-keys";
import { navigate } from "@/app/router";
import type { WorkspaceUsageWorkspace } from "@/lib/types";
import { Banner } from "@/components/ui/Banner";
import { DataTable, type DataTableColumn } from "@/components/ui/DataTable";
import { Empty } from "@/components/ui/Empty";
import { MetricCell, MetricStrip } from "@/components/ui/MetricStrip";
import { PageHeader } from "@/components/ui/PageHeader";
import { Panel } from "@/components/ui/Panel";
import { Segmented } from "@/components/ui/Segmented";
import { Skeleton } from "@/components/ui/Skeleton";
import { FlowMap } from "@/components/charts";
import { FilterBar } from "@/components/filters/FilterBar";

const DEFAULT_RANGE = "7d";
const POLL_MS = 30_000;

const LAYER_LABELS: Record<string, string> = {
  workspace: "工作空间",
  requested_model_id: "请求模型",
  model_id: "实际模型",
  provider_id: "供应商",
  key_name: "上游 Key",
  upstream_model_id: "上游模型",
};

function layerLabel(layer: string | { name?: string; label?: string }): string {
  const raw = typeof layer === "string" ? layer : layer.label || layer.name || "";
  return LAYER_LABELS[raw] ?? raw;
}

function workspaceStats(workspace: WorkspaceUsageWorkspace) {
  const stats = workspace.stats ?? { requests: 0 };
  return {
    requests: Number(stats.requests) || 0,
    successes: Number(stats.successes) || 0,
    totalTokens: Number(stats.total_tokens) || 0,
  };
}

export default function WorkspacesPage() {
  const filters = useMetricFilters(DEFAULT_RANGE);
  const rangeQuery = rangeToQuery(filters.state.range);
  const [metric, setMetric] = useState<"requests" | "tokens">("requests");

  const usageQuery = useQuery({
    queryKey: queryKeys.workspaceUsage({
      hours: rangeQuery.hours ?? undefined,
      allHistory: Boolean(rangeQuery.allHistory),
    }),
    queryFn: () => api.workspaceUsage({
      hours: rangeQuery.hours ?? 24,
      allHistory: Boolean(rangeQuery.allHistory),
    }),
    placeholderData: keepPreviousData,
    // 这是“实时”看板：30 秒必须重新请求。旧版把刷新函数挂在 onTick 上，
    // 但数据命中缓存后直接返回，导致数字不会动——这里交给 React Query 的
    // refetchInterval，并用 keepPreviousData 避免每次刷新闪成骨架。
    refetchInterval: POLL_MS,
    refetchIntervalInBackground: false,
  });

  const data = usageQuery.data;
  const workspaces = useMemo(() => data?.workspaces ?? [], [data]);
  const totals = useMemo(() => workspaces.reduce((acc, workspace) => {
    const stats = workspaceStats(workspace);
    return {
      requests: acc.requests + stats.requests,
      successes: acc.successes + stats.successes,
      tokens: acc.tokens + stats.totalTokens,
    };
  }, { requests: 0, successes: 0, tokens: 0 }), [workspaces]);

  const busiest = useMemo(() => [...workspaces]
    .sort((a, b) => workspaceStats(b).requests - workspaceStats(a).requests)[0], [workspaces]);

  const unattributed = data?.unattributed;
  const unattributedRequests = Number(unattributed?.requests) || 0;
  const successRate = formatPercent(totals.successes, totals.requests);
  const windowText = rangeLabel(filters.state.range);
  const layerLabels = (data?.layers ?? []).map(layerLabel);
  const flowLayerLabels = layerLabels.length
    ? layerLabels
    : ["工作空间", "请求模型", "实际模型", "供应商", "上游 Key", "上游模型"];

  const columns = useMemo<DataTableColumn<WorkspaceUsageWorkspace>[]>(() => [
    {
      key: "name",
      header: "工作空间",
      cell: (workspace) => <span className="font-medium text-ink">{workspace.name}</span>,
      sortValue: (workspace) => workspace.name,
    },
    {
      key: "requests",
      header: "请求",
      align: "right",
      tabular: true,
      cell: (workspace) => formatCount(workspaceStats(workspace).requests),
      sortValue: (workspace) => workspaceStats(workspace).requests,
    },
    {
      key: "tokens",
      header: "Token",
      align: "right",
      tabular: true,
      cell: (workspace) => formatCompact(workspaceStats(workspace).totalTokens),
      sortValue: (workspace) => workspaceStats(workspace).totalTokens,
    },
    {
      key: "success",
      header: "成功率",
      align: "right",
      tabular: true,
      cell: (workspace) => {
        const stats = workspaceStats(workspace);
        return formatPercent(stats.successes, stats.requests);
      },
      sortValue: (workspace) => {
        const stats = workspaceStats(workspace);
        return stats.requests ? stats.successes / stats.requests : null;
      },
    },
    {
      key: "share",
      header: "请求占比",
      headerClassName: "hidden sm:table-cell",
      className: "hidden sm:table-cell",
      cell: (workspace) => {
        const stats = workspaceStats(workspace);
        const share = totals.requests > 0 ? stats.requests / totals.requests : 0;
        return (
          <div className="flex min-w-[120px] items-center gap-2">
            <div className="h-1.5 min-w-0 flex-1 overflow-hidden rounded-pill bg-sunken" aria-hidden>
              <div className="h-full rounded-pill bg-accent" style={{ width: `${share * 100}%` }} />
            </div>
            <span className="w-10 shrink-0 text-right text-meta tabular-nums text-ink-3">
              {Math.round(share * 100)}%
            </span>
            <span className="sr-only">{formatPercent(stats.requests, totals.requests)}</span>
          </div>
        );
      },
      sortValue: (workspace) => workspaceStats(workspace).requests,
    },
  ], [totals.requests]);

  return (
    <div className="flex min-w-0 flex-col gap-3">
      <PageHeader
        title="工作空间"
        description="只按时间窗观察各工作空间的归属用量；行点击可直接进入对应空间的任务路由。"
        actions={<span className="text-meta text-ink-3">{windowText}</span>}
      />

      <FilterBar hideDimensions defaultRange={DEFAULT_RANGE} />

      {usageQuery.error ? (
        <Banner tone="critical" title="读取工作空间用量失败">
          {errorText(usageQuery.error)}
        </Banner>
      ) : null}

      <MetricStrip className="cols-5">
        <MetricCell
          className="stat"
          label="工作空间数"
          value={formatCount(workspaces.length)}
          hint={busiest ? `最忙 ${busiest.name} · ${formatCount(workspaceStats(busiest).requests)} 次` : "窗口内暂无归属"}
        />
        <MetricCell className="stat" label="归属请求" value={formatCount(totals.requests)} hint="已归属到具体工作空间" />
        <MetricCell className="stat" label="归属 Token" value={formatCompact(totals.tokens)} hint="请求与响应合计" />
        <MetricCell className="stat" label="成功率" value={successRate} hint="按归属请求口径" />
        <MetricCell
          className="stat col-span-2 sm:col-span-1"
          label="未归属请求"
          value={formatCount(unattributedRequests)}
          hint={`${formatPercent(unattributedRequests, totals.requests + unattributedRequests)} 占比 · 没有工作空间归属；升级前的历史行会落在这里`}
          tone={unattributedRequests > 0 ? "warn" : "neutral"}
        />
      </MetricStrip>

      <Panel
        title="请求流向"
        description="六层链路：工作空间 → 请求模型 → 实际模型 → 供应商 → 上游 Key → 上游模型。悬停节点可追踪上下游路径。"
        actions={(
          <Segmented
            aria-label="流向宽度口径"
            value={metric}
            onValueChange={(value) => setMetric(value === "tokens" ? "tokens" : "requests")}
            options={[
              { value: "requests", label: "请求数" },
              { value: "tokens", label: "Token" },
            ]}
          />
        )}
      >
        {usageQuery.isPending && !data ? (
          <Skeleton variant="chart" className="h-[360px]" />
        ) : (
          <FlowMap
            links={data?.links ?? []}
            layerLabels={flowLayerLabels}
            metric={metric}
            colorLayer={0}
            colorScope="workspace"
            format={metric === "tokens" ? formatCompact : formatCount}
            height={360}
            busy={usageQuery.isFetching && !usageQuery.isPending}
            error={usageQuery.error ? errorText(usageQuery.error) : undefined}
            emptyText="当前时间窗口内没有可归属的流向数据"
            ariaLabel="工作空间请求流向"
          />
        )}
      </Panel>

      <Panel
        title="空间明细"
        description="按请求数、Token 或成功率排序；点击一行进入该空间的任务路由。"
        padding={false}
      >
        {usageQuery.isPending && !data ? (
          <div className="p-4"><Skeleton variant="table" /></div>
        ) : workspaces.length === 0 ? (
          <Empty title="窗口内没有工作空间用量" description="工作空间归属从记录该字段的版本起才开始写入。" />
        ) : (
          <DataTable
            aria-label="工作空间明细"
            rows={workspaces}
            rowKey={(workspace) => workspace.name}
            columns={columns}
            dense
            onRowClick={(workspace) => navigate("tasks", { ws: workspace.name })}
            emptyState={<Empty title="没有工作空间" />}
            className="workspace-table"
          />
        )}
      </Panel>
    </div>
  );
}
