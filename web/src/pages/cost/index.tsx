// 成本：回答「钱花在哪」。
//
// 旧版成本页最大的缺陷是从不刷新，并且把快照里的 upstream_models 当数据源——v4
// 数据上那份分组恒为空，排行永远为空。这里改用 usage-breakdown 的 upstream_model
// 拆分，60 秒轮询；目录不可用时只显示 Token 读数，任何金额位置都不会退化成 $0。

import { useMemo } from "react";
import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { errorText, formatDateTime } from "@/lib/format";
import { rangeLabel, rangeToQuery, toApiFilters, FILTER_DIMENSIONS } from "@/lib/filters";
import { useMetricFilters, useSelectFilter } from "@/lib/hooks/useMetricFilters";
import { formatCompactNumber, MAX_HISTORY_HOURS, pickBucketSeconds } from "@/lib/chart-math";
import { loadPricing, pricingStatus } from "@/lib/pricing";
import { queryKeys } from "@/lib/query-keys";
import { Banner } from "@/components/ui/Banner";
import { Button } from "@/components/ui/Button";
import { MetricCell } from "@/components/ui/MetricStrip";
import { Panel } from "@/components/ui/Panel";
import { FilterBar } from "@/components/filters/FilterBar";
import { CostKpiBand } from "./CostKpiBand";
import {
  CostCompositionPanel,
  CostRankingPanel,
  CostTrendPanel,
  RecentCostsPanel,
} from "./CostPanels";
import { buildCostSummary } from "./cost-model";

const DEFAULT_RANGE = "7d";
const POLL_MS = 60_000;

function count(value: unknown): number {
  const parsed = Number(value);
  return Number.isFinite(parsed) ? parsed : 0;
}

