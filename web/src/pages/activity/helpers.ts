// 用量统计的拆分维度、显示名与筛选映射。
//
// 这些口径必须和旧版 activity.js 一致：拆分维度是「同一组筛选下的另一种切法」，
// 不是新的筛选条件；表格里能点「仅看此项」的维度，必须恰好是全局筛选支持的维度。

import type { ChartSeries } from "@/components/charts/chart-utils";
import type { FilterDimension, MetricFilterState, UpstreamKeyFilter } from "@/lib/filters";
import type { BreakdownGroup, BreakdownKey, MetricStats, UsageBreakdownResponse } from "@/lib/types";
import { formatCompactNumber, formatDurationValue } from "@/lib/chart-math";
import { formatCount, formatPercent, truncate } from "@/lib/format";

export const ACTIVITY_BREAKDOWN_DIMENSIONS = [
  { value: "model", label: "模型" },
  { value: "requested_model", label: "调用名" },
  { value: "provider", label: "供应商" },
  { value: "upstream_model", label: "上游模型" },
  { value: "upstream_key", label: "上游 Key" },
  { value: "access_key", label: "访问密钥" },
  { value: "workspace", label: "工作空间" },
  { value: "caller_type", label: "调用方" },
  { value: "status", label: "状态码" },
  { value: "api_format", label: "入站端点" },
] as const;

export type BreakdownDimension = (typeof ACTIVITY_BREAKDOWN_DIMENSIONS)[number]["value"];

export const ACTIVITY_BREAKDOWN_METRICS = [
  { value: "requests", label: "请求数" },
  { value: "tokens", label: "Token" },
  { value: "prompt_tokens", label: "输入 Token" },
  { value: "completion_tokens", label: "输出 Token" },
  { value: "failures", label: "失败数" },
] as const;

export type BreakdownMetric = (typeof ACTIVITY_BREAKDOWN_METRICS)[number]["value"];

export const ACTIVITY_BREAKDOWN_SHAPES = [
  { value: "area", label: "堆叠面积" },
  { value: "columns", label: "堆叠柱" },
  { value: "percent", label: "100% 占比" },
] as const;

export type BreakdownShape = (typeof ACTIVITY_BREAKDOWN_SHAPES)[number]["value"];

export function asBreakdownDimension(value: string | null): BreakdownDimension | null {
  return ACTIVITY_BREAKDOWN_DIMENSIONS.some((item) => item.value === value)
    ? (value as BreakdownDimension)
    : null;
}

export function asBreakdownMetric(value: string | null): BreakdownMetric | null {
  return ACTIVITY_BREAKDOWN_METRICS.some((item) => item.value === value)
    ? (value as BreakdownMetric)
    : null;
}

export function asBreakdownShape(value: string | null): BreakdownShape | null {
  return ACTIVITY_BREAKDOWN_SHAPES.some((item) => item.value === value)
    ? (value as BreakdownShape)
    : null;
}

export function dimensionLabel(dimension: BreakdownDimension): string {
  return ACTIVITY_BREAKDOWN_DIMENSIONS.find((item) => item.value === dimension)?.label ?? dimension;
}

export function metricLabel(metric: BreakdownMetric): string {
  return ACTIVITY_BREAKDOWN_METRICS.find((item) => item.value === metric)?.label ?? metric;
}

// 「当前筛选已经包含某个维度时，默认切到下一个更有信息量的维度」。
// 例：已经只看一个模型，再按模型拆只会得到一根柱；按上游 Key 拆才看得到它走了哪把 Key。
export function defaultBreakdownDimension(state: MetricFilterState): BreakdownDimension {
  if (state.model.length) {
    if (!state.uk.length) return "upstream_key";
    if (!state.ak.length) return "access_key";
    return "status";
  }
  if (
    state.provider.length
    || state.uk.length
    || state.ak.length
    || state.ws.length
    || state.caller.length
  ) {
    return "model";
  }
  if (state.outcome) return "status";
  return "model";
}

export function breakdownValueFormat(metric: BreakdownMetric): (value: number) => string {
  if (metric === "tokens" || metric === "prompt_tokens" || metric === "completion_tokens") {
    return (value) => `${formatCompactNumber(value)} Token`;
  }
  return (value) => `${formatCompactNumber(value)} 次`;
}

export function breakdownTableValue(metric: BreakdownMetric, value: number): string {
  if (metric === "tokens" || metric === "prompt_tokens" || metric === "completion_tokens") {
    return formatCompactNumber(value);
  }
  return formatCount(Math.round(value));
}

export function breakdownUnit(metric: BreakdownMetric): string {
  return metric === "tokens" || metric === "prompt_tokens" || metric === "completion_tokens"
    ? "Token"
    : "次";
}

