// 成本页的纯算术层。
//
// 三条口径不能散落在组件里：
//   1. 每桶成本必须和总额同源，都是 costParts 的四项相加；
//   2. 无单价返回 null，界面显示「—」，绝不退化成 0；
//   3. 覆盖率按 Token 算，不是按模型数算；模型数只放在 hint 里解释分子分母。

import type { BreakdownBucket, BreakdownGroup, MetricStats, UsageBreakdownResponse } from "@/lib/types";
import {
  costParts,
  estimateCost,
  formatCost,
  formatPrice,
  lookupPrice,
  type CostParts,
  type PriceEntry,
  type PriceIndex,
} from "@/lib/pricing";

export interface CostModelRow {
  id: string;
  providerId: string | null;
  upstreamModel: string;
  displayName: string;
  entry: PriceEntry | null;
  stats: MetricStats;
  parts: CostParts | null;
  cost: number | null;
}

export interface CostSummary {
  rows: CostModelRow[];
  pricedRows: CostModelRow[];
  unpricedRows: CostModelRow[];
  totalCost: number;
  parts: CostParts;
  pricedTokens: number;
  allTokens: number;
  pricedRequests: number;
  coverage: number | null;
  modelCount: number;
  pricedModelCount: number;
  unpricedTokens: number;
  dayCount: number;
  pricedRequestCount: number;
  requestCount: number;
}

function count(value: unknown): number {
  const parsed = Number(value);
  return Number.isFinite(parsed) ? parsed : 0;
}

function sumParts(rows: CostModelRow[]): CostParts {
  return rows.reduce<CostParts>((total, row) => {
    if (!row.parts) return total;
    return {
      input: total.input + row.parts.input,
      cacheRead: total.cacheRead + row.parts.cacheRead,
      cacheWrite: total.cacheWrite + row.parts.cacheWrite,
      output: total.output + row.parts.output,
    };
  }, { input: 0, cacheRead: 0, cacheWrite: 0, output: 0 });
}

function partsTotal(parts: CostParts): number {
  return parts.input + parts.cacheRead + parts.cacheWrite + parts.output;
}

function spanHours(data: UsageBreakdownResponse | undefined): number | null {
  if (!data?.buckets.length) return null;
  const first = Date.parse(data.buckets[0].started_at);
  const last = Date.parse(data.buckets[data.buckets.length - 1].started_at);
  if (!Number.isFinite(first) || !Number.isFinite(last) || last < first) return null;
  const bucket = Number(data.bucket_seconds) || 0;
  return (last - first + bucket * 1000) / 3_600_000;
}

export function buildCostSummary(
  index: PriceIndex | null,
  data: UsageBreakdownResponse | undefined,
  totalTokens: number,
  hours: number | null,
): CostSummary {
  const rows: CostModelRow[] = (data?.groups ?? []).map((group: BreakdownGroup) => {
    const key = group.key;
    const providerId = typeof key === "string" ? null : key.provider_id ?? null;
    const model = typeof key === "string" ? key : key.upstream_model_id ?? "";
    const entry = lookupPrice(index, model);
    const parts = entry ? costParts(entry, group.stats) : null;
    return {
      id: `${providerId ?? ""}/${model || "__unattributed__"}`,
      providerId,
      upstreamModel: model || "未归属",
      displayName: model || "未归属",
      entry,
      stats: group.stats,
      parts,
      cost: parts ? partsTotal(parts) : null,
    };
  });

  // 先按成本降序；没有定价的沉底，避免「—」排在金额中间看起来像一个 0 元模型。
  rows.sort((a, b) => {
    if (a.cost === null && b.cost === null) return count(b.stats.total_tokens) - count(a.stats.total_tokens);
    if (a.cost === null) return 1;
    if (b.cost === null) return -1;
    return b.cost - a.cost;
  });

  const pricedRows = rows.filter((row) => row.entry !== null && row.parts !== null);
  const unpricedRows = rows.filter((row) => row.entry === null || row.parts === null);
  const parts = sumParts(pricedRows);
  const splitTokens = data
    ? [...data.groups, ...(data.other ? [data.other] : [])]
      .reduce((sum, group) => sum + count(group.stats.total_tokens), 0)
    : 0;
  // 拆分接口的 total_groups 已包含 other；优先用它，覆盖率才与排行表同一分母。
  const allTokens = splitTokens > 0 ? splitTokens : totalTokens;
  const pricedTokens = pricedRows.reduce((sum, row) => sum + count(row.stats.total_tokens), 0);
  const pricedRequests = pricedRows.reduce((sum, row) => sum + count(row.stats.requests), 0);
  const requestCount = data
    ? [...data.groups, ...(data.other ? [data.other] : [])]
      .reduce((sum, group) => sum + count(group.stats.requests), 0)
    : 0;
  const windowHours = hours ?? spanHours(data);
  return {
    rows,
    pricedRows,
    unpricedRows,
    totalCost: partsTotal(parts),
    parts,
    pricedTokens,
    allTokens,
    pricedRequests,
    coverage: allTokens > 0 ? Math.min(1, pricedTokens / allTokens) : null,
    modelCount: data?.total_groups || rows.length,
    pricedModelCount: pricedRows.length,
    unpricedTokens: unpricedRows.reduce((sum, row) => sum + count(row.stats.total_tokens), 0),
    dayCount: Math.max(1, Math.ceil((windowHours ?? 1) / 24)),
    pricedRequestCount: pricedRequests,
    requestCount,
  };
}

// costSeriesForGroup 直接拿原始 group 的逐桶 token 序列算成本；row 上只保留 stats，
// 不复制一份序列（同一份数据存两遍最容易漂移）。
export function costSeriesForGroup(
  group: BreakdownGroup,
  entry: PriceEntry | null,
  buckets: BreakdownBucket[],
): Array<number | null> {
  if (!entry) return buckets.map(() => null);
  return buckets.map((_, index) => {
    const usage = {
      prompt_tokens: group.series.prompt_tokens[index] ?? 0,
      completion_tokens: group.series.completion_tokens[index] ?? 0,
      cached_tokens: group.series.cached_tokens[index] ?? 0,
      // 拆分序列没有缓存写字段；按 0 计，并在页脚明确说明。
      cache_read_input_tokens: 0,
      cache_creation_input_tokens: 0,
    };
    return estimateCost(entry, usage) ?? 0;
  });
}

export function money(value: number | null | undefined): string {
  if (value === null || value === undefined || !Number.isFinite(value) || value <= 0) return "—";
  return formatCost(value);
}

export function price(value: number | null | undefined): string {
  if (value === null || value === undefined || !Number.isFinite(value)) return "—";
  return formatPrice(value);
}

export function compositionItems(parts: CostParts) {
  return [
    { id: "input", label: "输入", value: parts.input },
    { id: "cache-read", label: "缓存读", value: parts.cacheRead },
    { id: "cache-write", label: "缓存写", value: parts.cacheWrite },
    { id: "output", label: "输出", value: parts.output },
  ];
}