export default function CostPage() {
  const filters = useMetricFilters(DEFAULT_RANGE);
  const rangeQuery = rangeToQuery(filters.state.range);
  const bucketSeconds = pickBucketSeconds(rangeQuery.hours ?? MAX_HISTORY_HOURS);
  const filterArgs = toApiFilters(filters.state);
  const selectFilter = useSelectFilter();

  const pricingQuery = useQuery({
    queryKey: queryKeys.pricing(),
    queryFn: () => loadPricing(() => api.pricing()),
    staleTime: 5 * 60_000,
    retry: false,
  });
  const index = pricingQuery.data ?? null;

  const breakdownOptions = {
    groupBy: "upstream_model" as const,
    rankBy: "tokens" as const,
    limit: 50,
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

  const filteredOptions = {
    ...rangeQuery,
    bucketSeconds,
    limit: 50,
    ...filterArgs,
  };
  const filteredQuery = useQuery({
    queryKey: queryKeys.filteredMetrics(filteredOptions),
    queryFn: () => api.filteredMetrics(filteredOptions),
    placeholderData: keepPreviousData,
    refetchInterval: POLL_MS,
    refetchIntervalInBackground: false,
  });

  const breakdownData = breakdownQuery.data;
  const totals = filteredQuery.data?.snapshot.total;
  const summary = useMemo(
    () => buildCostSummary(
      index,
      breakdownData,
      count(totals?.total_tokens),
      rangeQuery.hours ?? null,
    ),
    [index, breakdownData, totals?.total_tokens, rangeQuery.hours],
  );
  const status = pricingStatus();
  const windowLabel = rangeLabel(filters.state.range);
  const loading = breakdownQuery.isPending || filteredQuery.isPending;
  const tokenOnlyLoading = filteredQuery.isPending;

  return (
    <div className="min-w-0 space-y-3">
      <style>{`
        @media (max-width: 1499px) {
          .cost-rank-table thead tr > th:nth-child(5),
          .cost-rank-table tbody tr > td:nth-child(5) { display: none; }
        }
        @media (max-width: 767px) {
          .cost-rank-table thead tr > th:nth-child(3),
          .cost-rank-table tbody tr > td:nth-child(3),
          .cost-rank-table thead tr > th:nth-child(4),
          .cost-rank-table tbody tr > td:nth-child(4),
          .cost-rank-table thead tr > th:nth-child(5),
          .cost-rank-table tbody tr > td:nth-child(5),
          .cost-rank-table thead tr > th:nth-child(8),
          .cost-rank-table tbody tr > td:nth-child(8) { display: none; }
        }
        @media (max-width: 479px) {
          .cost-rank-table thead tr > th:nth-child(2),
          .cost-rank-table tbody tr > td:nth-child(2),
          .cost-rank-table thead tr > th:nth-child(9),
          .cost-rank-table tbody tr > td:nth-child(9) { display: none; }
        }
      `}</style>

      <FilterBar defaultRange={DEFAULT_RANGE} supported={FILTER_DIMENSIONS} />

      {pricingQuery.isPending ? (
        <Panel title="价格目录" busy>
          <p className="text-meta text-ink-3">正在读取 models.dev 价格目录…</p>
        </Panel>
      ) : null}

      {!pricingQuery.isPending && !index ? (
        <TokenOnlyFallback
          totals={totals}
          loading={tokenOnlyLoading}
          error={pricingQuery.error}
          onRetry={() => void pricingQuery.refetch()}
          retrying={pricingQuery.isFetching}
        />
      ) : null}

      {index ? (
        <>
          {breakdownQuery.error || filteredQuery.error ? (
            <Banner tone="critical" title="部分成本读数读取失败">
              {errorText(breakdownQuery.error ?? filteredQuery.error)}
            </Banner>
          ) : null}

          {status.stale ? (
            <Banner tone="warn" title="价格目录最近一次刷新失败">
              {`当前显示上次成功获取的价格（${status.staleReason || "原因未知"}），金额可能已经过期。`}
            </Banner>
          ) : null}

          <CostKpiBand summary={summary} loading={loading} />

          <CostTrendPanel
            data={breakdownData}
            summary={summary}
            busy={breakdownQuery.isFetching && !breakdownQuery.isPending}
            error={breakdownQuery.error ? errorText(breakdownQuery.error) : undefined}
            windowLabel={windowLabel}
          />

          <div className="grid min-w-0 gap-3 xl:grid-cols-12">
            <div className="min-w-0 xl:col-span-4">
              <CostCompositionPanel
                summary={summary}
                busy={breakdownQuery.isFetching && !breakdownQuery.isPending}
              />
            </div>
            <div className="min-w-0 xl:col-span-8">
              <CostRankingPanel
                summary={summary}
                currentProviders={filters.state.provider}
                onSelectProvider={(providerId) => selectFilter("provider", providerId)}
                busy={breakdownQuery.isFetching && !breakdownQuery.isPending}
              />
            </div>
          </div>

          <RecentCostsPanel
            items={filteredQuery.data?.requests?.items ?? []}
            index={index}
            busy={filteredQuery.isFetching && !filteredQuery.isPending}
          />

          <PriceFooter
            source={index.source}
            updatedAt={index.updatedAt}
            stale={status.stale}
          />
        </>
      ) : null}
    </div>
  );
}

function TokenOnlyFallback({
  totals,
  loading,
  error,
  onRetry,
  retrying,
}: {
  totals: { prompt_tokens?: number; completion_tokens?: number; total_tokens?: number; cached_tokens?: number; cache_read_tokens?: number } | undefined;
  loading: boolean;
  error: unknown;
  onRetry: () => void;
  retrying: boolean;
}) {
  const prompt = count(totals?.prompt_tokens);
  const cacheRead = count(totals?.cache_read_tokens) || count(totals?.cached_tokens);
  const output = count(totals?.completion_tokens);
  const total = count(totals?.total_tokens) || Math.max(0, prompt) + output;
  return (
    <Panel
      title="价格目录暂不可用"
      description="成本需要 models.dev 的单价；目录就绪前只显示 Token 读数，不显示任何金额。"
      busy={loading}
      actions={(
        <Button size="sm" variant="secondary" loading={retrying} onClick={onRetry}>
          重新获取价格目录
        </Button>
      )}
    >
      <div className="grid grid-cols-2 gap-px overflow-hidden rounded-control border border-rule bg-rule md:grid-cols-4">
        <MetricCell className="min-w-0 bg-panel" label="总 Token" value={loading ? "—" : formatCompactNumber(total)} unit="Token" />
        <MetricCell
          className="min-w-0 bg-panel"
          label="输入（未缓存）"
          value={loading ? "—" : formatCompactNumber(Math.max(0, prompt - cacheRead))}
          unit="Token"
        />
        <MetricCell className="min-w-0 bg-panel" label="缓存读" value={loading ? "—" : formatCompactNumber(cacheRead)} unit="Token" />
        <MetricCell className="min-w-0 bg-panel" label="输出" value={loading ? "—" : formatCompactNumber(output)} unit="Token" />
      </div>
      {error ? (
        <p className="mt-3 text-meta text-ink-3">目录状态：{errorText(error)}</p>
      ) : null}
    </Panel>
  );
}

function PriceFooter({
  source,
  updatedAt,
  stale,
}: {
  source: string | null;
  updatedAt: string | null;
  stale: boolean;
}) {
  return (
    <footer className="rounded-panel border border-rule bg-sunken px-4 py-3 text-meta text-ink-3">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
        <strong className="font-medium text-ink-2">估算不是账单</strong>
        <span>价格来源 {source || "models.dev"}</span>
        <span>目录更新时间 {updatedAt ? formatDateTime(updatedAt) : "未提供"}</span>
        {stale ? <span className="text-warn-ink">目录可能已过期</span> : null}
      </div>
      <p className="mt-1">
        单价来自 models.dev 的公开目录，与上游实际计费可能有出入；金额只用于比较量级与相对开销。
        拆分序列没有缓存写字段，缓存写按 0 计。
      </p>
    </footer>
  );
}