// 「其他」行是服务端聚合出来的桶，只有 group_count/stats/series，没有 key；
// 取值函数必须容忍空 key，否则读 key[field] 会抛 TypeError 并让整页崩掉。
function keyString(key: BreakdownKey | null | undefined, field: string): string | null {
  if (key === null || key === undefined) return null;
  if (typeof key === "string") return key || null;
  const value = key[field];
  if (value === null || value === undefined || value === "") return null;
  return String(value);
}

function keyNumber(key: BreakdownKey | null | undefined, field: string): number | null {
  if (key === null || key === undefined) return null;
  if (typeof key === "string") {
    const parsed = Number(key);
    return Number.isFinite(parsed) ? parsed : null;
  }
  const value = key[field];
  if (value === null || value === undefined || value === "") return null;
  const parsed = Number(value);
  return Number.isFinite(parsed) ? parsed : null;
}

export interface GroupDisplay {
  primary: string;
  secondary: string | null;
  statusCode: number | null;
  nullish: boolean;
}

const CALLER_LABELS: Record<string, string> = {
  local: "本机",
  workspace: "工作空间凭据",
  access_key: "访问密钥",
};

export function groupDisplay(key: BreakdownKey, dimension: BreakdownDimension): GroupDisplay {
  switch (dimension) {
    case "model": {
      const value = keyString(key, "model_id");
      return { primary: value ?? "未归属", secondary: null, statusCode: null, nullish: !value };
    }
    case "requested_model": {
      const value = keyString(key, "requested_model_id");
      return { primary: value ?? "未归属", secondary: null, statusCode: null, nullish: !value };
    }
    case "provider": {
      const value = keyString(key, "provider_id");
      return { primary: value ?? "未归属", secondary: null, statusCode: null, nullish: !value };
    }
    case "upstream_model": {
      const model = keyString(key, "upstream_model_id");
      const provider = keyString(key, "provider_id");
      return {
        primary: model ?? "未归属",
        secondary: provider,
        statusCode: null,
        nullish: !model,
      };
    }
    case "upstream_key": {
      const name = keyString(key, "key_name");
      const provider = keyString(key, "provider_id");
      return {
        primary: name ?? "未归属",
        secondary: provider,
        statusCode: null,
        nullish: !name,
      };
    }
    case "access_key": {
      const id = keyString(key, "access_key_id");
      const name = keyString(key, "access_key_name");
      if (!id) return { primary: "非访问密钥调用", secondary: null, statusCode: null, nullish: true };
      return { primary: name ?? id, secondary: name ? id : null, statusCode: null, nullish: false };
    }
    case "workspace": {
      const value = keyString(key, "workspace");
      return { primary: value ?? "未归属", secondary: null, statusCode: null, nullish: !value };
    }
    case "caller_type": {
      const value = keyString(key, "caller_type");
      return {
        primary: value ? CALLER_LABELS[value] ?? value : "未归属",
        secondary: null,
        statusCode: null,
        nullish: !value,
      };
    }
    case "status": {
      const status = keyNumber(key, "status_code");
      return {
        primary: status === null ? "未归属" : `HTTP ${status}`,
        secondary: null,
        statusCode: status,
        nullish: status === null,
      };
    }
    case "api_format": {
      const value = keyString(key, "api_format");
      return { primary: value ?? "未归属", secondary: null, statusCode: null, nullish: !value };
    }
  }
}

export function groupColorKey(key: BreakdownKey, dimension: BreakdownDimension): string {
  const display = groupDisplay(key, dimension);
  // 用稳定标识而不是显示名做颜色键：显示名可能为空、也可能在配置里改名。
  const stable = (() => {
    switch (dimension) {
      case "model": return keyString(key, "model_id");
      case "requested_model": return keyString(key, "requested_model_id");
      case "provider": return keyString(key, "provider_id");
      case "upstream_model": return [keyString(key, "provider_id"), keyString(key, "upstream_model_id")].filter(Boolean).join("/");
      case "upstream_key": return [keyString(key, "provider_id"), keyString(key, "key_name")].filter(Boolean).join("/");
      case "access_key": return keyString(key, "access_key_id");
      case "workspace": return keyString(key, "workspace");
      case "caller_type": return keyString(key, "caller_type");
      case "status": return keyNumber(key, "status_code")?.toString() ?? null;
      case "api_format": return keyString(key, "api_format");
    }
  })();
  return stable || `null:${display.primary}`;
}

export interface BreakdownRow {
  id: string;
  group: BreakdownGroup;
  colorKey: string;
  isOther: boolean;
  display: GroupDisplay;
  otherCount: number;
}

interface OtherGroup extends BreakdownGroup {
  group_count?: number;
}

