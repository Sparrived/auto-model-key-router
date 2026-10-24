// 成本页的四块内容：趋势、构成、排行、最近请求。
//
// 所有金额都只是「估算」：单价来自 models.dev 的公开目录，金额不落库。这里刻意
// 不提供任何「账单」措辞，也不把无单价的结果补成 $0。

import { useMemo, type ReactNode } from "react";
import { ArrowRight } from "lucide-react";
import type { RequestItem, UsageBreakdownResponse } from "@/lib/types";
import type { PriceIndex } from "@/lib/pricing";
import { formatCost, requestCost } from "@/lib/pricing";
import { formatCompactNumber, formatPercentValue } from "@/lib/chart-math";
import { formatCount, formatDateTime } from "@/lib/format";
import { useSeriesColors } from "@/lib/series-colors";
import { ProviderIcon } from "@/lib/brand-icons";
import { Badge } from "@/components/ui/Badge";
import { Button } from "@/components/ui/Button";
import { DataTable, type DataTableColumn } from "@/components/ui/DataTable";
import { Empty } from "@/components/ui/Empty";
import { Panel } from "@/components/ui/Panel";
import { ShareBar } from "@/components/charts/ShareBar";
import { TimeSeriesChart } from "@/components/charts/TimeSeriesChart";
import {
  compositionItems,
  costSeriesForGroup,
  money,
  price,
  type CostModelRow,
  type CostSummary,
} from "./cost-model";

function groupId(provider: string | null, model: string): string {
  return `${provider ?? ""}/${model || "__unattributed__"}`;
}

export function CostTrendPanel({
  data,
  summary,
  busy,
  error,
  windowLabel,
}: {
  data: UsageBreakdownResponse | undefined;
  summary: CostSummary;
  busy: boolean;
  error: ReactNode;
  windowLabel: string;
}) {
  const rowsById = useMemo(
    () => new Map(summary.rows.map((row) => [row.id, row])),
    [summary.rows],
  );
  const pricedGroups = useMemo(() => (data?.groups ?? []).map((group) => {
    const key = group.key;
    const provider = typeof key === "string" ? null : key.provider_id ?? null;
    const model = typeof key === "string" ? key : key.upstream_model_id ?? "";
    const row = rowsById.get(groupId(provider, model));
    return { group, provider, model, row };
  }).filter((item) => Boolean(item.row?.entry)), [data, rowsById]);

  const colorKeys = pricedGroups.map((item) => groupId(item.provider, item.model));
  const colorOf = useSeriesColors("upstream_model", colorKeys);
  const modelCounts = new Map<string, number>();
  for (const item of pricedGroups) modelCounts.set(item.model, (modelCounts.get(item.model) ?? 0) + 1);
  const buckets = (data?.buckets ?? []).map((bucket) => ({
    startedAt: bucket.started_at,
    complete: bucket.complete,
  }));
  const series = pricedGroups.map((item) => {
    const row = item.row as CostModelRow;
    return {
      id: groupId(item.provider, item.model),
      label: (modelCounts.get(item.model) ?? 0) > 1
        ? `${item.model}（${item.provider ?? "未归属"}）`
        : item.model,
      color: colorOf(groupId(item.provider, item.model)),
      values: data ? costSeriesForGroup(item.group, row.entry, data.buckets) : [],
    };
  });

  const unpricedNames = summary.unpricedRows
    .map((row) => [row.providerId, row.upstreamModel].filter(Boolean).join(" / "))
    .join("、");

  return (
    <Panel
      title="成本趋势"
      description={`按上游模型堆叠，只画有单价的模型；窗口 ${windowLabel}。`}
      busy={busy}
      toolbar={(
        <Badge tone="neutral">
          {summary.pricedRows.length} 个已计价模型
        </Badge>
      )}
      footer={(
        <div className="space-y-1 text-meta text-ink-3">
          <p>
            {summary.unpricedRows.length
              ? `未计价模型：${unpricedNames}（共 ${formatCompactNumber(summary.unpricedTokens)} Token）`
              : "全部上游模型都有定价。"}
          </p>
          <p>拆分序列没有缓存写字段，缓存写按 0 计；金额为估算，不是账单。</p>
        </div>
      )}
    >
      <TimeSeriesChart
        buckets={buckets}
        series={series}
        format={formatCost}
        height={318}
        variant="stacked-area"
        ariaLabel="按上游模型的估算成本趋势"
        tableUnit="USD"
        busy={busy}
        error={data ? undefined : (error ?? undefined)}
        emptyText="当前窗口没有可计价的成本趋势"
        embedded
      />
    </Panel>
  );
}