export function breakdownRows(data: UsageBreakdownResponse | undefined, dimension: BreakdownDimension): BreakdownRow[] {
  if (!data) return [];
  const rows: BreakdownRow[] = data.groups.map((group) => ({
    id: groupColorKey(group.key, dimension),
    group,
    colorKey: groupColorKey(group.key, dimension),
    isOther: false,
    display: groupDisplay(group.key, dimension),
    otherCount: 0,
  }));
  const other = data.other as OtherGroup | null;
  if (other) {
    const count = Number(other.group_count ?? 0);
    rows.push({
      id: "other",
      group: other,
      colorKey: "other",
      isOther: true,
      display: {
        primary: count > 0 ? `其他（${count} 项）` : "其他",
        secondary: null,
        statusCode: null,
        nullish: false,
      },
      otherCount: count,
    });
  }
  return rows;
}

export function groupMetricValues(group: BreakdownGroup, metric: BreakdownMetric): number[] {
  const series = group.series;
  switch (metric) {
    case "requests": return series.requests;
    case "tokens": return series.total_tokens;
    case "prompt_tokens": return series.prompt_tokens;
    case "completion_tokens": return series.completion_tokens;
    case "failures": return series.failures;
  }
}

export function groupMetricTotal(group: BreakdownGroup, metric: BreakdownMetric): number {
  return groupMetricValues(group, metric).reduce((sum, value) => sum + (Number.isFinite(value) ? value : 0), 0);
}

export function breakdownChartSeries(
  data: UsageBreakdownResponse | undefined,
  dimension: BreakdownDimension,
  metric: BreakdownMetric,
  colorOf: (key: string) => string,
): ChartSeries[] {
  if (!data) return [];
  return breakdownRows(data, dimension).map((row) => {
    const values = groupMetricValues(row.group, metric);
    const label = row.isOther
      ? row.display.primary
      : [row.display.primary, row.display.secondary].filter(Boolean).join(" · ");
    return {
      id: row.id,
      label: truncate(label, 34),
      color: row.isOther ? "var(--series-other)" : colorOf(row.colorKey),
      values: data.buckets.map((_, index) => (
        index < values.length && Number.isFinite(values[index]) ? values[index] : null
      )),
    };
  });
}

export function breakdownTotalRequests(data: UsageBreakdownResponse | undefined): number {
  if (!data) return 0;
  return [...data.groups, ...(data.other ? [data.other] : [])]
    .reduce((sum, group) => sum + numberField(group.stats, "requests"), 0);
}

export function numberField(stats: MetricStats | undefined, field: keyof MetricStats): number {
  const value = stats?.[field];
  const number = Number(value);
  return Number.isFinite(number) ? number : 0;
}

// 「其他」不是可筛选目标：它代表 limit 之外的一整批分组，没有可加入全局筛选的
// 单一值（key 为空）。空 key 与字符串 key 一样直接返回 null，交给调用方不展示动作。
export function groupFilterTarget(
  dimension: BreakdownDimension,
  key: BreakdownKey | null | undefined,
): { dimension: FilterDimension; value: string | UpstreamKeyFilter } | null {
  if (key === null || key === undefined || typeof key === "string") return null;
  switch (dimension) {
    case "model": {
      const value = keyString(key, "model_id");
      return value ? { dimension: "model", value } : null;
    }
    case "provider": {
      const value = keyString(key, "provider_id");
      return value ? { dimension: "provider", value } : null;
    }
    case "upstream_key": {
      const providerId = keyString(key, "provider_id");
      const keyName = keyString(key, "key_name");
      return providerId && keyName ? { dimension: "uk", value: { providerId, keyName } } : null;
    }
    case "access_key": {
      const value = keyString(key, "access_key_id");
      return value ? { dimension: "ak", value } : null;
    }
    case "workspace": {
      const value = keyString(key, "workspace");
      return value ? { dimension: "ws", value } : null;
    }
    case "caller_type": {
      const value = keyString(key, "caller_type");
      return value ? { dimension: "caller", value } : null;
    }
    // 上游模型 / 状态码 / 入站端点不是全局筛选维度，因此不提供「仅看此项」。
    case "requested_model":
    case "upstream_model":
    case "status":
    case "api_format":
      return null;
  }
}

export function statusTone(status: number | null): "good" | "warn" | "critical" | "neutral" {
  if (status === null) return "neutral";
  if (status >= 500) return "critical";
  if (status >= 400) return "warn";
  if (status >= 200 && status < 300) return "good";
  return "neutral";
}

export function averageDuration(stats: MetricStats): string {
  const requests = numberField(stats, "requests");
  const total = numberField(stats, "total_duration_ms");
  return requests > 0 ? formatDurationValue(total / requests) : "—";
}

export function successRate(stats: MetricStats): string {
  const requests = numberField(stats, "requests");
  if (requests <= 0) return "—";
  return formatPercent(numberField(stats, "successes"), requests);
}