export function CostCompositionPanel({
  summary,
  busy,
}: {
  summary: CostSummary;
  busy: boolean;
}) {
  const colorOf = useSeriesColors("cost-parts", ["input", "cache-read", "cache-write", "output"]);
  const items = compositionItems(summary.parts).map((item) => ({
    ...item,
    color: colorOf(item.id),
  }));
  return (
    <Panel
      title="成本构成"
      description="四项成本相加就是窗口的估算总额。"
      busy={busy}
      footer="缓存写按 0 计；输入是扣除缓存读、缓存写后的新鲜输入。"
    >
      <ShareBar
        items={items}
        format={money}
        total={summary.totalCost}
        unit="USD"
        ariaLabel="成本构成"
        busy={busy}
        emptyText="当前窗口没有可估算的已计价成本"
        embedded
      />
      <div className="mt-3 grid grid-cols-2 gap-x-4 gap-y-2">
        {items.map((item) => (
          <div key={item.id} className="min-w-0">
            <div className="flex items-center gap-1.5 text-[11px] text-ink-3">
              <span aria-hidden className="size-2 shrink-0 rounded-[2px]" style={{ backgroundColor: item.color }} />
              <span className="truncate">{item.label}</span>
            </div>
            <div className="mt-0.5 flex items-baseline justify-between gap-2">
              <span className="truncate text-title font-medium tabular-nums text-ink" title={money(item.value)}>
                {money(item.value)}
              </span>
              <span className="shrink-0 text-[11px] tabular-nums text-ink-3">
                {summary.totalCost > 0 ? formatPercentValue(item.value / summary.totalCost) : "—"}
              </span>
            </div>
          </div>
        ))}
      </div>
    </Panel>
  );
}

export function CostRankingPanel({
  summary,
  currentProviders,
  onSelectProvider,
  busy,
}: {
  summary: CostSummary;
  currentProviders: string[];
  onSelectProvider: (providerId: string) => void;
  busy: boolean;
}) {
  const colorOf = useSeriesColors("upstream_model", summary.rows.map((row) => row.id));
  const columns: DataTableColumn<CostModelRow>[] = [
    {
      key: "model",
      header: "上游模型",
      cell: (row) => (
        <div className="min-w-0">
          <span className="block truncate font-mono text-[12px] text-ink" title={row.upstreamModel}>
            {row.upstreamModel}
          </span>
          {row.entry === null ? <span className="block text-[11px] text-warn-ink">未计价</span> : null}
          {row.providerId ? (
            <Button
              size="sm"
              variant="ghost"
              className="mt-0.5 px-0 sm:hidden"
              onClick={() => onSelectProvider(row.providerId as string)}
            >
              仅看此供应商
            </Button>
          ) : null}
        </div>
      ),
      sortValue: (row) => row.upstreamModel,
      width: "20%",
    },
    {
      key: "provider",
      header: "供应商",
      cell: (row) => row.providerId ? (
        <span className="flex min-w-0 items-center gap-1.5">
          <ProviderIcon id={row.providerId} baseUrl={null} size={15} />
          <span className="truncate text-[12px] text-ink-2">{row.providerId}</span>
          {currentProviders.includes(row.providerId) ? <Badge tone="neutral">已筛</Badge> : null}
        </span>
      ) : <span className="text-ink-3">未归属</span>,
      sortValue: (row) => row.providerId,
      width: 120,
    },
    {
      key: "input",
      header: "输入 $/1M",
      align: "right",
      cell: (row) => <span className="tabular-nums">{price(row.entry?.input)}</span>,
      sortValue: (row) => row.entry?.input ?? null,
      width: 92,
      tabular: true,
    },
    {
      key: "output",
      header: "输出 $/1M",
      align: "right",
      cell: (row) => <span className="tabular-nums">{price(row.entry?.output)}</span>,
      sortValue: (row) => row.entry?.output ?? null,
      width: 92,
      tabular: true,
    },
    {
      key: "cache",
      header: "缓存读 $/1M",
      align: "right",
      cell: (row) => (
        <span className="tabular-nums">{price(row.entry ? (row.entry.cacheRead ?? row.entry.input) : null)}</span>
      ),
      sortValue: (row) => (row.entry ? row.entry.cacheRead ?? row.entry.input : null),
      width: 104,
      tabular: true,
    },
    {
      key: "tokens",
      header: "Token",
      align: "right",
      cell: (row) => <span className="tabular-nums">{formatCompactNumber(Number(row.stats.total_tokens) || 0)}</span>,
      sortValue: (row) => Number(row.stats.total_tokens) || 0,
      width: 80,
      tabular: true,
    },
    {
      key: "cost",
      header: "成本",
      align: "right",
      cell: (row) => <span className="tabular-nums">{money(row.cost)}</span>,
      sortValue: (row) => row.cost,
      width: 86,
      tabular: true,
    },
    {
      key: "share",
      header: "占比",
      cell: (row) => {
        const share = summary.totalCost > 0 && row.cost !== null ? row.cost / summary.totalCost : 0;
        return (
          <span className="flex min-w-[54px] items-center gap-1.5">
            <span className="h-1.5 min-w-0 flex-1 overflow-hidden rounded-full bg-sunken" aria-hidden>
              <span
                className="block h-full rounded-full"
                style={{ width: `${Math.min(100, share * 100)}%`, backgroundColor: colorOf(row.id) }}
              />
            </span>
            <span className="w-9 shrink-0 text-right text-[11px] tabular-nums text-ink-3">
              {summary.totalCost > 0 ? formatPercentValue(share) : "—"}
            </span>
          </span>
        );
      },
      sortValue: (row) => (row.cost !== null && summary.totalCost > 0 ? row.cost / summary.totalCost : null),
      width: 92,
    },
  ];

  return (
    <Panel
      title="模型成本排行"
      description="按成本排序；未计价模型保留在表里，金额显示为「—」。"
      busy={busy}
      padding={false}
      className="min-w-0 overflow-hidden"
      footer="上游模型不是全局筛选维度；「仅看此供应商」按供应商收窄，表格仍保留模型粒度。"
    >
      <DataTable
        aria-label="模型成本排行"
        className="cost-rank-table"
        columns={columns}
        rows={summary.rows}
        rowKey={(row) => row.id}
        dense
        loading={busy}
        loadingRows={5}
        emptyState={(
          <Empty
            title="当前窗口没有上游模型"
            description="换个时间窗或筛选条件；没有调用的模型不会出现在排行里。"
          />
        )}
        rowActions={(row) => row.providerId ? (
          <Button
            size="sm"
            variant="ghost"
            disabled={currentProviders.includes(row.providerId)}
            onClick={() => onSelectProvider(row.providerId as string)}
          >
            {currentProviders.includes(row.providerId) ? "已按此供应商筛选" : "仅看此供应商"}
          </Button>
        ) : null}
      />
    </Panel>
  );
}

export function RecentCostsPanel({
  items,
  index,
  busy,
}: {
  items: RequestItem[];
  index: PriceIndex | null;
  busy: boolean;
}) {
  const costs = items.map((item) => requestCost(index, item));
  const pricedCount = costs.filter((cost) => cost !== null).length;
  return (
    <Panel
      title="最近请求成本"
      description={`窗口内最近 ${items.length} 条请求，列表可上下滚动；无单价的模型显示「—」。`}
      busy={busy}
      padding={false}
      footer="逐条成本按请求的 upstream_model_id 匹配单价，不使用本地路由名；无单价时不会显示为零。"
    >
      {items.length ? (
        <ul className="max-h-[440px] divide-y divide-rule overflow-y-auto" aria-label="最近请求成本列表">
          {items.map((item, index_) => {
            const cost = costs[index_];
            const route = item.model_id || item.requested_model_id || "—";
            const upstream = item.upstream_model_id || "—";
            return (
              <li key={item.id} className="grid min-w-0 grid-cols-[minmax(0,1fr)_auto] items-center gap-x-3 gap-y-1 px-4 py-2 sm:flex sm:flex-wrap">
                <time className="col-span-1 shrink-0 text-[11px] tabular-nums text-ink-3 sm:w-[132px]" dateTime={item.created_at}>
                  {formatDateTime(item.created_at)}
                </time>
                <div className="col-span-1 flex shrink-0 items-center justify-end gap-3 text-right text-[12px] sm:hidden">
                  <span className="tabular-nums text-ink-3">{formatCount(Number(item.total_tokens) || 0)} Token</span>
                  <span className="min-w-[56px] font-medium tabular-nums text-ink">{money(cost)}</span>
                </div>
                <div className="col-span-2 flex min-w-0 flex-col gap-0.5 text-[12px] sm:col-span-1 sm:flex-1 sm:flex-row sm:items-center sm:gap-1.5">
                  <span className="truncate font-mono text-ink-2" title={route}>{route}</span>
                  <ArrowRight aria-hidden className="hidden size-3 shrink-0 text-ink-3 sm:block" />
                  <span className="truncate font-mono text-ink" title={upstream}>{upstream}</span>
                </div>
                <div className="hidden shrink-0 items-center gap-3 text-right text-[12px] sm:flex">
                  <span className="tabular-nums text-ink-3">{formatCount(Number(item.total_tokens) || 0)} Token</span>
                  <span className="min-w-[64px] font-medium tabular-nums text-ink">{money(cost)}</span>
                </div>
              </li>
            );
          })}
        </ul>
      ) : (
        <Empty
          title="当前窗口没有请求"
          description="换个时间窗或筛选条件；最近请求列表最多显示 50 条。"
        />
      )}
      {items.length ? (
        <p className="border-t border-rule px-4 py-2 text-[11px] text-ink-3">
          {pricedCount}/{items.length} 条可计价；其余显示「—」。
        </p>
      ) : null}
    </Panel>
  );
}
